package device

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDeleteTodoUsesDeleteEndpoint(t *testing.T) {
	client := NewDeviceClient("https://device.example/open/v1", "test-key", "device-1", nil).(*zectrixClient)
	client.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete {
			t.Errorf("method=%s, want DELETE", r.Method)
		}
		if r.URL.Path != "/open/v1/todos/42" {
			t.Errorf("path=%s, want /open/v1/todos/42", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("missing API key")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"success"}`)),
			Request:    r,
		}, nil
	})

	if err := client.DeleteTodo(context.Background(), 42); err != nil {
		t.Fatalf("DeleteTodo: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
