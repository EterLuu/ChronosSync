package sync

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/EterLuu/ChronosSync/internal/caldav"
	"github.com/EterLuu/ChronosSync/internal/device"
	"github.com/EterLuu/ChronosSync/internal/store"
)

type ConflictResolution string

const (
	DeviceWins   ConflictResolution = "device_wins"
	RadicaleWins ConflictResolution = "radicale_wins"
	Manual       ConflictResolution = "manual"
)

type CalendarType string

const (
	CalTypeTodos            CalendarType = "todos"
	CalTypeCalendar         CalendarType = "calendar"
	CalTypeCalendarAndTodos CalendarType = "calendar_and_todos"
)

const eventUIDPrefix = "event-"

type CalendarEntry struct {
	Client  caldav.CalDAVClient
	CalType CalendarType
	Path    string
}

type SyncEngine struct {
	calendars      []CalendarEntry
	deviceClient   device.DeviceClient
	store          store.Store
	conflictPolicy ConflictResolution
	logger         *log.Logger
	now            func() time.Time
}

func (e *SyncEngine) primaryCalendar() *CalendarEntry {
	for i := range e.calendars {
		if e.calendars[i].CalType == CalTypeTodos || e.calendars[i].CalType == CalTypeCalendarAndTodos {
			return &e.calendars[i]
		}
	}
	return nil
}

func NewSyncEngine(
	calendars []CalendarEntry,
	deviceClient device.DeviceClient,
	store store.Store,
	conflictPolicy ConflictResolution,
	logger *log.Logger,
) *SyncEngine {
	hasTodos := false
	for _, c := range calendars {
		if c.CalType == CalTypeTodos || c.CalType == CalTypeCalendarAndTodos {
			hasTodos = true
			break
		}
	}
	if !hasTodos {
		logger.Println("WARNING: no calendar configured for todos sync")
	}
	return &SyncEngine{
		calendars:      calendars,
		deviceClient:   deviceClient,
		store:          store,
		conflictPolicy: conflictPolicy,
		logger:         logger,
		now:            time.Now,
	}
}

type SyncResult struct {
	Created       int
	Updated       int
	Deleted       int
	Errors        []error
	EventsCreated int
}

func (r *SyncResult) merge(other *SyncResult) {
	r.Created += other.Created
	r.Updated += other.Updated
	r.Deleted += other.Deleted
	r.EventsCreated += other.EventsCreated
	r.Errors = append(r.Errors, other.Errors...)
}

func (e *SyncEngine) refreshDeviceIndex(ctx context.Context) (map[int]device.DeviceTodo, error) {
	todos, err := e.deviceClient.GetTodos(ctx, e.deviceClient.GetDeviceID(), nil)
	if err != nil {
		return nil, err
	}
	idx := make(map[int]device.DeviceTodo, len(todos))
	for _, todo := range todos {
		idx[todo.ID] = todo
	}
	return idx, nil
}

// Sync 执行一次完整同步。VTODO 双向同步；VEVENT 只由 CalDAV 投影到设备。
func (e *SyncEngine) Sync(ctx context.Context) (*SyncResult, error) {
	e.logger.Println("Starting sync...")
	result := &SyncResult{}

	deviceIndex, err := e.refreshDeviceIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get device todos: %w", err)
	}
	mappings, err := e.store.GetMappings(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get mappings: %w", err)
	}

	// 旧版本的事件映射没有日历路径且一个重复系列只保留一条映射，无法可靠
	// 判断所属日历或实例。清理一次后会按新的实例键重建当前应显示的事件。
	result.merge(e.cleanupLegacyEventMappings(ctx, deviceIndex, mappings))
	mappings, err = e.store.GetMappings(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh mappings: %w", err)
	}

	legacyTodoOwnerAssigned := false
	for i := range e.calendars {
		entry := e.calendars[i]
		e.logger.Printf("Processing calendar: %s (type=%s)", entry.Path, entry.CalType)

		if entry.CalType == CalTypeTodos || entry.CalType == CalTypeCalendarAndTodos {
			claimLegacy := !legacyTodoOwnerAssigned
			legacyTodoOwnerAssigned = true
			result.merge(e.syncTodos(ctx, entry, deviceIndex, mappings, claimLegacy))
			mappings, err = e.store.GetMappings(ctx)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("refresh mappings after todos: %w", err))
				continue
			}
		}

		if entry.CalType == CalTypeCalendar || entry.CalType == CalTypeCalendarAndTodos {
			result.merge(e.syncEvents(ctx, entry, deviceIndex, mappings))
			mappings, err = e.store.GetMappings(ctx)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("refresh mappings after events: %w", err))
				continue
			}
		}
	}

	deviceIndex, err = e.refreshDeviceIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh device todos: %w", err)
	}
	mappings, err = e.store.GetMappings(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh final mappings: %w", err)
	}

	mappedDeviceIDs := make(map[int]bool, len(mappings))
	for _, mapping := range mappings {
		mappedDeviceIDs[mapping.DeviceTodoID] = true
	}

	// 未映射的设备待办写入第一个启用 VTODO 的日历。没有 VTODO 日历时，
	// 设备待办保持原样，不会因为仅配置事件日历而触发空指针或误写。
	if primary := e.primaryCalendar(); primary != nil {
		for _, deviceTodo := range deviceIndex {
			if mappedDeviceIDs[deviceTodo.ID] {
				continue
			}
			e.logger.Printf("New device todo: %d (%s)", deviceTodo.ID, deviceTodo.Title)
			caldavTodo := e.convertDeviceToCalDAV(&deviceTodo)
			if err := e.createCalDAVTodoWithMapping(ctx, *primary, caldavTodo, deviceTodo); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("failed to sync device todo %d: %w", deviceTodo.ID, err))
				continue
			}
			result.Created++
		}
	}

	e.logger.Printf("Sync done: created=%d updated=%d deleted=%d events=%d errors=%d",
		result.Created, result.Updated, result.Deleted, result.EventsCreated, len(result.Errors))
	return result, nil
}

func (e *SyncEngine) cleanupLegacyEventMappings(ctx context.Context, deviceIndex map[int]device.DeviceTodo, mappings []store.Mapping) *SyncResult {
	result := &SyncResult{}
	for _, mapping := range mappings {
		if mapping.CalendarPath != "" || !strings.HasPrefix(mapping.CalDAVUID, eventUIDPrefix) {
			continue
		}
		if _, exists := deviceIndex[mapping.DeviceTodoID]; exists {
			if err := e.deviceClient.DeleteTodo(ctx, mapping.DeviceTodoID); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("delete legacy event todo %d: %w", mapping.DeviceTodoID, err))
				continue
			}
			delete(deviceIndex, mapping.DeviceTodoID)
			result.Deleted++
		}
		if err := e.store.DeleteMapping(ctx, mapping.CalendarPath, mapping.CalDAVUID); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("delete legacy event mapping %s: %w", mapping.CalDAVUID, err))
		}
	}
	return result
}

// syncTodos 同步单个日历的 VTODO，并传播两端的删除。
func (e *SyncEngine) syncTodos(ctx context.Context, entry CalendarEntry, deviceIndex map[int]device.DeviceTodo, mappings []store.Mapping, claimLegacy bool) *SyncResult {
	result := &SyncResult{}
	caldavTodos, err := entry.Client.GetTodos(ctx)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("failed to get CalDAV todos from %s: %w", entry.Path, err))
		return result
	}

	mappingByUID := make(map[string]*store.Mapping)
	for i := range mappings {
		mapping := &mappings[i]
		if strings.HasPrefix(mapping.CalDAVUID, eventUIDPrefix) {
			continue
		}
		if mapping.CalendarPath == entry.Path || (claimLegacy && mapping.CalendarPath == "") {
			if mapping.CalendarPath == "" {
				mapping.CalendarPath = entry.Path
				if err := e.store.UpdateMapping(ctx, mapping); err != nil {
					result.Errors = append(result.Errors, fmt.Errorf("claim legacy mapping %s: %w", mapping.CalDAVUID, err))
					continue
				}
			}
			mappingByUID[mapping.CalDAVUID] = mapping
		}
	}

	caldavByUID := make(map[string]caldav.TodoItem, len(caldavTodos))
	for _, todo := range caldavTodos {
		if todo.UID != "" {
			caldavByUID[todo.UID] = todo
		}
	}

	// CalDAV 端删除：删除设备待办及映射。
	for uid, mapping := range mappingByUID {
		if _, exists := caldavByUID[uid]; exists {
			continue
		}
		if _, deviceExists := deviceIndex[mapping.DeviceTodoID]; deviceExists {
			if err := e.deviceClient.DeleteTodo(ctx, mapping.DeviceTodoID); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("delete device todo %d: %w", mapping.DeviceTodoID, err))
				continue
			}
			delete(deviceIndex, mapping.DeviceTodoID)
			result.Deleted++
		}
		if err := e.store.DeleteMapping(ctx, entry.Path, uid); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("delete mapping %s: %w", uid, err))
		}
		delete(mappingByUID, uid)
	}

	for uid, caldavTodo := range caldavByUID {
		mapping, hasMapping := mappingByUID[uid]
		if !hasMapping {
			e.logger.Printf("New CalDAV todo: %s", uid)
			deviceTodo := e.convertCalDAVToDevice(&caldavTodo)
			created, err := e.deviceClient.CreateTodo(ctx, deviceTodo)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("create device todo: %w", err))
				continue
			}
			if isCalDAVCompleted(caldavTodo) && !isDeviceCompleted(*created) {
				if err := e.deviceClient.CompleteTodo(ctx, created.ID); err != nil {
					e.deviceClient.DeleteTodo(ctx, created.ID)
					result.Errors = append(result.Errors, fmt.Errorf("complete new device todo: %w", err))
					continue
				}
			}

			newMapping := &store.Mapping{
				CalendarPath:     entry.Path,
				CalDAVUID:        uid,
				DeviceTodoID:     created.ID,
				DeviceUpdateDate: created.UpdateDate,
				LastSyncTime:     e.now(),
				CalDAVETag:       caldavTodo.ETag,
			}
			if err := e.safeCreateMapping(ctx, newMapping); err != nil {
				e.deviceClient.DeleteTodo(ctx, created.ID)
				result.Errors = append(result.Errors, fmt.Errorf("create mapping: %w", err))
				continue
			}
			indexed := *deviceTodo
			indexed.ID = created.ID
			indexed.UpdateDate = created.UpdateDate
			deviceIndex[created.ID] = indexed
			result.Created++
			continue
		}

		deviceTodo, deviceExists := deviceIndex[mapping.DeviceTodoID]
		if !deviceExists {
			// 设备端删除：删除对应 CalDAV VTODO。
			e.logger.Printf("Mapped device todo %d gone, deleting CalDAV %s", mapping.DeviceTodoID, uid)
			if err := entry.Client.DeleteTodo(ctx, uid); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("delete CalDAV todo: %w", err))
				continue
			}
			if err := e.store.DeleteMapping(ctx, entry.Path, uid); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("delete mapping %s: %w", uid, err))
			}
			result.Deleted++
			continue
		}

		if e.needsUpdate(caldavTodo, deviceTodo, mapping) {
			if err := e.syncUpdate(ctx, entry.Client, &caldavTodo, &deviceTodo, mapping); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("sync update %s: %w", uid, err))
				continue
			}
			deviceIndex[deviceTodo.ID] = deviceTodo
			result.Updated++
		}
	}

	return result
}

// syncEvents 同步当前仍有效、且今天或明天开始的 VEVENT 实例。
func (e *SyncEngine) syncEvents(ctx context.Context, entry CalendarEntry, deviceIndex map[int]device.DeviceTodo, mappings []store.Mapping) *SyncResult {
	result := &SyncResult{}
	now := e.now()
	loc := now.Location()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	windowEnd := dayStart.AddDate(0, 0, 2) // 明天结束（半开区间）

	events, err := entry.Client.GetEvents(ctx, now, windowEnd)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("get events from %s: %w", entry.Path, err))
		return result
	}

	desired := make(map[string]caldav.EventItem)
	for _, event := range events {
		if event.UID == "" || !eventShouldBeVisible(event, now) {
			continue
		}
		desired[eventMappingUID(event)] = event
	}
	e.logger.Printf("Found %d visible event instances for %s", len(desired), entry.Path)

	mappingByUID := make(map[string]*store.Mapping)
	for i := range mappings {
		mapping := &mappings[i]
		if mapping.CalendarPath == entry.Path && strings.HasPrefix(mapping.CalDAVUID, eventUIDPrefix) {
			mappingByUID[mapping.CalDAVUID] = mapping
		}
	}

	// 先清理已经到期、被删除或移出显示窗口的实例，避免重复系列长期堆积。
	for uid, mapping := range mappingByUID {
		if _, exists := desired[uid]; exists {
			continue
		}
		if _, deviceExists := deviceIndex[mapping.DeviceTodoID]; deviceExists {
			if err := e.deviceClient.DeleteTodo(ctx, mapping.DeviceTodoID); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("delete expired event todo %d: %w", mapping.DeviceTodoID, err))
				continue
			}
			delete(deviceIndex, mapping.DeviceTodoID)
			result.Deleted++
		}
		if err := e.store.DeleteMapping(ctx, entry.Path, uid); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("delete event mapping %s: %w", uid, err))
			continue
		}
		delete(mappingByUID, uid)
	}

	for uid, event := range desired {
		mapping, exists := mappingByUID[uid]
		if !exists {
			deviceTodo := e.convertEventToDevice(&event)
			created, err := e.deviceClient.CreateTodo(ctx, deviceTodo)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("create event todo: %w", err))
				continue
			}
			mapping = &store.Mapping{
				CalendarPath:     entry.Path,
				CalDAVUID:        uid,
				DeviceTodoID:     created.ID,
				DeviceUpdateDate: created.UpdateDate,
				LastSyncTime:     now,
				CalDAVETag:       event.ETag,
			}
			if err := e.safeCreateMapping(ctx, mapping); err != nil {
				e.deviceClient.DeleteTodo(ctx, created.ID)
				result.Errors = append(result.Errors, fmt.Errorf("create event mapping: %w", err))
				continue
			}
			indexed := *deviceTodo
			indexed.ID = created.ID
			indexed.UpdateDate = created.UpdateDate
			deviceIndex[created.ID] = indexed
			result.EventsCreated++
			continue
		}

		deviceTodo, deviceExists := deviceIndex[mapping.DeviceTodoID]
		expected := e.convertEventToDevice(&event)
		if !deviceExists {
			created, err := e.deviceClient.CreateTodo(ctx, expected)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("recreate event todo: %w", err))
				continue
			}
			mapping.DeviceTodoID = created.ID
			mapping.DeviceUpdateDate = created.UpdateDate
			mapping.CalDAVETag = event.ETag
			mapping.LastSyncTime = now
			if err := e.store.UpdateMapping(ctx, mapping); err != nil {
				e.deviceClient.DeleteTodo(ctx, created.ID)
				result.Errors = append(result.Errors, fmt.Errorf("update recreated event mapping: %w", err))
				continue
			}
			indexed := *expected
			indexed.ID = created.ID
			indexed.UpdateDate = created.UpdateDate
			deviceIndex[created.ID] = indexed
			result.EventsCreated++
			continue
		}

		changed := false
		// 日历投影在设备端是只读提醒。用户误标完成时恢复为未完成，直到事件到期删除。
		if isDeviceCompleted(deviceTodo) {
			if err := e.deviceClient.UncompleteTodo(ctx, deviceTodo.ID); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("uncomplete event todo %d: %w", deviceTodo.ID, err))
				continue
			}
			deviceTodo.Status, deviceTodo.Completed = 0, false
			changed = true
		}
		if !eventTodoFieldsEqual(deviceTodo, *expected) {
			updated, err := e.deviceClient.UpdateTodo(ctx, deviceTodo.ID, expected)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("update event todo %d: %w", deviceTodo.ID, err))
				continue
			}
			updateDate := deviceTodo.UpdateDate
			if updated.UpdateDate != 0 {
				updateDate = updated.UpdateDate
			}
			deviceTodo = *expected
			deviceTodo.ID = mapping.DeviceTodoID
			deviceTodo.UpdateDate = updateDate
			deviceIndex[deviceTodo.ID] = deviceTodo
			changed = true
		}
		if changed {
			deviceIndex[deviceTodo.ID] = deviceTodo
			result.Updated++
		}

		if mapping.CalDAVETag != event.ETag || mapping.DeviceUpdateDate != deviceTodo.UpdateDate || mapping.LastSyncTime.Before(now) {
			mapping.CalDAVETag = event.ETag
			mapping.DeviceUpdateDate = deviceTodo.UpdateDate
			mapping.LastSyncTime = now
			if err := e.store.UpdateMapping(ctx, mapping); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("update event mapping %s: %w", uid, err))
			}
		}
	}

	return result
}

func eventShouldBeVisible(event caldav.EventItem, now time.Time) bool {
	if event.StartTime == nil {
		return false
	}
	start := event.StartTime.In(now.Location())
	visibleFrom := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -1)
	if now.Before(visibleFrom) {
		return false
	}
	expires := start
	if event.EndTime != nil {
		expires = event.EndTime.In(now.Location())
	}
	return !now.After(expires)
}

func eventMappingUID(event caldav.EventItem) string {
	uid := eventUIDPrefix + event.UID
	if event.Recurring {
		instance := event.StartTime
		if event.RecurrenceID != nil {
			instance = event.RecurrenceID
		}
		if instance != nil {
			uid += "::" + instance.UTC().Format("20060102T150405Z")
		}
	}
	return uid
}

func (e *SyncEngine) safeCreateMapping(ctx context.Context, mapping *store.Mapping) error {
	existing, err := e.store.GetMapping(ctx, mapping.CalendarPath, mapping.CalDAVUID)
	if err != nil {
		return fmt.Errorf("check mapping: %w", err)
	}
	if existing != nil {
		existing.DeviceTodoID = mapping.DeviceTodoID
		existing.DeviceUpdateDate = mapping.DeviceUpdateDate
		existing.LastSyncTime = mapping.LastSyncTime
		existing.CalDAVETag = mapping.CalDAVETag
		return e.store.UpdateMapping(ctx, existing)
	}
	return e.store.CreateMapping(ctx, mapping)
}

func (e *SyncEngine) createCalDAVTodoWithMapping(ctx context.Context, entry CalendarEntry, todo *caldav.TodoItem, deviceTodo device.DeviceTodo) error {
	created, err := entry.Client.CreateTodo(ctx, todo)
	if err != nil {
		return err
	}
	mapping := &store.Mapping{
		CalendarPath:     entry.Path,
		CalDAVUID:        created.UID,
		DeviceTodoID:     deviceTodo.ID,
		DeviceUpdateDate: deviceTodo.UpdateDate,
		LastSyncTime:     e.now(),
		CalDAVETag:       created.ETag,
	}
	if err := e.safeCreateMapping(ctx, mapping); err != nil {
		// 建映射失败时回滚 CalDAV 端，避免下一轮产生重复待办。
		_ = entry.Client.DeleteTodo(ctx, created.UID)
		return err
	}
	return nil
}

func (e *SyncEngine) needsUpdate(caldavTodo caldav.TodoItem, deviceTodo device.DeviceTodo, mapping *store.Mapping) bool {
	if isCalDAVCompleted(caldavTodo) != isDeviceCompleted(deviceTodo) {
		return true
	}
	if !e.todoFieldsEqual(caldavTodo, deviceTodo) {
		return true
	}
	if caldavChangedSinceMapping(caldavTodo, mapping) {
		return true
	}
	return deviceChangedSinceMapping(deviceTodo, mapping)
}

func deviceChangedSinceMapping(todo device.DeviceTodo, mapping *store.Mapping) bool {
	if mapping.DeviceUpdateDate != 0 {
		return todo.UpdateDate != 0 && todo.UpdateDate != mapping.DeviceUpdateDate
	}
	return todo.UpdateDate > mapping.LastSyncTime.Unix()
}

func caldavChangedSinceMapping(todo caldav.TodoItem, mapping *store.Mapping) bool {
	return todo.ETag != mapping.CalDAVETag || todo.LastModTime.After(mapping.LastSyncTime)
}

// syncUpdate 使用变更方向同步。只有两端同时变化（或无法判断）时才应用冲突策略。
func (e *SyncEngine) syncUpdate(ctx context.Context, client caldav.CalDAVClient, caldavTodo *caldav.TodoItem, deviceTodo *device.DeviceTodo, mapping *store.Mapping) error {
	caldavChanged := caldavChangedSinceMapping(*caldavTodo, mapping)
	deviceChanged := deviceChangedSinceMapping(*deviceTodo, mapping)

	if isCalDAVCompleted(*caldavTodo) != isDeviceCompleted(*deviceTodo) {
		winner, manual := e.chooseWinner(caldavChanged, deviceChanged)
		if manual {
			return e.createConflict(ctx, caldavTodo, deviceTodo)
		}
		if winner == RadicaleWins {
			if isCalDAVCompleted(*caldavTodo) {
				if err := e.deviceClient.CompleteTodo(ctx, deviceTodo.ID); err != nil {
					return fmt.Errorf("complete device: %w", err)
				}
				deviceTodo.Status, deviceTodo.Completed = 1, true
			} else {
				if err := e.deviceClient.UncompleteTodo(ctx, deviceTodo.ID); err != nil {
					return fmt.Errorf("uncomplete device: %w", err)
				}
				deviceTodo.Status, deviceTodo.Completed = 0, false
			}
		} else {
			if isDeviceCompleted(*deviceTodo) {
				if err := client.CompleteTodo(ctx, caldavTodo.UID); err != nil {
					return fmt.Errorf("complete CalDAV: %w", err)
				}
				caldavTodo.Status = "COMPLETED"
			} else {
				if err := client.UncompleteTodo(ctx, caldavTodo.UID); err != nil {
					return fmt.Errorf("uncomplete CalDAV: %w", err)
				}
				caldavTodo.Status = "NEEDS-ACTION"
			}
		}
	}

	if !e.todoFieldsEqual(*caldavTodo, *deviceTodo) {
		winner, manual := e.chooseWinner(caldavChanged, deviceChanged)
		if manual {
			return e.createConflict(ctx, caldavTodo, deviceTodo)
		}
		if winner == RadicaleWins {
			updated, err := e.deviceClient.UpdateTodo(ctx, deviceTodo.ID, e.convertCalDAVToDevice(caldavTodo))
			if err != nil {
				return fmt.Errorf("update device fields: %w", err)
			}
			if updated.UpdateDate != 0 {
				deviceTodo.UpdateDate = updated.UpdateDate
			}
			expected := e.convertCalDAVToDevice(caldavTodo)
			expected.ID = deviceTodo.ID
			expected.UpdateDate = deviceTodo.UpdateDate
			*deviceTodo = *expected
		} else {
			updated, err := client.UpdateTodo(ctx, e.convertDeviceToCalDAVWithUID(deviceTodo, caldavTodo.UID))
			if err != nil {
				return fmt.Errorf("update CalDAV fields: %w", err)
			}
			*caldavTodo = *updated
		}
	}

	mapping.LastSyncTime = e.now()
	mapping.CalDAVETag = caldavTodo.ETag
	mapping.DeviceUpdateDate = deviceTodo.UpdateDate
	return e.store.UpdateMapping(ctx, mapping)
}

func (e *SyncEngine) chooseWinner(caldavChanged, deviceChanged bool) (ConflictResolution, bool) {
	if caldavChanged && !deviceChanged {
		return RadicaleWins, false
	}
	if !caldavChanged && deviceChanged {
		return DeviceWins, false
	}
	if e.conflictPolicy == Manual {
		return Manual, true
	}
	return e.conflictPolicy, false
}

func (e *SyncEngine) createConflict(ctx context.Context, caldavTodo *caldav.TodoItem, deviceTodo *device.DeviceTodo) error {
	return e.store.CreateConflict(ctx, &store.Conflict{
		CalDAVUID:    caldavTodo.UID,
		DeviceTodoID: deviceTodo.ID,
		DetectedTime: e.now(),
		Status:       "pending",
	})
}

func isCalDAVCompleted(todo caldav.TodoItem) bool {
	return strings.EqualFold(todo.Status, "COMPLETED")
}

func isDeviceCompleted(todo device.DeviceTodo) bool {
	return todo.Completed || todo.Status == 1
}

func (e *SyncEngine) todoFieldsEqual(caldavTodo caldav.TodoItem, deviceTodo device.DeviceTodo) bool {
	expected := e.convertCalDAVToDevice(&caldavTodo)
	return eventTodoFieldsEqual(deviceTodo, *expected)
}

func eventTodoFieldsEqual(actual, expected device.DeviceTodo) bool {
	return actual.Title == expected.Title &&
		actual.Description == expected.Description &&
		actual.DueDate == expected.DueDate &&
		actual.DueTime == expected.DueTime &&
		actual.Priority == expected.Priority
}

// ---- 数据转换 ----

func (e *SyncEngine) convertCalDAVToDevice(todo *caldav.TodoItem) *device.DeviceTodo {
	deviceTodo := &device.DeviceTodo{
		Title:       todo.Summary,
		Description: todo.Description,
		DeviceID:    e.deviceClient.GetDeviceID(),
		RepeatType:  "none",
	}
	if todo.DueDate != nil {
		localDue := todo.DueDate.In(time.Local)
		deviceTodo.DueDate = localDue.Format("2006-01-02")
		deviceTodo.DueTime = localDue.Format("15:04")
	}
	if isCalDAVCompleted(*todo) {
		deviceTodo.Status = 1
		deviceTodo.Completed = true
	}
	switch todo.Priority {
	case 1:
		deviceTodo.Priority = 2
	case 2:
		deviceTodo.Priority = 1
	}
	return deviceTodo
}

func (e *SyncEngine) convertDeviceToCalDAV(todo *device.DeviceTodo) *caldav.TodoItem {
	return e.convertDeviceToCalDAVWithUID(todo, fmt.Sprintf("device-%d@chronos", todo.ID))
}

func (e *SyncEngine) convertDeviceToCalDAVWithUID(todo *device.DeviceTodo, uid string) *caldav.TodoItem {
	caldavTodo := &caldav.TodoItem{
		UID:         uid,
		Summary:     todo.Title,
		Description: todo.Description,
	}
	if todo.DueDate != "" {
		dueStr := todo.DueDate
		layout := "2006-01-02"
		if todo.DueTime != "" {
			dueStr += "T" + todo.DueTime
			layout += "T15:04"
		}
		if due, err := time.ParseInLocation(layout, dueStr, time.Local); err == nil {
			caldavTodo.DueDate = &due
		}
	}
	if isDeviceCompleted(*todo) {
		caldavTodo.Status = "COMPLETED"
	} else {
		caldavTodo.Status = "NEEDS-ACTION"
	}
	switch todo.Priority {
	case 2:
		caldavTodo.Priority = 1
	case 1:
		caldavTodo.Priority = 2
	}
	return caldavTodo
}

func (e *SyncEngine) convertEventToDevice(event *caldav.EventItem) *device.DeviceTodo {
	deviceTodo := &device.DeviceTodo{
		Title:       event.Summary,
		Description: event.Description,
		DeviceID:    e.deviceClient.GetDeviceID(),
		RepeatType:  "none",
	}
	if event.StartTime != nil {
		localStart := event.StartTime.In(time.Local)
		deviceTodo.DueDate = localStart.Format("2006-01-02")
		if !event.AllDay {
			deviceTodo.DueTime = localStart.Format("15:04")
		}
	}
	return deviceTodo
}
