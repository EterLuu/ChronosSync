package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Mapping 同步映射
type Mapping struct {
	ID           int64     `json:"id"`
	CalDAVUID    string    `json:"caldav_uid"`
	DeviceTodoID int       `json:"device_todo_id"`
	LastSyncTime time.Time `json:"last_sync_time"`
	CalDAVETag   string    `json:"caldav_etag"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Conflict 冲突记录
type Conflict struct {
	ID             int64      `json:"id"`
	CalDAVUID      string     `json:"caldav_uid"`
	DeviceTodoID   int        `json:"device_todo_id"`
	DetectedTime   time.Time  `json:"detected_time"`
	ResolvedTime   *time.Time `json:"resolved_time"`
	Status         string     `json:"status"`
	ResolutionType string     `json:"resolution_type"`
	Notes          string     `json:"notes"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// SyncLog 同步日志
type SyncLog struct {
	ID           int64     `json:"id"`
	SyncTime     time.Time `json:"sync_time"`
	Direction    string    `json:"direction"`
	Action       string    `json:"action"`
	CalDAVUID    string    `json:"caldav_uid"`
	DeviceTodoID int       `json:"device_todo_id"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message"`
	CreatedAt    time.Time `json:"created_at"`
}

// Store 数据存储接口
type Store interface {
	// 映射操作
	CreateMapping(ctx context.Context, mapping *Mapping) error
	GetMapping(ctx context.Context, caldavUID string) (*Mapping, error)
	GetMappingByDeviceID(ctx context.Context, deviceTodoID int) (*Mapping, error)
	GetMappings(ctx context.Context) ([]Mapping, error)
	UpdateMapping(ctx context.Context, mapping *Mapping) error
	DeleteMapping(ctx context.Context, caldavUID string) error

	// 冲突操作
	CreateConflict(ctx context.Context, conflict *Conflict) error
	GetConflict(ctx context.Context, id int64) (*Conflict, error)
	GetPendingConflicts(ctx context.Context) ([]Conflict, error)
	UpdateConflict(ctx context.Context, conflict *Conflict) error

	// 同步日志操作
	CreateSyncLog(ctx context.Context, log *SyncLog) error
	GetSyncLogs(ctx context.Context, limit int) ([]SyncLog, error)

	// 关闭数据库
	Close() error
}

// sqliteStore SQLite 存储实现
type sqliteStore struct {
	db *sql.DB
}

// NewStore 创建新的存储
func NewStore(dbPath string) (Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// 测试连接
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// 启用外键约束
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	store := &sqliteStore{db: db}

	// 初始化数据库表
	if err := store.initDB(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	return store, nil
}

// initDB 初始化数据库表
func (s *sqliteStore) initDB() error {
	schema := `
	CREATE TABLE IF NOT EXISTS mappings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		caldav_uid TEXT NOT NULL UNIQUE,
		device_todo_id INTEGER NOT NULL,
		last_sync_time DATETIME NOT NULL,
		caldav_etag TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS conflicts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		caldav_uid TEXT NOT NULL,
		device_todo_id INTEGER NOT NULL,
		detected_time DATETIME NOT NULL,
		resolved_time DATETIME,
		status TEXT NOT NULL DEFAULT 'pending',
		resolution_type TEXT,
		notes TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sync_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		sync_time DATETIME NOT NULL,
		direction TEXT NOT NULL,
		action TEXT NOT NULL,
		caldav_uid TEXT,
		device_todo_id INTEGER,
		status TEXT NOT NULL,
		error_message TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_mappings_caldav_uid ON mappings(caldav_uid);
	CREATE INDEX IF NOT EXISTS idx_mappings_device_todo_id ON mappings(device_todo_id);
	CREATE INDEX IF NOT EXISTS idx_conflicts_status ON conflicts(status);
	CREATE INDEX IF NOT EXISTS idx_sync_logs_sync_time ON sync_logs(sync_time);
	`

	_, err := s.db.Exec(schema)
	return err
}

// CreateMapping 创建映射
func (s *sqliteStore) CreateMapping(ctx context.Context, mapping *Mapping) error {
	query := `
		INSERT INTO mappings (caldav_uid, device_todo_id, last_sync_time, caldav_etag)
		VALUES (?, ?, ?, ?)
	`

	now := time.Now()
	mapping.CreatedAt = now
	mapping.UpdatedAt = now

	result, err := s.db.ExecContext(ctx, query,
		mapping.CalDAVUID,
		mapping.DeviceTodoID,
		mapping.LastSyncTime,
		mapping.CalDAVETag,
	)
	if err != nil {
		return fmt.Errorf("failed to create mapping: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	mapping.ID = id
	return nil
}

// GetMapping 获取映射
func (s *sqliteStore) GetMapping(ctx context.Context, caldavUID string) (*Mapping, error) {
	query := `
		SELECT id, caldav_uid, device_todo_id, last_sync_time, caldav_etag, created_at, updated_at
		FROM mappings
		WHERE caldav_uid = ?
	`

	mapping := &Mapping{}
	err := s.db.QueryRowContext(ctx, query, caldavUID).Scan(
		&mapping.ID,
		&mapping.CalDAVUID,
		&mapping.DeviceTodoID,
		&mapping.LastSyncTime,
		&mapping.CalDAVETag,
		&mapping.CreatedAt,
		&mapping.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get mapping: %w", err)
	}

	return mapping, nil
}

// GetMappingByDeviceID 根据设备ID获取映射
func (s *sqliteStore) GetMappingByDeviceID(ctx context.Context, deviceTodoID int) (*Mapping, error) {
	query := `
		SELECT id, caldav_uid, device_todo_id, last_sync_time, caldav_etag, created_at, updated_at
		FROM mappings
		WHERE device_todo_id = ?
	`

	mapping := &Mapping{}
	err := s.db.QueryRowContext(ctx, query, deviceTodoID).Scan(
		&mapping.ID,
		&mapping.CalDAVUID,
		&mapping.DeviceTodoID,
		&mapping.LastSyncTime,
		&mapping.CalDAVETag,
		&mapping.CreatedAt,
		&mapping.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get mapping by device id: %w", err)
	}

	return mapping, nil
}

// GetMappings 获取所有映射
func (s *sqliteStore) GetMappings(ctx context.Context) ([]Mapping, error) {
	query := `
		SELECT id, caldav_uid, device_todo_id, last_sync_time, caldav_etag, created_at, updated_at
		FROM mappings
		ORDER BY id
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get mappings: %w", err)
	}
	defer rows.Close()

	var mappings []Mapping
	for rows.Next() {
		var mapping Mapping
		if err := rows.Scan(
			&mapping.ID,
			&mapping.CalDAVUID,
			&mapping.DeviceTodoID,
			&mapping.LastSyncTime,
			&mapping.CalDAVETag,
			&mapping.CreatedAt,
			&mapping.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan mapping: %w", err)
		}
		mappings = append(mappings, mapping)
	}

	return mappings, nil
}

// UpdateMapping 更新映射
func (s *sqliteStore) UpdateMapping(ctx context.Context, mapping *Mapping) error {
	query := `
		UPDATE mappings
		SET device_todo_id = ?, last_sync_time = ?, caldav_etag = ?, updated_at = ?
		WHERE caldav_uid = ?
	`

	mapping.UpdatedAt = time.Now()

	_, err := s.db.ExecContext(ctx, query,
		mapping.DeviceTodoID,
		mapping.LastSyncTime,
		mapping.CalDAVETag,
		mapping.UpdatedAt,
		mapping.CalDAVUID,
	)
	if err != nil {
		return fmt.Errorf("failed to update mapping: %w", err)
	}

	return nil
}

// DeleteMapping 删除映射
func (s *sqliteStore) DeleteMapping(ctx context.Context, caldavUID string) error {
	query := `DELETE FROM mappings WHERE caldav_uid = ?`

	_, err := s.db.ExecContext(ctx, query, caldavUID)
	if err != nil {
		return fmt.Errorf("failed to delete mapping: %w", err)
	}

	return nil
}

// CreateConflict 创建冲突记录
func (s *sqliteStore) CreateConflict(ctx context.Context, conflict *Conflict) error {
	query := `
		INSERT INTO conflicts (caldav_uid, device_todo_id, detected_time, status, resolution_type, notes)
		VALUES (?, ?, ?, ?, ?, ?)
	`

	now := time.Now()
	conflict.CreatedAt = now
	conflict.UpdatedAt = now

	result, err := s.db.ExecContext(ctx, query,
		conflict.CalDAVUID,
		conflict.DeviceTodoID,
		conflict.DetectedTime,
		conflict.Status,
		conflict.ResolutionType,
		conflict.Notes,
	)
	if err != nil {
		return fmt.Errorf("failed to create conflict: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	conflict.ID = id
	return nil
}

// GetConflict 获取冲突记录
func (s *sqliteStore) GetConflict(ctx context.Context, id int64) (*Conflict, error) {
	query := `
		SELECT id, caldav_uid, device_todo_id, detected_time, resolved_time, status, resolution_type, notes, created_at, updated_at
		FROM conflicts
		WHERE id = ?
	`

	conflict := &Conflict{}
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&conflict.ID,
		&conflict.CalDAVUID,
		&conflict.DeviceTodoID,
		&conflict.DetectedTime,
		&conflict.ResolvedTime,
		&conflict.Status,
		&conflict.ResolutionType,
		&conflict.Notes,
		&conflict.CreatedAt,
		&conflict.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get conflict: %w", err)
	}

	return conflict, nil
}

// GetPendingConflicts 获取待处理的冲突
func (s *sqliteStore) GetPendingConflicts(ctx context.Context) ([]Conflict, error) {
	query := `
		SELECT id, caldav_uid, device_todo_id, detected_time, resolved_time, status, resolution_type, notes, created_at, updated_at
		FROM conflicts
		WHERE status = 'pending'
		ORDER BY detected_time DESC
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending conflicts: %w", err)
	}
	defer rows.Close()

	var conflicts []Conflict
	for rows.Next() {
		var conflict Conflict
		if err := rows.Scan(
			&conflict.ID,
			&conflict.CalDAVUID,
			&conflict.DeviceTodoID,
			&conflict.DetectedTime,
			&conflict.ResolvedTime,
			&conflict.Status,
			&conflict.ResolutionType,
			&conflict.Notes,
			&conflict.CreatedAt,
			&conflict.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan conflict: %w", err)
		}
		conflicts = append(conflicts, conflict)
	}

	return conflicts, nil
}

// UpdateConflict 更新冲突记录
func (s *sqliteStore) UpdateConflict(ctx context.Context, conflict *Conflict) error {
	query := `
		UPDATE conflicts
		SET resolved_time = ?, status = ?, resolution_type = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`

	conflict.UpdatedAt = time.Now()

	_, err := s.db.ExecContext(ctx, query,
		conflict.ResolvedTime,
		conflict.Status,
		conflict.ResolutionType,
		conflict.Notes,
		conflict.UpdatedAt,
		conflict.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update conflict: %w", err)
	}

	return nil
}

// CreateSyncLog 创建同步日志
func (s *sqliteStore) CreateSyncLog(ctx context.Context, log *SyncLog) error {
	query := `
		INSERT INTO sync_logs (sync_time, direction, action, caldav_uid, device_todo_id, status, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	log.CreatedAt = time.Now()

	result, err := s.db.ExecContext(ctx, query,
		log.SyncTime,
		log.Direction,
		log.Action,
		log.CalDAVUID,
		log.DeviceTodoID,
		log.Status,
		log.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("failed to create sync log: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	log.ID = id
	return nil
}

// GetSyncLogs 获取同步日志
func (s *sqliteStore) GetSyncLogs(ctx context.Context, limit int) ([]SyncLog, error) {
	query := `
		SELECT id, sync_time, direction, action, caldav_uid, device_todo_id, status, error_message, created_at
		FROM sync_logs
		ORDER BY sync_time DESC
		LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get sync logs: %w", err)
	}
	defer rows.Close()

	var logs []SyncLog
	for rows.Next() {
		var log SyncLog
		if err := rows.Scan(
			&log.ID,
			&log.SyncTime,
			&log.Direction,
			&log.Action,
			&log.CalDAVUID,
			&log.DeviceTodoID,
			&log.Status,
			&log.ErrorMessage,
			&log.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan sync log: %w", err)
		}
		logs = append(logs, log)
	}

	return logs, nil
}

// Close 关闭数据库
func (s *sqliteStore) Close() error {
	return s.db.Close()
}
