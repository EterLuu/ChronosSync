package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"time"
)

// DeviceTodo 表示水墨屏设备上的待办事项
type DeviceTodo struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	DueDate     string `json:"dueDate"`
	DueTime     string `json:"dueTime"`
	RepeatType  string `json:"repeatType"`
	Status      int    `json:"status"`
	Priority    int    `json:"priority"`
	Completed   bool   `json:"completed"`
	DeviceID    string `json:"deviceId"`
	DeviceName  string `json:"deviceName"`
	CreateDate  string `json:"createDate"`
	UpdateDate  int64  `json:"updateDate"`
}

// Device 表示水墨屏设备
type Device struct {
	DeviceID string `json:"deviceId"`
	Alias    string `json:"alias"`
	Board    string `json:"board"`
}

// APIResponse 表示 API 响应
type APIResponse struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
	Msg  string          `json:"msg"`
}

// DeviceClient 定义与水墨屏设备 API 交互的接口
type DeviceClient interface {
	GetDevices(ctx context.Context) ([]Device, error)
	GetTodos(ctx context.Context, deviceID string, status *int) ([]DeviceTodo, error)
	CreateTodo(ctx context.Context, todo *DeviceTodo) (*DeviceTodo, error)
	UpdateTodo(ctx context.Context, todoID int, todo *DeviceTodo) (*DeviceTodo, error)
	CompleteTodo(ctx context.Context, todoID int) error
	UncompleteTodo(ctx context.Context, todoID int) error
	GetDeviceID() string
}

// DebugTransport 记录所有 HTTP 请求和响应的调试运输层
type DebugTransport struct {
	Transport http.RoundTripper
	Logger    *log.Logger
	Prefix    string
}

func (t *DebugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Logger != nil {
		dump, _ := httputil.DumpRequestOut(req, true)
		t.Logger.Printf("=== %s REQUEST ===\n%s\n=== END REQUEST ===", t.Prefix, string(dump))
	}

	resp, err := t.Transport.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if t.Logger != nil {
		dump, _ := httputil.DumpResponse(resp, true)
		t.Logger.Printf("=== %s RESPONSE ===\n%s\n=== END RESPONSE ===", t.Prefix, string(dump))
	}

	return resp, nil
}

// zectrixClient 实现 DeviceClient 接口
type zectrixClient struct {
	apiURL      string
	apiKey      string
	deviceID    string
	client      *http.Client
	debugLogger *log.Logger
}

// NewDeviceClient 创建新的设备客户端
func NewDeviceClient(apiURL, apiKey, deviceID string, debugLogger *log.Logger) DeviceClient {
	transport := http.DefaultTransport
	if debugLogger != nil {
		transport = &DebugTransport{
			Transport: transport,
			Logger:    debugLogger,
			Prefix:    "DEVICE",
		}
	}

	return &zectrixClient{
		apiURL:      apiURL,
		apiKey:      apiKey,
		deviceID:    deviceID,
		debugLogger: debugLogger,
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

func (c *zectrixClient) debug(format string, args ...interface{}) {
	if c.debugLogger != nil {
		c.debugLogger.Printf("[Device] "+format, args...)
	}
}

// GetDeviceID 获取设备ID
func (c *zectrixClient) GetDeviceID() string {
	return c.deviceID
}

// GetDevices 获取设备列表
func (c *zectrixClient) GetDevices(ctx context.Context) ([]Device, error) {
	url := fmt.Sprintf("%s/devices", c.apiURL)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if apiResp.Code != 0 {
		return nil, fmt.Errorf("API error: code=%d, msg=%s", apiResp.Code, apiResp.Msg)
	}

	var devices []Device
	if err := json.Unmarshal(apiResp.Data, &devices); err != nil {
		return nil, fmt.Errorf("failed to decode devices: %w", err)
	}

	return devices, nil
}

// GetTodos 获取待办列表
func (c *zectrixClient) GetTodos(ctx context.Context, deviceID string, status *int) ([]DeviceTodo, error) {
	url := fmt.Sprintf("%s/todos", c.apiURL)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)

	q := req.URL.Query()
	if deviceID != "" {
		q.Set("deviceId", deviceID)
	}
	if status != nil {
		q.Set("status", fmt.Sprintf("%d", *status))
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if apiResp.Code != 0 {
		return nil, fmt.Errorf("API error: code=%d, msg=%s", apiResp.Code, apiResp.Msg)
	}

	var todos []DeviceTodo
	if err := json.Unmarshal(apiResp.Data, &todos); err != nil {
		return nil, fmt.Errorf("failed to decode todos: %w", err)
	}

	c.debug("GetTodos returned %d todos", len(todos))
	return todos, nil
}

// CreateTodo 创建新的待办事项
func (c *zectrixClient) CreateTodo(ctx context.Context, todo *DeviceTodo) (*DeviceTodo, error) {
	url := fmt.Sprintf("%s/todos", c.apiURL)

	body, err := json.Marshal(todo)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal todo: %w", err)
	}

	c.debug("CREATE todo: %s", string(body))

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if apiResp.Code != 0 {
		return nil, fmt.Errorf("API error: code=%d, msg=%s", apiResp.Code, apiResp.Msg)
	}

	var created DeviceTodo
	if err := json.Unmarshal(apiResp.Data, &created); err != nil {
		return nil, fmt.Errorf("failed to decode created todo: %w", err)
	}

	c.debug("CREATE todo success: id=%d", created.ID)
	return &created, nil
}

// UpdateTodo 更新待办事项
func (c *zectrixClient) UpdateTodo(ctx context.Context, todoID int, todo *DeviceTodo) (*DeviceTodo, error) {
	url := fmt.Sprintf("%s/todos/%d", c.apiURL, todoID)

	body, err := json.Marshal(todo)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal todo: %w", err)
	}

	c.debug("UPDATE todo: id=%d body=%s", todoID, string(body))

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if apiResp.Code != 0 {
		return nil, fmt.Errorf("API error: code=%d, msg=%s", apiResp.Code, apiResp.Msg)
	}

	var updated DeviceTodo
	if err := json.Unmarshal(apiResp.Data, &updated); err != nil {
		return nil, fmt.Errorf("failed to decode updated todo: %w", err)
	}

	return &updated, nil
}

// CompleteTodo 标记待办事项为已完成
func (c *zectrixClient) CompleteTodo(ctx context.Context, todoID int) error {
	url := fmt.Sprintf("%s/todos/%d/complete", c.apiURL, todoID)

	req, err := http.NewRequestWithContext(ctx, "PUT", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	if apiResp.Code != 0 {
		return fmt.Errorf("API error: code=%d, msg=%s", apiResp.Code, apiResp.Msg)
	}

	return nil
}

// UncompleteTodo 标记待办事项为未完成
func (c *zectrixClient) UncompleteTodo(ctx context.Context, todoID int) error {
	return c.CompleteTodo(ctx, todoID)
}
