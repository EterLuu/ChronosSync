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
	Location    string
	LastModTime time.Time
	ETag        string
	Path        string
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
		event := convertCalDAVObjectToEventItem(obj)
		events = append(events, event)
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
					if t, err := prop.DateTime(nil); err == nil {
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

// convertCalDAVObjectToEventItem 将 CalDAV 对象转换为 EventItem
func convertCalDAVObjectToEventItem(obj caldav.CalendarObject) EventItem {
	event := EventItem{
		Path:        obj.Path,
		ETag:        obj.ETag,
		LastModTime: obj.ModTime,
	}

	if obj.Data != nil {
		for _, component := range obj.Data.Children {
			if component.Name == ical.CompEvent {
				if prop := component.Props.Get(ical.PropUID); prop != nil {
					if text, err := prop.Text(); err == nil {
						event.UID = text
					}
				}
				if prop := component.Props.Get(ical.PropSummary); prop != nil {
					if text, err := prop.Text(); err == nil {
						event.Summary = text
					}
				}
				if prop := component.Props.Get(ical.PropDescription); prop != nil {
					if text, err := prop.Text(); err == nil {
						event.Description = text
					}
				}
				if prop := component.Props.Get(ical.PropDateTimeStart); prop != nil {
					if t, err := prop.DateTime(nil); err == nil {
						event.StartTime = &t
					}
				}
				if prop := component.Props.Get(ical.PropDateTimeEnd); prop != nil {
					if t, err := prop.DateTime(nil); err == nil {
						event.EndTime = &t
					}
				}
				if prop := component.Props.Get(ical.PropLocation); prop != nil {
					if text, err := prop.Text(); err == nil {
						event.Location = text
					}
				}
			}
		}
	}

	return event
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
