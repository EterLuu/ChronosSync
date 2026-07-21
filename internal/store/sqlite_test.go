package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStoreMigratesLegacyMappingsAndAllowsSameUIDAcrossCalendars(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "chronos.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE mappings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			caldav_uid TEXT NOT NULL UNIQUE,
			device_todo_id INTEGER NOT NULL,
			last_sync_time DATETIME NOT NULL,
			caldav_etag TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO mappings (caldav_uid, device_todo_id, last_sync_time, caldav_etag)
		VALUES ('same-uid', 1, '2026-07-21T10:00:00Z', 'etag-1');
	`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	storage, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	defer storage.Close()
	ctx := context.Background()
	mappings, err := storage.GetMappings(ctx)
	if err != nil || len(mappings) != 1 {
		t.Fatalf("legacy mappings: %#v, %v", mappings, err)
	}
	if mappings[0].CalendarPath != "" || mappings[0].DeviceUpdateDate != 0 {
		t.Fatalf("legacy defaults: %#v", mappings[0])
	}

	mappings[0].CalendarPath = "/calendar-a/"
	if err := storage.UpdateMapping(ctx, &mappings[0]); err != nil {
		t.Fatalf("claim legacy mapping: %v", err)
	}
	second := &Mapping{
		CalendarPath: "/calendar-b/", CalDAVUID: "same-uid", DeviceTodoID: 2,
		DeviceUpdateDate: 20, LastSyncTime: time.Now(), CalDAVETag: "etag-2",
	}
	if err := storage.CreateMapping(ctx, second); err != nil {
		t.Fatalf("same UID in another calendar should be valid: %v", err)
	}
	mappings, err = storage.GetMappings(ctx)
	if err != nil || len(mappings) != 2 {
		t.Fatalf("mappings after insert: %#v, %v", mappings, err)
	}
}
