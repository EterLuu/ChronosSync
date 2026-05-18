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
}

func (e *SyncEngine) primaryClient() caldav.CalDAVClient {
	for _, c := range e.calendars {
		if c.CalType == CalTypeTodos || c.CalType == CalTypeCalendarAndTodos {
			return c.Client
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

// refreshDeviceIndex 重新拉取设备待办列表并重建索引
func (e *SyncEngine) refreshDeviceIndex(ctx context.Context) (map[int]device.DeviceTodo, error) {
	todos, err := e.deviceClient.GetTodos(ctx, e.deviceClient.GetDeviceID(), nil)
	if err != nil {
		return nil, err
	}
	idx := make(map[int]device.DeviceTodo, len(todos))
	for _, t := range todos {
		idx[t.ID] = t
	}
	return idx, nil
}

// Sync 执行同步（单次完整流程）
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

	// 建立 event mapped 的设备待办 ID 集合（这些不应回写）
	eventDeviceIDs := make(map[int]bool)
	for _, m := range mappings {
		if strings.HasPrefix(m.CalDAVUID, eventUIDPrefix) {
			eventDeviceIDs[m.DeviceTodoID] = true
		}
	}

	for _, cal := range e.calendars {
		e.logger.Printf("Processing calendar: %s (type=%s)", cal.Path, cal.CalType)

		switch cal.CalType {
		case CalTypeTodos:
			res := e.syncTodos(ctx, cal.Client, deviceIndex, mappings)
			result.merge(res)
		case CalTypeCalendar:
			res := e.syncEvents(ctx, cal.Client, deviceIndex, mappings, eventDeviceIDs)
			result.EventsCreated += res.EventsCreated
			result.Errors = append(result.Errors, res.Errors...)
		case CalTypeCalendarAndTodos:
			res := e.syncTodos(ctx, cal.Client, deviceIndex, mappings)
			result.merge(res)
			// 刷新 deviceIndex — syncTodos 可能创建了新待办
			if res.Created > 0 {
				deviceIndex, _ = e.refreshDeviceIndex(ctx)
			}
			evRes := e.syncEvents(ctx, cal.Client, deviceIndex, mappings, eventDeviceIDs)
			result.EventsCreated += evRes.EventsCreated
			result.Errors = append(result.Errors, evRes.Errors...)
		}

		// 刷新 eventDeviceIDs
		if result.EventsCreated > 0 || result.Created > 0 {
			mappings, _ = e.store.GetMappings(ctx)
			eventDeviceIDs = make(map[int]bool)
			for _, m := range mappings {
				if strings.HasPrefix(m.CalDAVUID, eventUIDPrefix) {
					eventDeviceIDs[m.DeviceTodoID] = true
				}
			}
		}

		mappings, _ = e.store.GetMappings(ctx)
	}

	// 刷新最终设备列表（用于 Device→CalDAV）
	deviceIndex, _ = e.refreshDeviceIndex(ctx)

	// Device → CalDAV：跳过事件衍生的待办
	for _, dt := range deviceIndex {
		if eventDeviceIDs[dt.ID] {
			continue
		}
		hasMapping := false
		for _, m := range mappings {
			if m.DeviceTodoID == dt.ID {
				hasMapping = true
				break
			}
		}
		if !hasMapping {
			e.logger.Printf("New device todo: %d (%s)", dt.ID, dt.Title)
			caldavTodo := e.convertDeviceToCalDAV(&dt)
			if err := e.createCalDAVTodoWithMapping(ctx, caldavTodo, dt.ID); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("failed to sync device todo %d: %w", dt.ID, err))
				continue
			}
			result.Created++
		}
	}

	e.logger.Printf("Sync done: created=%d updated=%d deleted=%d events=%d errors=%d",
		result.Created, result.Updated, result.Deleted, result.EventsCreated, len(result.Errors))
	return result, nil
}

// syncTodos 同步单个日历的 VTODO
func (e *SyncEngine) syncTodos(ctx context.Context, client caldav.CalDAVClient, deviceIndex map[int]device.DeviceTodo, mappings []store.Mapping) *SyncResult {
	result := &SyncResult{}
	caldavTodos, err := client.GetTodos(ctx)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("failed to get CalDAV todos: %w", err))
		return result
	}

	mappingByUID := make(map[string]*store.Mapping)
	for i := range mappings {
		mappingByUID[mappings[i].CalDAVUID] = &mappings[i]
	}

	caldavByUID := make(map[string]caldav.TodoItem)
	for _, t := range caldavTodos {
		caldavByUID[t.UID] = t
	}

	for uid, ct := range caldavByUID {
		mapping, hasMapping := mappingByUID[uid]
		if !hasMapping {
			e.logger.Printf("New CalDAV todo: %s", uid)
			deviceTodo := e.convertCalDAVToDevice(&ct)
			created, err := e.deviceClient.CreateTodo(ctx, deviceTodo)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("create device todo: %w", err))
				continue
			}
			if ct.Status == "COMPLETED" {
				e.deviceClient.CompleteTodo(ctx, created.ID)
			}
			newMapping := &store.Mapping{
				CalDAVUID:    uid,
				DeviceTodoID: created.ID,
				LastSyncTime: time.Now(),
				CalDAVETag:   ct.ETag,
			}
			if err := e.safeCreateMapping(ctx, newMapping); err != nil {
				e.logger.Printf("Rolling back orphan device todo %d", created.ID)
				e.deviceClient.CompleteTodo(ctx, created.ID)
				result.Errors = append(result.Errors, fmt.Errorf("create mapping: %w", err))
				continue
			}
			// 即时更新 deviceIndex，避免陈旧数据
			created.Status = deviceTodo.Status
			created.Completed = deviceTodo.Completed
			deviceIndex[created.ID] = *created
			result.Created++
		} else {
			dt, deviceExists := deviceIndex[mapping.DeviceTodoID]
			if !deviceExists {
				e.logger.Printf("Mapped device todo %d gone, deleting CalDAV %s", mapping.DeviceTodoID, uid)
				if err := client.DeleteTodo(ctx, uid); err != nil {
					result.Errors = append(result.Errors, fmt.Errorf("delete CalDAV todo: %w", err))
					continue
				}
				e.store.DeleteMapping(ctx, uid)
				result.Deleted++
			} else if e.needsUpdate(ct, dt, mapping) {
				e.logger.Printf("Update: %s (caldav=%s dev_done=%v)", uid, ct.Status, dt.Completed)
				if err := e.syncUpdate(ctx, &ct, &dt, mapping); err != nil {
					result.Errors = append(result.Errors, fmt.Errorf("sync update: %w", err))
					continue
				}
				result.Updated++
			}
		}
	}
	return result
}

// syncEvents 同步 VEVENT → 设备待办（单向）
func (e *SyncEngine) syncEvents(ctx context.Context, client caldav.CalDAVClient, deviceIndex map[int]device.DeviceTodo, mappings []store.Mapping, eventDeviceIDs map[int]bool) *SyncResult {
	result := &SyncResult{}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	events, err := client.GetEvents(ctx, start, start.AddDate(0, 0, 7))
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("get events: %w", err))
		return result
	}

	e.logger.Printf("Found %d events in next 7 days", len(events))

	mappingByUID := make(map[string]*store.Mapping)
	for i := range mappings {
		if strings.HasPrefix(mappings[i].CalDAVUID, eventUIDPrefix) {
			mappingByUID[mappings[i].CalDAVUID] = &mappings[i]
		}
	}

	for _, ev := range events {
		if ev.UID == "" {
			continue
		}
		euid := eventUIDPrefix + ev.UID
		mapping, exists := mappingByUID[euid]

		if exists {
			if _, deviceExists := deviceIndex[mapping.DeviceTodoID]; !deviceExists {
				e.logger.Printf("Recreating event todo: %s", ev.UID)
				dt := e.convertEventToDevice(&ev)
				created, err := e.deviceClient.CreateTodo(ctx, dt)
				if err != nil {
					result.Errors = append(result.Errors, fmt.Errorf("recreate event todo: %w", err))
					continue
				}
				mapping.DeviceTodoID = created.ID
				mapping.LastSyncTime = time.Now()
				e.store.UpdateMapping(ctx, mapping)
				deviceIndex[created.ID] = *created
				eventDeviceIDs[created.ID] = true
				result.EventsCreated++
			}
		} else {
			e.logger.Printf("New event: %s (%s)", ev.Summary, ev.UID)
			dt := e.convertEventToDevice(&ev)
			created, err := e.deviceClient.CreateTodo(ctx, dt)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("create event todo: %w", err))
				continue
			}
			m := &store.Mapping{
				CalDAVUID:    euid,
				DeviceTodoID: created.ID,
				LastSyncTime: time.Now(),
				CalDAVETag:   ev.ETag,
			}
			if err := e.safeCreateMapping(ctx, m); err != nil {
				e.logger.Printf("Rolling back orphan event todo %d", created.ID)
				e.deviceClient.CompleteTodo(ctx, created.ID)
				result.Errors = append(result.Errors, fmt.Errorf("create mapping: %w", err))
				continue
			}
			deviceIndex[created.ID] = *created
			eventDeviceIDs[created.ID] = true
			result.EventsCreated++
		}
	}

	return result
}

// safeCreateMapping 创建映射；已存在则更新
func (e *SyncEngine) safeCreateMapping(ctx context.Context, m *store.Mapping) error {
	existing, err := e.store.GetMapping(ctx, m.CalDAVUID)
	if err != nil {
		return fmt.Errorf("check mapping: %w", err)
	}
	if existing != nil {
		existing.DeviceTodoID = m.DeviceTodoID
		existing.LastSyncTime = m.LastSyncTime
		existing.CalDAVETag = m.CalDAVETag
		return e.store.UpdateMapping(ctx, existing)
	}
	return e.store.CreateMapping(ctx, m)
}

// createCalDAVTodoWithMapping 创建 CalDAV VTODO 并建立映射
func (e *SyncEngine) createCalDAVTodoWithMapping(ctx context.Context, todo *caldav.TodoItem, deviceTodoID int) error {
	created, err := e.primaryClient().CreateTodo(ctx, todo)
	if err != nil {
		return err
	}
	m := &store.Mapping{
		CalDAVUID:    created.UID,
		DeviceTodoID: deviceTodoID,
		LastSyncTime: time.Now(),
		CalDAVETag:   created.ETag,
	}
	return e.safeCreateMapping(ctx, m)
}

func (e *SyncEngine) needsUpdate(ct caldav.TodoItem, dt device.DeviceTodo, m *store.Mapping) bool {
	if ct.ETag != m.CalDAVETag {
		return true
	}
	if ct.LastModTime.After(m.LastSyncTime) {
		return true
	}
	if dt.UpdateDate > m.LastSyncTime.Unix() {
		return true
	}
	return false
}

// syncUpdate 状态双向同步（各自比对 mapping.LastSyncTime），策略只影响非状态字段
func (e *SyncEngine) syncUpdate(ctx context.Context, ct *caldav.TodoItem, dt *device.DeviceTodo, m *store.Mapping) error {
	cd := ct.Status == "COMPLETED"
	dd := dt.Completed

	if cd == dd {
		return e.updateFields(ctx, ct, dt, m)
	}

	// 判断各自是否在上次同步后变更（与同一个本地时间参照点比较）
	caldavChanged := ct.LastModTime.After(m.LastSyncTime) || ct.ETag != m.CalDAVETag
	deviceChanged := dt.UpdateDate > m.LastSyncTime.Unix()

	var caldavWins bool
	if caldavChanged && !deviceChanged {
		caldavWins = true
	} else if !caldavChanged && deviceChanged {
		caldavWins = false
	} else {
		// 双方都变或都没变 → 按策略
		caldavWins = e.conflictPolicy == RadicaleWins
	}

	if caldavWins {
		if cd {
			e.logger.Printf("Status sync: caldav wins → complete device %d", dt.ID)
			if err := e.deviceClient.CompleteTodo(ctx, dt.ID); err != nil {
				return fmt.Errorf("complete device: %w", err)
			}
		} else {
			e.logger.Printf("Status sync: caldav wins → uncomplete device %d", dt.ID)
			if err := e.deviceClient.CompleteTodo(ctx, dt.ID); err != nil {
				return fmt.Errorf("uncomplete device: %w", err)
			}
		}
	} else {
		if dd {
			e.logger.Printf("Status sync: device wins → complete CalDAV %s", ct.UID)
			if pc := e.primaryClient(); pc != nil {
				if err := pc.CompleteTodo(ctx, ct.UID); err != nil {
					return fmt.Errorf("complete CalDAV: %w", err)
				}
			}
		} else {
			e.logger.Printf("Status sync: device wins → uncomplete CalDAV %s", ct.UID)
			if pc := e.primaryClient(); pc != nil {
				if err := pc.UncompleteTodo(ctx, ct.UID); err != nil {
					return fmt.Errorf("uncomplete CalDAV: %w", err)
				}
			}
		}
	}

	m.LastSyncTime = time.Now()
	m.CalDAVETag = ct.ETag
	return e.store.UpdateMapping(ctx, m)
}

func (e *SyncEngine) updateFields(ctx context.Context, ct *caldav.TodoItem, dt *device.DeviceTodo, m *store.Mapping) error {
	switch e.conflictPolicy {
	case DeviceWins:
		updated := e.convertDeviceToCalDAV(dt)
		updated.UID = ct.UID
		pc := e.primaryClient()
		if pc == nil {
			return fmt.Errorf("no primary client for todo sync")
		}
		if _, err := pc.UpdateTodo(ctx, updated); err != nil {
			return err
		}
		m.CalDAVETag = updated.ETag
		m.LastSyncTime = time.Now()
		return e.store.UpdateMapping(ctx, m)

	case RadicaleWins:
		updated := e.convertCalDAVToDevice(ct)
		if _, err := e.deviceClient.UpdateTodo(ctx, dt.ID, updated); err != nil {
			return err
		}
		m.LastSyncTime = time.Now()
		m.CalDAVETag = ct.ETag
		return e.store.UpdateMapping(ctx, m)

	case Manual:
		return e.store.CreateConflict(ctx, &store.Conflict{
			CalDAVUID:    ct.UID,
			DeviceTodoID: dt.ID,
			DetectedTime: time.Now(),
			Status:       "pending",
		})

	default:
		return fmt.Errorf("unknown policy: %s", e.conflictPolicy)
	}
}

// ---- 数据转换 ----

func (e *SyncEngine) convertCalDAVToDevice(todo *caldav.TodoItem) *device.DeviceTodo {
	dt := &device.DeviceTodo{
		Title:       todo.Summary,
		Description: todo.Description,
		DeviceID:    e.deviceClient.GetDeviceID(),
	}
	if todo.DueDate != nil {
		dt.DueDate = todo.DueDate.Format("2006-01-02")
		dt.DueTime = todo.DueDate.Format("15:04")
	}
	if todo.Status == "COMPLETED" {
		dt.Status = 1
		dt.Completed = true
	}
	switch todo.Priority {
	case 1:
		dt.Priority = 2
	case 2:
		dt.Priority = 1
	}
	return dt
}

func (e *SyncEngine) convertDeviceToCalDAV(todo *device.DeviceTodo) *caldav.TodoItem {
	ct := &caldav.TodoItem{
		UID:         fmt.Sprintf("device-%d@chronos", todo.ID),
		Summary:     todo.Title,
		Description: todo.Description,
	}
	if todo.DueDate != "" {
		dueStr := todo.DueDate
		if todo.DueTime != "" {
			dueStr += "T" + todo.DueTime + ":00"
		} else {
			dueStr += "T00:00:00"
		}
		if t, err := time.Parse("2006-01-02T15:04:05", dueStr); err == nil {
			ct.DueDate = &t
		}
	}
	if todo.Status == 1 {
		ct.Status = "COMPLETED"
	} else {
		ct.Status = "NEEDS-ACTION"
	}
	switch todo.Priority {
	case 2:
		ct.Priority = 1
	case 1:
		ct.Priority = 2
	}
	return ct
}

func (e *SyncEngine) convertEventToDevice(ev *caldav.EventItem) *device.DeviceTodo {
	dt := &device.DeviceTodo{
		Title:       ev.Summary,
		Description: ev.Description,
		DeviceID:    e.deviceClient.GetDeviceID(),
		Priority:    0,
	}
	if ev.StartTime != nil {
		dt.DueDate = ev.StartTime.Format("2006-01-02")
		dt.DueTime = ev.StartTime.Format("15:04")
	}
	return dt
}
