package webhook

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/EterLuu/ChronosSync/internal/sync"
)

// Scheduler 定时同步调度器
type Scheduler struct {
	engine   *sync.SyncEngine
	logger   *log.Logger
	interval time.Duration

	httpServer *http.Server
}

// NewScheduler 创建调度器
func NewScheduler(engine *sync.SyncEngine, logger *log.Logger, interval time.Duration) *Scheduler {
	return &Scheduler{
		engine:   engine,
		logger:   logger,
		interval: interval,
	}
}

// Start 仅启动定时同步（无 HTTP 服务）
func (s *Scheduler) Start(ctx context.Context) {
	s.logger.Printf("Starting periodic sync (interval=%s)", s.interval)
	go s.runPeriodicSync(ctx)
}

// StartWithHTTP 启动定时同步 + HTTP 控制接口
func (s *Scheduler) StartWithHTTP(ctx context.Context, addr string) error {
	s.Start(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/sync", s.handleSync)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)

	s.httpServer = &http.Server{Addr: addr, Handler: mux}

	s.logger.Printf("Webhook HTTP server on %s", addr)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Stop 停止调度器
func (s *Scheduler) Stop(ctx context.Context) error {
	if s.httpServer != nil {
		s.logger.Println("Stopping HTTP server...")
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

func (s *Scheduler) runPeriodicSync(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.executeSync(ctx)

	for {
		select {
		case <-ctx.Done():
			s.logger.Println("Periodic sync stopped")
			return
		case <-ticker.C:
			s.executeSync(ctx)
		}
	}
}

func (s *Scheduler) executeSync(ctx context.Context) {
	s.logger.Println("Executing periodic sync...")

	result, err := s.engine.Sync(ctx)
	if err != nil {
		s.logger.Printf("Sync failed: %v", err)
		return
	}

	s.logger.Printf("Sync completed: created=%d, updated=%d, deleted=%d, events_created=%d, errors=%d",
		result.Created, result.Updated, result.Deleted, result.EventsCreated, len(result.Errors))

	for _, err := range result.Errors {
		s.logger.Printf("Sync error: %v", err)
	}
}

func (s *Scheduler) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.logger.Println("Manual sync triggered")
	result, err := s.engine.Sync(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"created":%d,"updated":%d,"deleted":%d,"events_created":%d,"errors":%d}`,
		result.Created, result.Updated, result.Deleted, result.EventsCreated, len(result.Errors))
}

func (s *Scheduler) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
}

func (s *Scheduler) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"running","interval":"%s"}`, s.interval)
}
