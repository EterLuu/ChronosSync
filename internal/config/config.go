package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 应用配置
type Config struct {
	Radicale RadicaleConfig `yaml:"radicale"`
	Device   DeviceConfig   `yaml:"device"`
	Sync     SyncConfig     `yaml:"sync"`
	Database DatabaseConfig `yaml:"database"`
	Logging  LoggingConfig  `yaml:"logging"`
	Webhook  WebhookConfig  `yaml:"webhook"`
}

// RadicaleConfig Radicale 服务器配置
type RadicaleConfig struct {
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// 单日历（向后兼容）
	CalendarPath string `yaml:"calendar_path"`
	// 多日历列表
	Calendars []CalendarConfig `yaml:"calendars"`
}

// CalendarConfig 单个日历配置
type CalendarConfig struct {
	Path string `yaml:"path"`
	Type string `yaml:"type"` // todos / calendar / calendar_and_todos
}

// DeviceConfig 水墨屏设备配置
type DeviceConfig struct {
	APIURL   string `yaml:"api_url"`
	APIKey   string `yaml:"api_key"`
	DeviceID string `yaml:"device_id"`
}

// SyncConfig 同步配置
type SyncConfig struct {
	Interval           int    `yaml:"interval"`
	ConflictResolution string `yaml:"conflict_resolution"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// LoggingConfig 日志配置
type LoggingConfig struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

// WebhookConfig Webhook 服务器配置
type WebhookConfig struct {
	Addr string `yaml:"addr"`
}

// Load 加载配置文件
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	cfg.applyDefaults()
	cfg.overrideFromEnv()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Sync.Interval == 0 {
		c.Sync.Interval = 300
	}
	if c.Sync.ConflictResolution == "" {
		c.Sync.ConflictResolution = "device_wins"
	}
	if c.Database.Path == "" {
		c.Database.Path = "./data/chronos.db"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Webhook.Addr == "" {
		c.Webhook.Addr = ":8080"
	}
	// 向后兼容：如果 calendars 为空但 calendar_path 有值，自动转换
	if len(c.Radicale.Calendars) == 0 && c.Radicale.CalendarPath != "" {
		c.Radicale.Calendars = []CalendarConfig{
			{Path: c.Radicale.CalendarPath, Type: "calendar_and_todos"},
		}
	}
	// 默认类型
	for i := range c.Radicale.Calendars {
		if c.Radicale.Calendars[i].Type == "" {
			c.Radicale.Calendars[i].Type = "calendar_and_todos"
		}
	}
}

func (c *Config) overrideFromEnv() {
	if v := os.Getenv("CHRONOS_RADICALE_URL"); v != "" {
		c.Radicale.URL = v
	}
	if v := os.Getenv("CHRONOS_RADICALE_USERNAME"); v != "" {
		c.Radicale.Username = v
	}
	if v := os.Getenv("CHRONOS_RADICALE_PASSWORD"); v != "" {
		c.Radicale.Password = v
	}
	if v := os.Getenv("CHRONOS_DEVICE_API_URL"); v != "" {
		c.Device.APIURL = v
	}
	if v := os.Getenv("CHRONOS_DEVICE_API_KEY"); v != "" {
		c.Device.APIKey = v
	}
	if v := os.Getenv("CHRONOS_DEVICE_ID"); v != "" {
		c.Device.DeviceID = v
	}
	if v := os.Getenv("CHRONOS_SYNC_INTERVAL"); v != "" {
		if interval, err := strconv.Atoi(v); err == nil {
			c.Sync.Interval = interval
		}
	}
	if v := os.Getenv("CHRONOS_SYNC_CONFLICT_RESOLUTION"); v != "" {
		c.Sync.ConflictResolution = v
	}
	if v := os.Getenv("CHRONOS_DATABASE_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("CHRONOS_LOGGING_LEVEL"); v != "" {
		c.Logging.Level = v
	}
	if v := os.Getenv("CHRONOS_WEBHOOK_ADDR"); v != "" {
		c.Webhook.Addr = v
	}
}

func (c *Config) validate() error {
	if c.Radicale.URL == "" {
		return fmt.Errorf("radicale.url is required")
	}
	if c.Device.APIURL == "" {
		return fmt.Errorf("device.api_url is required")
	}
	if c.Device.APIKey == "" {
		return fmt.Errorf("device.api_key is required")
	}
	if c.Device.DeviceID == "" {
		return fmt.Errorf("device.device_id is required")
	}
	if c.Sync.Interval < 10 {
		return fmt.Errorf("sync.interval must be at least 10 seconds")
	}
	validResolutions := map[string]bool{"device_wins": true, "radicale_wins": true, "manual": true}
	if !validResolutions[c.Sync.ConflictResolution] {
		return fmt.Errorf("sync.conflict_resolution must be one of: device_wins, radicale_wins, manual")
	}
	validTypes := map[string]bool{"todos": true, "calendar": true, "calendar_and_todos": true}
	for _, cal := range c.Radicale.Calendars {
		if cal.Path == "" {
			return fmt.Errorf("calendar path cannot be empty")
		}
		if !validTypes[cal.Type] {
			return fmt.Errorf("calendar type must be one of: todos, calendar, calendar_and_todos")
		}
	}
	return nil
}

// GetSyncInterval 获取同步间隔
func (c *Config) GetSyncInterval() time.Duration {
	return time.Duration(c.Sync.Interval) * time.Second
}
