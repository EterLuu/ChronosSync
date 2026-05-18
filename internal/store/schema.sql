-- 同步映射表
CREATE TABLE IF NOT EXISTS mappings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    caldav_uid TEXT NOT NULL UNIQUE,
    device_todo_id INTEGER NOT NULL,
    last_sync_time DATETIME NOT NULL,
    caldav_etag TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 冲突记录表
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

-- 同步日志表
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

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_mappings_caldav_uid ON mappings(caldav_uid);
CREATE INDEX IF NOT EXISTS idx_mappings_device_todo_id ON mappings(device_todo_id);
CREATE INDEX IF NOT EXISTS idx_conflicts_status ON conflicts(status);
CREATE INDEX IF NOT EXISTS idx_sync_logs_sync_time ON sync_logs(sync_time);
