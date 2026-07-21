package caldav

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/teambition/rrule-go"
)

// TodoItem 表示从 CalDAV 获取的待办事项
type TodoItem struct {
	UID         string
	Summary     string
	Description string
	DueDate     *time.Time
	Status      string
	Priority    int
	LastModTime time.Time
	ETag        string
	Path        string
}

// EventItem 表示从 CalDAV 获取的日历事件
type EventItem struct {
	UID         string
	Summary     string
	Description string
	StartTime   *time.Time
	EndTime     *time.Time
	// RecurrenceID 标识重复事件中的原始实例时间。重复实例必须使用它建立独立映射。
	RecurrenceID *time.Time
	Recurring    bool
	AllDay       bool
	Location     string
	LastModTime  time.Time
	ETag         string
	Path         string
}

// CalDAVClient 定义与 Radicale 交互的接口
type CalDAVClient interface {
	GetTodos(ctx context.Context) ([]TodoItem, error)
	GetTodo(ctx context.Context, uid string) (*TodoItem, error)
	CreateTodo(ctx context.Context, todo *TodoItem) (*TodoItem, error)
	UpdateTodo(ctx context.Context, todo *TodoItem) (*TodoItem, error)
	DeleteTodo(ctx context.Context, uid string) error
	CompleteTodo(ctx context.Context, uid string) error
	UncompleteTodo(ctx context.Context, uid string) error
	GetEvents(ctx context.Context, start, end time.Time) ([]EventItem, error)
}

// radicaleClient 实现 CalDAVClient 接口
type radicaleClient struct {
	client       *caldav.Client
	serverURL    string
	calendarPath string
	debugLogger  *log.Logger
}

// debugTransport 记录所有 HTTP 请求和响应的调试运输层
type debugTransport struct {
	transport http.RoundTripper
	logger    *log.Logger
}

func (t *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.logger != nil {
		dump, _ := httputil.DumpRequestOut(req, true)
		t.logger.Printf("=== CALDAV REQUEST ===\n%s\n=== END REQUEST ===", string(dump))
	}

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if t.logger != nil {
		dump, _ := httputil.DumpResponse(resp, true)
		t.logger.Printf("=== CALDAV RESPONSE ===\n%s\n=== END RESPONSE ===", string(dump))
	}

	return resp, nil
}

// NewCalDAVClient 创建新的 CalDAV 客户端
func NewCalDAVClient(serverURL, username, password, calendarPath string, debugLogger *log.Logger) (CalDAVClient, error) {
	transport := http.DefaultTransport
	if debugLogger != nil {
		transport = &debugTransport{transport: transport, logger: debugLogger}
	}

	baseClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	httpClient := &basicAuthClient{
		username: username,
		password: password,
		client:   baseClient,
	}

	client, err := caldav.NewClient(httpClient, serverURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create CalDAV client: %w", err)
	}

	if !strings.HasSuffix(calendarPath, "/") {
		calendarPath += "/"
	}

	return &radicaleClient{
		client:       client,
		serverURL:    serverURL,
		calendarPath: calendarPath,
		debugLogger:  debugLogger,
	}, nil
}

// basicAuthClient 实现基本认证的 HTTP 客户端
type basicAuthClient struct {
	username string
	password string
	client   *http.Client
}

func (c *basicAuthClient) Do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(c.username, c.password)
	return c.client.Do(req)
}

// GetTodos 获取所有待办事项
func (c *radicaleClient) GetTodos(ctx context.Context) ([]TodoItem, error) {
	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     ical.CompToDo,
			AllProps: true,
		},
		CompFilter: caldav.CompFilter{
			Name: ical.CompCalendar,
			Comps: []caldav.CompFilter{
				{Name: ical.CompToDo},
			},
		},
	}

	objects, err := c.client.QueryCalendar(ctx, c.calendarPath, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query calendar: %w", err)
	}

	c.debug("GetTodos returned %d objects", len(objects))

	var todos []TodoItem
	for _, obj := range objects {
		todo := convertCalDAVObjectToTodoItem(obj)
		todos = append(todos, todo)
	}

	return todos, nil
}

// GetEvents 获取指定时间范围内的日历事件
func (c *radicaleClient) GetEvents(ctx context.Context, start, end time.Time) ([]EventItem, error) {
	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     ical.CompEvent,
			AllProps: true,
		},
		CompFilter: caldav.CompFilter{
			Name: ical.CompCalendar,
			Comps: []caldav.CompFilter{
				{
					Name:  ical.CompEvent,
					Start: start,
					End:   end,
				},
			},
		},
	}

	c.debug("GetEvents query: start=%s end=%s", start, end)

	objects, err := c.client.QueryCalendar(ctx, c.calendarPath, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}

	c.debug("GetEvents returned %d events", len(objects))

	var events []EventItem
	for _, obj := range objects {
		items, err := convertCalDAVObjectToEventItems(obj, start, end)
		if err != nil {
			return nil, fmt.Errorf("failed to parse event %s: %w", obj.Path, err)
		}
		events = append(events, items...)
	}

	return events, nil
}

func (c *radicaleClient) debug(format string, args ...interface{}) {
	if c.debugLogger != nil {
		c.debugLogger.Printf("[CalDAV] "+format, args...)
	}
}

// convertCalDAVObjectToTodoItem 将 CalDAV 对象转换为 TodoItem
func convertCalDAVObjectToTodoItem(obj caldav.CalendarObject) TodoItem {
	todo := TodoItem{
		Path:        obj.Path,
		ETag:        obj.ETag,
		LastModTime: obj.ModTime,
	}

	if obj.Data != nil {
		for _, component := range obj.Data.Children {
			if component.Name == ical.CompToDo {
				if prop := component.Props.Get(ical.PropUID); prop != nil {
					if text, err := prop.Text(); err == nil {
						todo.UID = text
					}
				}
				if prop := component.Props.Get(ical.PropSummary); prop != nil {
					if text, err := prop.Text(); err == nil {
						todo.Summary = text
					}
				}
				if prop := component.Props.Get(ical.PropDescription); prop != nil {
					if text, err := prop.Text(); err == nil {
						todo.Description = text
					}
				}
				if prop := component.Props.Get(ical.PropDue); prop != nil {
					if t, err := prop.DateTime(time.Local); err == nil {
						todo.DueDate = &t
					}
				}
				if prop := component.Props.Get(ical.PropStatus); prop != nil {
					if text, err := prop.Text(); err == nil {
						todo.Status = text
					}
				}
				if prop := component.Props.Get(ical.PropPriority); prop != nil {
					if p, err := prop.Int(); err == nil {
						todo.Priority = p
					}
				}
			}
		}
	}

	return todo
}

// convertCalDAVObjectToEventItems 将一个 CalDAV 资源转换为时间窗口内的事件实例。
// CalDAV 资源可能包含一个重复事件主项及多个 RECURRENCE-ID 例外项。
func convertCalDAVObjectToEventItems(obj caldav.CalendarObject, windowStart, windowEnd time.Time) ([]EventItem, error) {
	if obj.Data == nil {
		return nil, nil
	}

	loc := windowStart.Location()
	if loc == nil {
		loc = time.Local
	}

	// 例外项（包括 CANCELLED）会替代主项生成的同一 recurrence-id。
	overridden := make(map[string]bool)
	for _, component := range obj.Data.Children {
		if component.Name != ical.CompEvent {
			continue
		}
		if prop := component.Props.Get(ical.PropRecurrenceID); prop != nil {
			if recurrenceID, err := prop.DateTime(loc); err == nil {
				overridden[recurrenceKey(recurrenceID)] = true
			}
		}
	}

	var events []EventItem
	for _, component := range obj.Data.Children {
		if component.Name != ical.CompEvent {
			continue
		}

		item, duration, cancelled, err := convertEventComponent(component, obj, loc)
		if err != nil {
			return nil, err
		}
		if item.StartTime == nil {
			continue
		}

		if item.RecurrenceID != nil {
			// 服务端展开后的实例和显式例外项都走这里。
			if !cancelled && eventOverlaps(item, windowStart, windowEnd) {
				events = append(events, item)
			}
			continue
		}

		recurrenceSet, err := buildRecurrenceSet(component, loc)
		if err != nil {
			return nil, fmt.Errorf("invalid recurrence rule for %s: %w", item.UID, err)
		}
		if recurrenceSet == nil {
			if !cancelled && eventOverlaps(item, windowStart, windowEnd) {
				events = append(events, item)
			}
			continue
		}

		// 本地展开 RRULE/RDATE/EXDATE。向前偏移 duration，确保跨越窗口起点
		// 的长事件仍能被选中。
		item.Recurring = true
		after := windowStart
		if duration > 0 {
			after = after.Add(-duration)
		}
		for _, occurrence := range recurrenceSet.Between(after, windowEnd, true) {
			if overridden[recurrenceKey(occurrence)] {
				continue
			}
			instance := item
			start := occurrence
			end := occurrence.Add(duration)
			instance.StartTime = &start
			instance.EndTime = &end
			instance.RecurrenceID = &start
			if !cancelled && eventOverlaps(instance, windowStart, windowEnd) {
				events = append(events, instance)
			}
		}
	}

	return events, nil
}

// buildRecurrenceSet 构建完整的 DTSTART + RRULE/RDATE - EXDATE 集合。
// go-ical 的便捷方法要求 RRULE 存在，且不能解析单属性中的逗号日期列表；
// CalDAV 数据中这两种写法都很常见，因此在这里补齐。
func buildRecurrenceSet(component *ical.Component, loc *time.Location) (*rrule.Set, error) {
	option, err := component.Props.RecurrenceRule()
	if err != nil {
		return nil, err
	}
	rdateProps := component.Props.Values(ical.PropRecurrenceDates)
	if option == nil && len(rdateProps) == 0 {
		return nil, nil
	}

	start, err := component.Props.DateTime(ical.PropDateTimeStart, loc)
	if err != nil {
		return nil, err
	}
	set := &rrule.Set{}
	set.DTStart(start)
	if option != nil {
		option.Dtstart = start
		rule, err := rrule.NewRRule(*option)
		if err != nil {
			return nil, err
		}
		set.RRule(rule)
	} else {
		set.RDate(start)
	}

	for _, prop := range rdateProps {
		dates, err := parseRecurrenceDateList(prop, loc)
		if err != nil {
			return nil, err
		}
		for _, date := range dates {
			set.RDate(date)
		}
	}
	for _, prop := range component.Props.Values(ical.PropExceptionDates) {
		dates, err := parseRecurrenceDateList(prop, loc)
		if err != nil {
			return nil, err
		}
		for _, date := range dates {
			set.ExDate(date)
		}
	}
	return set, nil
}

func parseRecurrenceDateList(prop ical.Prop, loc *time.Location) ([]time.Time, error) {
	values := strings.Split(prop.Value, ",")
	dates := make([]time.Time, 0, len(values))
	for _, value := range values {
		item := prop
		item.Value = strings.TrimSpace(value)
		date, err := item.DateTime(loc)
		if err != nil {
			return nil, err
		}
		dates = append(dates, date)
	}
	return dates, nil
}

func convertEventComponent(component *ical.Component, obj caldav.CalendarObject, loc *time.Location) (EventItem, time.Duration, bool, error) {
	item := EventItem{
		Path:        obj.Path,
		ETag:        obj.ETag,
		LastModTime: obj.ModTime,
	}

	item.UID, _ = component.Props.Text(ical.PropUID)
	item.Summary, _ = component.Props.Text(ical.PropSummary)
	item.Description, _ = component.Props.Text(ical.PropDescription)
	item.Location, _ = component.Props.Text(ical.PropLocation)
	status, _ := component.Props.Text(ical.PropStatus)

	startProp := component.Props.Get(ical.PropDateTimeStart)
	if startProp == nil {
		return item, 0, strings.EqualFold(status, "CANCELLED"), nil
	}
	start, err := startProp.DateTime(loc)
	if err != nil {
		return item, 0, false, fmt.Errorf("invalid DTSTART for %s: %w", item.UID, err)
	}
	item.StartTime = &start
	item.AllDay = startProp.ValueType() == ical.ValueDate

	var end time.Time
	if endProp := component.Props.Get(ical.PropDateTimeEnd); endProp != nil {
		end, err = endProp.DateTime(loc)
		if err != nil {
			return item, 0, false, fmt.Errorf("invalid DTEND for %s: %w", item.UID, err)
		}
	} else if durationProp := component.Props.Get(ical.PropDuration); durationProp != nil {
		duration, durationErr := durationProp.Duration()
		if durationErr != nil {
			return item, 0, false, fmt.Errorf("invalid DURATION for %s: %w", item.UID, durationErr)
		}
		end = start.Add(duration)
	} else if item.AllDay {
		end = start.AddDate(0, 0, 1)
	} else {
		// 无 DTEND/DURATION 的定时事件在 DTSTART 时刻到期。
		end = start
	}
	item.EndTime = &end

	if recurrenceProp := component.Props.Get(ical.PropRecurrenceID); recurrenceProp != nil {
		recurrenceID, recurrenceErr := recurrenceProp.DateTime(loc)
		if recurrenceErr != nil {
			return item, 0, false, fmt.Errorf("invalid RECURRENCE-ID for %s: %w", item.UID, recurrenceErr)
		}
		item.RecurrenceID = &recurrenceID
		item.Recurring = true
	}
	if component.Props.Get(ical.PropRecurrenceRule) != nil ||
		len(component.Props.Values(ical.PropRecurrenceDates)) > 0 {
		item.Recurring = true
	}

	return item, end.Sub(start), strings.EqualFold(status, "CANCELLED"), nil
}

func eventOverlaps(item EventItem, windowStart, windowEnd time.Time) bool {
	if item.StartTime == nil || !item.StartTime.Before(windowEnd) {
		return false
	}
	if item.EndTime == nil || item.EndTime.Equal(*item.StartTime) {
		return !item.StartTime.Before(windowStart)
	}
	return item.EndTime.After(windowStart)
}

func recurrenceKey(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// GetTodo 获取指定 UID 的待办事项
func (c *radicaleClient) GetTodo(ctx context.Context, uid string) (*TodoItem, error) {
	todos, err := c.GetTodos(ctx)
	if err != nil {
		return nil, err
	}
	for _, todo := range todos {
		if todo.UID == uid {
			return &todo, nil
		}
	}
	return nil, fmt.Errorf("todo with UID %s not found", uid)
}

// CreateTodo 创建新的待办事项
func (c *radicaleClient) CreateTodo(ctx context.Context, todo *TodoItem) (*TodoItem, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Chronos//EN")

	now := time.Now()
	vtodo := ical.NewComponent(ical.CompToDo)
	vtodo.Props.SetDateTime(ical.PropDateTimeStamp, now)
	vtodo.Props.SetText(ical.PropUID, todo.UID)
	vtodo.Props.SetText(ical.PropSummary, todo.Summary)

	if todo.Description != "" {
		vtodo.Props.SetText(ical.PropDescription, todo.Description)
	}
	if todo.DueDate != nil {
		vtodo.Props.SetDateTime(ical.PropDue, *todo.DueDate)
	}
	if todo.Status != "" {
		vtodo.Props.SetText(ical.PropStatus, todo.Status)
	}
	if todo.Priority > 0 {
		vtodo.Props.SetText(ical.PropPriority, strconv.Itoa(todo.Priority))
	}

	cal.Children = append(cal.Children, vtodo)

	path := c.calendarPath + todo.UID + ".ics"

	if c.debugLogger != nil {
		var buf bytes.Buffer
		ical.NewEncoder(&buf).Encode(cal)
		c.debugLogger.Printf("[CalDAV] CREATE todo: path=%s\n%s", path, buf.String())
	}

	obj, err := c.client.PutCalendarObject(ctx, path, cal)
	if err != nil {
		return nil, fmt.Errorf("failed to create todo: %w", err)
	}

	result := convertCalDAVObjectToTodoItem(*obj)
	return &result, nil
}

// UpdateTodo 更新待办事项
func (c *radicaleClient) UpdateTodo(ctx context.Context, todo *TodoItem) (*TodoItem, error) {
	existing, err := c.GetTodo(ctx, todo.UID)
	if err != nil {
		return nil, err
	}

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Chronos//EN")

	now := time.Now()
	vtodo := ical.NewComponent(ical.CompToDo)
	vtodo.Props.SetDateTime(ical.PropDateTimeStamp, now)
	vtodo.Props.SetText(ical.PropUID, todo.UID)
	vtodo.Props.SetText(ical.PropSummary, todo.Summary)

	if todo.Description != "" {
		vtodo.Props.SetText(ical.PropDescription, todo.Description)
	}
	if todo.DueDate != nil {
		vtodo.Props.SetDateTime(ical.PropDue, *todo.DueDate)
	}
	if todo.Status != "" {
		vtodo.Props.SetText(ical.PropStatus, todo.Status)
	}
	if todo.Priority > 0 {
		vtodo.Props.SetText(ical.PropPriority, strconv.Itoa(todo.Priority))
	}

	cal.Children = append(cal.Children, vtodo)

	c.debug("UPDATE todo: path=%s uid=%s", existing.Path, todo.UID)

	obj, err := c.client.PutCalendarObject(ctx, existing.Path, cal)
	if err != nil {
		return nil, fmt.Errorf("failed to update todo: %w", err)
	}

	result := convertCalDAVObjectToTodoItem(*obj)
	return &result, nil
}

// DeleteTodo 删除待办事项
func (c *radicaleClient) DeleteTodo(ctx context.Context, uid string) error {
	existing, err := c.GetTodo(ctx, uid)
	if err != nil {
		return err
	}

	c.debug("DELETE todo: path=%s uid=%s", existing.Path, uid)

	webdavClient := c.client.Client
	if err := webdavClient.RemoveAll(ctx, existing.Path); err != nil {
		return fmt.Errorf("failed to delete todo: %w", err)
	}

	return nil
}

// CompleteTodo 标记待办事项为已完成
func (c *radicaleClient) CompleteTodo(ctx context.Context, uid string) error {
	todo, err := c.GetTodo(ctx, uid)
	if err != nil {
		return err
	}
	todo.Status = "COMPLETED"
	_, err = c.UpdateTodo(ctx, todo)
	return err
}

// UncompleteTodo 标记待办事项为未完成
func (c *radicaleClient) UncompleteTodo(ctx context.Context, uid string) error {
	todo, err := c.GetTodo(ctx, uid)
	if err != nil {
		return err
	}
	todo.Status = "NEEDS-ACTION"
	_, err = c.UpdateTodo(ctx, todo)
	return err
}
