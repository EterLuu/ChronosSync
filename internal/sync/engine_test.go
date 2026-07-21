package sync

import (
	"context"
	"io"
	"log"
	"sort"
	"testing"
	"time"

	"github.com/EterLuu/ChronosSync/internal/caldav"
	"github.com/EterLuu/ChronosSync/internal/device"
	"github.com/EterLuu/ChronosSync/internal/store"
)

func TestSyncEventsAppearsDayBeforeAndIsDeletedAfterExpiry(t *testing.T) {
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, loc)
	start := time.Date(2026, 7, 22, 15, 0, 0, 0, loc)
	end := start.Add(time.Hour)

	calendar := &fakeCalendar{
		events: []caldav.EventItem{{
			UID: "meeting@example.test", Summary: "周会", StartTime: &start, EndTime: &end, ETag: "e1",
		}},
	}
	deviceClient := newFakeDevice()
	storage := newFakeStore()
	engine := newTestEngine(CalendarEntry{Client: calendar, CalType: CalTypeCalendar, Path: "/events/"}, deviceClient, storage)
	engine.now = func() time.Time { return now }

	result := engine.syncEvents(ctx, engine.calendars[0], deviceClient.todos, nil)
	if len(result.Errors) != 0 {
		t.Fatalf("first sync errors: %v", result.Errors)
	}
	if result.EventsCreated != 1 || len(deviceClient.todos) != 1 {
		t.Fatalf("created=%d device todos=%d", result.EventsCreated, len(deviceClient.todos))
	}
	if !calendar.queryStart.Equal(now) {
		t.Fatalf("query start=%v, want %v", calendar.queryStart, now)
	}
	wantEnd := time.Date(2026, 7, 23, 0, 0, 0, 0, loc)
	if !calendar.queryEnd.Equal(wantEnd) {
		t.Fatalf("query end=%v, want %v", calendar.queryEnd, wantEnd)
	}

	// 事件到期后 CalDAV 的时间窗口不再返回它，设备端应真实删除而非仅完成。
	now = end.Add(time.Minute)
	calendar.events = nil
	mappings, _ := storage.GetMappings(ctx)
	result = engine.syncEvents(ctx, engine.calendars[0], deviceClient.todos, mappings)
	if len(result.Errors) != 0 {
		t.Fatalf("expiry sync errors: %v", result.Errors)
	}
	if result.Deleted != 1 || len(deviceClient.deleted) != 1 || len(deviceClient.todos) != 0 {
		t.Fatalf("deleted=%d delete calls=%v todos=%v", result.Deleted, deviceClient.deleted, deviceClient.todos)
	}
	mappings, _ = storage.GetMappings(ctx)
	if len(mappings) != 0 {
		t.Fatalf("expired mapping was retained: %#v", mappings)
	}
}

func TestSyncEventsUsesIndependentRecurringInstanceMappings(t *testing.T) {
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, loc)
	first := time.Date(2026, 7, 21, 15, 0, 0, 0, loc)
	second := time.Date(2026, 7, 22, 15, 0, 0, 0, loc)
	firstEnd, secondEnd := first.Add(time.Hour), second.Add(time.Hour)
	calendar := &fakeCalendar{events: []caldav.EventItem{
		{UID: "series", Summary: "实例一", StartTime: &first, EndTime: &firstEnd, RecurrenceID: &first, Recurring: true},
		{UID: "series", Summary: "实例二", StartTime: &second, EndTime: &secondEnd, RecurrenceID: &second, Recurring: true},
	}}
	deviceClient := newFakeDevice()
	storage := newFakeStore()
	engine := newTestEngine(CalendarEntry{Client: calendar, CalType: CalTypeCalendar, Path: "/events/"}, deviceClient, storage)
	engine.now = func() time.Time { return now }

	result := engine.syncEvents(ctx, engine.calendars[0], deviceClient.todos, nil)
	if len(result.Errors) != 0 || result.EventsCreated != 2 {
		t.Fatalf("result=%+v", result)
	}
	mappings, _ := storage.GetMappings(ctx)
	if len(mappings) != 2 || mappings[0].CalDAVUID == mappings[1].CalDAVUID {
		t.Fatalf("recurring mappings=%#v", mappings)
	}
}

func TestSyncTodosPropagatesDeletionBothWays(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)

	t.Run("CalDAV deletion removes device todo", func(t *testing.T) {
		calendar := &fakeCalendar{}
		deviceClient := newFakeDevice()
		deviceClient.todos[10] = device.DeviceTodo{ID: 10, Title: "gone remotely"}
		storage := newFakeStore()
		mapping := store.Mapping{CalendarPath: "/todos/", CalDAVUID: "todo-1", DeviceTodoID: 10, LastSyncTime: now}
		_ = storage.CreateMapping(ctx, &mapping)
		engine := newTestEngine(CalendarEntry{Client: calendar, CalType: CalTypeTodos, Path: "/todos/"}, deviceClient, storage)
		engine.now = func() time.Time { return now }

		mappings, _ := storage.GetMappings(ctx)
		result := engine.syncTodos(ctx, engine.calendars[0], deviceClient.todos, mappings, false)
		if len(result.Errors) != 0 || len(deviceClient.deleted) != 1 || len(storage.mappings) != 0 {
			t.Fatalf("result=%+v deleted=%v mappings=%v", result, deviceClient.deleted, storage.mappings)
		}
	})

	t.Run("device deletion removes CalDAV todo", func(t *testing.T) {
		calendar := &fakeCalendar{todos: []caldav.TodoItem{{UID: "todo-2", Summary: "deleted on device"}}}
		deviceClient := newFakeDevice()
		storage := newFakeStore()
		mapping := store.Mapping{CalendarPath: "/todos/", CalDAVUID: "todo-2", DeviceTodoID: 20, LastSyncTime: now}
		_ = storage.CreateMapping(ctx, &mapping)
		engine := newTestEngine(CalendarEntry{Client: calendar, CalType: CalTypeTodos, Path: "/todos/"}, deviceClient, storage)

		mappings, _ := storage.GetMappings(ctx)
		result := engine.syncTodos(ctx, engine.calendars[0], deviceClient.todos, mappings, false)
		if len(result.Errors) != 0 || len(calendar.deleted) != 1 || calendar.deleted[0] != "todo-2" {
			t.Fatalf("result=%+v CalDAV deletes=%v", result, calendar.deleted)
		}
	})
}

func TestSingleSidedDeviceEditWinsRegardlessOfConflictFallback(t *testing.T) {
	ctx := context.Background()
	calendar := &fakeCalendar{todos: []caldav.TodoItem{{
		UID: "todo-1", Summary: "old title", Status: "NEEDS-ACTION", ETag: "etag-1",
	}}}
	deviceClient := newFakeDevice()
	deviceClient.todos[7] = device.DeviceTodo{ID: 7, Title: "new title", UpdateDate: 101}
	storage := newFakeStore()
	mapping := store.Mapping{
		CalendarPath: "/todos/", CalDAVUID: "todo-1", DeviceTodoID: 7,
		DeviceUpdateDate: 100, CalDAVETag: "etag-1", LastSyncTime: time.Unix(100, 0),
	}
	_ = storage.CreateMapping(ctx, &mapping)
	engine := newTestEngine(CalendarEntry{Client: calendar, CalType: CalTypeTodos, Path: "/todos/"}, deviceClient, storage)
	engine.conflictPolicy = RadicaleWins

	mappings, _ := storage.GetMappings(ctx)
	result := engine.syncTodos(ctx, engine.calendars[0], deviceClient.todos, mappings, false)
	if len(result.Errors) != 0 {
		t.Fatalf("sync errors: %v", result.Errors)
	}
	if calendar.updated == nil || calendar.updated.Summary != "new title" {
		t.Fatalf("device-only edit was not written to CalDAV: %#v", calendar.updated)
	}
}

func newTestEngine(entry CalendarEntry, deviceClient *fakeDevice, storage *fakeStore) *SyncEngine {
	return NewSyncEngine([]CalendarEntry{entry}, deviceClient, storage, RadicaleWins, log.New(io.Discard, "", 0))
}

type fakeCalendar struct {
	todos      []caldav.TodoItem
	events     []caldav.EventItem
	deleted    []string
	updated    *caldav.TodoItem
	queryStart time.Time
	queryEnd   time.Time
}

func (f *fakeCalendar) GetTodos(context.Context) ([]caldav.TodoItem, error) {
	return append([]caldav.TodoItem(nil), f.todos...), nil
}
func (f *fakeCalendar) GetTodo(_ context.Context, uid string) (*caldav.TodoItem, error) {
	for i := range f.todos {
		if f.todos[i].UID == uid {
			copy := f.todos[i]
			return &copy, nil
		}
	}
	return nil, nil
}
func (f *fakeCalendar) CreateTodo(_ context.Context, todo *caldav.TodoItem) (*caldav.TodoItem, error) {
	copy := *todo
	copy.ETag = "created-etag"
	f.todos = append(f.todos, copy)
	return &copy, nil
}
func (f *fakeCalendar) UpdateTodo(_ context.Context, todo *caldav.TodoItem) (*caldav.TodoItem, error) {
	copy := *todo
	copy.ETag = "updated-etag"
	f.updated = &copy
	return &copy, nil
}
func (f *fakeCalendar) DeleteTodo(_ context.Context, uid string) error {
	f.deleted = append(f.deleted, uid)
	return nil
}
func (f *fakeCalendar) CompleteTodo(context.Context, string) error   { return nil }
func (f *fakeCalendar) UncompleteTodo(context.Context, string) error { return nil }
func (f *fakeCalendar) GetEvents(_ context.Context, start, end time.Time) ([]caldav.EventItem, error) {
	f.queryStart, f.queryEnd = start, end
	return append([]caldav.EventItem(nil), f.events...), nil
}

type fakeDevice struct {
	todos   map[int]device.DeviceTodo
	nextID  int
	deleted []int
}

func newFakeDevice() *fakeDevice {
	return &fakeDevice{todos: make(map[int]device.DeviceTodo), nextID: 100}
}
func (f *fakeDevice) GetDevices(context.Context) ([]device.Device, error) { return nil, nil }
func (f *fakeDevice) GetTodos(context.Context, string, *int) ([]device.DeviceTodo, error) {
	result := make([]device.DeviceTodo, 0, len(f.todos))
	for _, todo := range f.todos {
		result = append(result, todo)
	}
	return result, nil
}
func (f *fakeDevice) CreateTodo(_ context.Context, todo *device.DeviceTodo) (*device.DeviceTodo, error) {
	f.nextID++
	created := *todo
	created.ID = f.nextID
	created.UpdateDate = int64(f.nextID)
	f.todos[created.ID] = created
	return &created, nil
}
func (f *fakeDevice) UpdateTodo(_ context.Context, id int, todo *device.DeviceTodo) (*device.DeviceTodo, error) {
	updated := *todo
	updated.ID = id
	updated.UpdateDate = f.todos[id].UpdateDate + 1
	f.todos[id] = updated
	return &updated, nil
}
func (f *fakeDevice) CompleteTodo(_ context.Context, id int) error {
	todo := f.todos[id]
	todo.Completed = !todo.Completed
	if todo.Completed {
		todo.Status = 1
	} else {
		todo.Status = 0
	}
	todo.UpdateDate++
	f.todos[id] = todo
	return nil
}
func (f *fakeDevice) UncompleteTodo(ctx context.Context, id int) error {
	return f.CompleteTodo(ctx, id)
}
func (f *fakeDevice) DeleteTodo(_ context.Context, id int) error {
	f.deleted = append(f.deleted, id)
	delete(f.todos, id)
	return nil
}
func (f *fakeDevice) GetDeviceID() string { return "device-1" }

type fakeStore struct {
	mappings map[string]store.Mapping
	nextID   int64
}

func newFakeStore() *fakeStore           { return &fakeStore{mappings: make(map[string]store.Mapping)} }
func mappingKey(path, uid string) string { return path + "\x00" + uid }
func (f *fakeStore) CreateMapping(_ context.Context, mapping *store.Mapping) error {
	f.nextID++
	mapping.ID = f.nextID
	f.mappings[mappingKey(mapping.CalendarPath, mapping.CalDAVUID)] = *mapping
	return nil
}
func (f *fakeStore) GetMapping(_ context.Context, path, uid string) (*store.Mapping, error) {
	mapping, ok := f.mappings[mappingKey(path, uid)]
	if !ok {
		return nil, nil
	}
	return &mapping, nil
}
func (f *fakeStore) GetMappingByDeviceID(_ context.Context, id int) (*store.Mapping, error) {
	for _, mapping := range f.mappings {
		if mapping.DeviceTodoID == id {
			copy := mapping
			return &copy, nil
		}
	}
	return nil, nil
}
func (f *fakeStore) GetMappings(context.Context) ([]store.Mapping, error) {
	result := make([]store.Mapping, 0, len(f.mappings))
	for _, mapping := range f.mappings {
		result = append(result, mapping)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
func (f *fakeStore) UpdateMapping(_ context.Context, mapping *store.Mapping) error {
	for key, existing := range f.mappings {
		if existing.ID == mapping.ID {
			delete(f.mappings, key)
			break
		}
	}
	f.mappings[mappingKey(mapping.CalendarPath, mapping.CalDAVUID)] = *mapping
	return nil
}
func (f *fakeStore) DeleteMapping(_ context.Context, path, uid string) error {
	delete(f.mappings, mappingKey(path, uid))
	return nil
}
func (f *fakeStore) CreateConflict(context.Context, *store.Conflict) error { return nil }
func (f *fakeStore) GetConflict(context.Context, int64) (*store.Conflict, error) {
	return nil, nil
}
func (f *fakeStore) GetPendingConflicts(context.Context) ([]store.Conflict, error) {
	return nil, nil
}
func (f *fakeStore) UpdateConflict(context.Context, *store.Conflict) error { return nil }
func (f *fakeStore) CreateSyncLog(context.Context, *store.SyncLog) error   { return nil }
func (f *fakeStore) GetSyncLogs(context.Context, int) ([]store.SyncLog, error) {
	return nil, nil
}
func (f *fakeStore) Close() error { return nil }
