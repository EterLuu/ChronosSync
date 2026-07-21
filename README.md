# ChronosSync

Radicale CalDAV 服务器与 Zectrix 水墨屏设备之间的日历/待办双向同步服务。

## 功能

- **待办事项双向同步**：Radicale VTODO ↔ 水墨屏设备待办
- **日历事件单向同步**：Radicale VEVENT 在开始前一天显示，到期后自动从水墨屏删除
- **重复事件支持**：每周/每月 RRULE 按实例展开，改期或取消的实例也会正确处理
- **多日历支持**：每个日历可配置为 `todos`/`calendar`/`calendar_and_todos`
- **完成状态双向传播**：任一端标记完成/撤销，自动同步到另一端（基于变更检测而非跨系统时钟）
- **字段冲突策略**：标题、描述、日期、优先级按 `radicale_wins`/`device_wins`/`manual` 策略解决
- **调试模式**：`logging.level: debug` 输出完整 HTTP 请求/响应
- **HTTP 控制接口**（可选 `--webhook`）：`POST /sync` 手动触发、`GET /health` 健康检查

## 同步逻辑

```
┌──────────┐                          ┌──────────┐
│ Radicale │←── 双向 VTODO 同步 ────→ │水墨屏设备  │
│          │                          │          │
│ VEVENT   │──→ 当前/次日事件 → 待办 ─→ │ (只读)    │
└──────────┘                          └──────────┘
```

- **VTODO**：双向同步。新建、修改、完成/撤销以及删除都会在两端传播。
- **VEVENT**：单向。事件从开始日期的前一天起创建为设备待办；到达 `DTEND`（无结束时间时为 `DTSTART`）后删除，**不回写**到 CalDAV。
- **重复事件**：RRULE/RDATE/EXDATE 和 `RECURRENCE-ID` 例外按实例处理；每个每周/每月实例拥有独立映射，不会覆盖上一实例。
- **冲突判断**：使用 CalDAV ETag 与设备 `updateDate` 判断哪一端发生了变化。单边变化直接向另一端传播，双方同时变化时才使用配置的冲突策略。

## 安装与运行

### 构建

```bash
go build -o chronos ./cmd/chronos
```

> 需要 GCC（CGO）。Windows 用 MSYS2 安装 `mingw-w64-ucrt-x86_64-gcc`。

### 运行

```bash
# 默认：仅后台定时同步，不开放端口
./chronos

# 自定义配置文件
./chronos -config /path/to/config.yaml

# 开启 HTTP 控制接口
./chronos --webhook
```

### 调试模式

将配置文件 `logging.level` 设为 `debug`，所有 HTTP 请求/响应体完整输出到控制台。

## 配置

```yaml
# Radicale CalDAV 服务器
radicale:
  url: "https://radicale.example.com"
  username: "user"
  password: "pass"
  calendars:
    - path: "/user/tasks/"
      type: "calendar_and_todos"   # 待办 + 日历事件
    - path: "/user/events/"
      type: "calendar"             # 仅日历事件

  # 向后兼容：calendar_path 自动转为 calendars
  # calendar_path: "/user/cal/"

# 水墨屏设备 API
device:
  api_url: "https://cloud.zectrix.com/open/v1"
  api_key: "zt_xxx"
  device_id: "AA:BB:CC:DD:EE:FF"

# 同步配置
sync:
  interval: 300                    # 秒
  conflict_resolution: "radicale_wins"  # radicale_wins | device_wins | manual

database:
  path: "./data/chronos.db"

logging:
  level: "info"                    # debug | info
```

### 日历类型

| 类型 | 行为 |
|------|------|
| `todos` | 双向同步 VTODO |
| `calendar` | 单向：VEVENT 实例 → 设备待办，开始前一天出现、到期删除 |
| `calendar_and_todos` | 两者都做 |

### 冲突策略

| 策略 | 完成状态 | 字段（标题/日期/优先级等） |
|------|---------|-------------------------|
| `radicale_wins` | 双向变更检测 | Radicale 覆盖设备 |
| `device_wins` | 双向变更检测 | 设备覆盖 Radicale |
| `manual` | 双向变更检测 | 记录冲突，不自动解决 |

> 完成状态不受策略约束——谁最后改了就以谁为准，双方同时修改时才用策略裁决。

### 环境变量

所有配置项均可通过环境变量覆盖：

- `CHRONOS_RADICALE_URL` `CHRONOS_RADICALE_USERNAME` `CHRONOS_RADICALE_PASSWORD`
- `CHRONOS_DEVICE_API_URL` `CHRONOS_DEVICE_API_KEY` `CHRONOS_DEVICE_ID`
- `CHRONOS_SYNC_INTERVAL` `CHRONOS_SYNC_CONFLICT_RESOLUTION`
- `CHRONOS_DATABASE_PATH` `CHRONOS_LOGGING_LEVEL` `CHRONOS_WEBHOOK_ADDR`
- `CHRONOS_LOG_FILE`

## HTTP 接口（需 `--webhook`）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/sync` | 立即执行一次同步 |
| GET | `/health` | 健康检查 |
| GET | `/status` | 当前状态和同步间隔 |

## 目录结构

```
chronos/
├── cmd/chronos/main.go            # 入口
├── internal/
│   ├── config/config.go           # 配置加载
│   ├── caldav/client.go           # CalDAV 客户端（Radicale）
│   ├── device/client.go           # 设备 API 客户端（Zectrix）
│   ├── sync/engine.go             # 同步引擎
│   ├── store/sqlite.go            # SQLite 存储（映射/冲突/日志）
│   └── webhook/server.go          # 定时调度 + 可选 HTTP
├── config/config.yaml             # 配置文件
├── Dockerfile
├── docker-compose.yml
└── go.mod
```

## Docker 部署

```bash
docker-compose up -d
```

包含 ChronosSync 和 Radicale 服务。
