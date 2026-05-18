package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/EterLuu/ChronosSync/internal/caldav"
	"github.com/EterLuu/ChronosSync/internal/config"
	"github.com/EterLuu/ChronosSync/internal/device"
	"github.com/EterLuu/ChronosSync/internal/store"
	"github.com/EterLuu/ChronosSync/internal/sync"
	"github.com/EterLuu/ChronosSync/internal/webhook"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "Path to config file")
	enableWebhook := flag.Bool("webhook", false, "Enable HTTP control endpoints (/sync, /health, /status)")
	flag.Parse()

	var logWriter io.Writer = os.Stdout
	if logFile := os.Getenv("CHRONOS_LOG_FILE"); logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			logWriter = io.MultiWriter(os.Stdout, f)
		}
	}
	logger := log.New(logWriter, "[Chronos] ", log.LstdFlags|log.Lshortfile)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatalf("Failed to load config: %v", err)
	}

	isDebug := cfg.Logging.Level == "debug"
	var debugLogger *log.Logger
	if isDebug {
		debugLogger = log.New(logWriter, "[DEBUG] ", log.LstdFlags|log.Lmicroseconds)
		logger.SetFlags(log.LstdFlags | log.Lshortfile | log.Lmicroseconds)
		logger.Println("DEBUG mode enabled")
	}

	logger.Println("Starting Chronos...")
	logger.Printf("Config loaded from: %s", *configPath)

	dbDir := filepath.Dir(cfg.Database.Path)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		logger.Fatalf("Failed to create database directory: %v", err)
	}

	dbStore, err := store.NewStore(cfg.Database.Path)
	if err != nil {
		logger.Fatalf("Failed to initialize database: %v", err)
	}
	defer dbStore.Close()
	logger.Println("Database initialized")

	deviceClient := device.NewDeviceClient(
		cfg.Device.APIURL,
		cfg.Device.APIKey,
		cfg.Device.DeviceID,
		debugLogger,
	)
	logger.Println("Device client created")

	var calendars []sync.CalendarEntry
	for _, calCfg := range cfg.Radicale.Calendars {
		client, err := caldav.NewCalDAVClient(
			cfg.Radicale.URL,
			cfg.Radicale.Username,
			cfg.Radicale.Password,
			calCfg.Path,
			debugLogger,
		)
		if err != nil {
			logger.Fatalf("Failed to create CalDAV client for %s: %v", calCfg.Path, err)
		}
		calendars = append(calendars, sync.CalendarEntry{
			Client:  client,
			CalType: sync.CalendarType(calCfg.Type),
			Path:    calCfg.Path,
		})
		logger.Printf("CalDAV client created: %s (type=%s)", calCfg.Path, calCfg.Type)
	}

	syncEngine := sync.NewSyncEngine(
		calendars,
		deviceClient,
		dbStore,
		sync.ConflictResolution(cfg.Sync.ConflictResolution),
		logger,
	)
	logger.Println("Sync engine created")

	scheduler := webhook.NewScheduler(syncEngine, logger, cfg.GetSyncInterval())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if *enableWebhook {
		go func() {
			if err := scheduler.StartWithHTTP(ctx, cfg.Webhook.Addr); err != nil {
				logger.Printf("HTTP server error: %v", err)
			}
		}()
		logger.Printf("HTTP control enabled on %s", cfg.Webhook.Addr)
	} else {
		scheduler.Start(ctx)
	}

	logger.Printf("Sync interval: %s", cfg.GetSyncInterval())
	logger.Printf("Conflict resolution: %s", cfg.Sync.ConflictResolution)

	sig := <-sigChan
	logger.Printf("Received signal: %v", sig)
	logger.Println("Shutting down...")

	if err := scheduler.Stop(ctx); err != nil {
		logger.Printf("Stop error: %v", err)
	}

	logger.Println("Chronos stopped")
}
