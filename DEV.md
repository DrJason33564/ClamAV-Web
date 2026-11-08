# ClamAV-Web 开发指南

本文面向维护者和贡献者，说明项目边界、前后端协作方式、运行链路与常用开发操作。用户部署说明请参阅 [README.md](README.md)，完整接口契约请参阅 [doc/api.md](doc/api.md)。

## 项目概览

ClamAV-Web 是一个单镜像应用：上游 `clamav/clamav:stable_base-debian` 提供扫描引擎与病毒库更新，项目在其上增加 shell 编排、Go Web API、React 管理界面和 Go 定时调度。

| 层 | 技术与职责 |
| --- | --- |
| 容器与编排 | Docker 多阶段构建；`startup.sh` 监管 ClamAV 和 Go 服务。 |
| 扫描执行 | Go 遍历目录，通过 Unix socket / FILDES 扫描，统一生成任务结果、日志和隔离记录。 |
| 后端 | Go 1.25、标准库 `net/http`、SQLite（`modernc.org/sqlite`）、robfig/cron/v3、Argon2id。 |
| 前端 | React 19、TypeScript、Vite 8、Tailwind CSS 4、Base UI / shadcn 风格组件。 |
| 持久化 | 文件系统中的配置、状态、日志与隔离文件；SQLite 保存用户、session 与历史索引。 |

## 架构与数据流

```mermaid
flowchart LR
  B[浏览器] -->|Cookie + JSON /api| W[Go Web Server]
  B -->|静态资源| W
  W --> U[(users.db\nsessions)]
  W --> H[(history.db)]
  W --> C[/config\ncron / allow-list / app config/]
  W --> Q[内存扫描队列]
  R[robfig/cron] --> Q
  Q --> S[Go ScanExecutor]
  S --> D[Unix socket / FILDES → clamd]
  S --> T[HistoryStore → /state/jobs]
  T --> H
  S --> L[Go 日志管理器 → /log]
  S --> Z[Go 文件处置 → /quarantine]
```

关键约束：

- 手动和定时扫描经同一个 Go 内存队列串行执行；引擎休眠、扫描及白名单更新使用进程内同步。重启后等待中的内存任务不会恢复。
- 前后端只允许访问 `/scan`。浏览 API 隐藏符号链接，所有接收文件路径的业务 API 都拒绝包含符号链接的路径；`IS_TIMEDOCK=Y` 时还会按登录用户的 `timedock_account` 追加目录限制。
- 业务资源按不可变的 `users.id` 隔离，用户名只用于登录、显示和日志。admin 不是“全局数据查看者”，只额外拥有用户管理、服务配置和 ClamAV 全局控制权限。
- 正在扫描时，白名单变更、部分清理与账户删除会受内存状态约束，避免破坏运行中的任务。

## 目录导览

```text
.
├── Dockerfile                 # 前端构建 → Go 编译 → ClamAV 运行镜像
├── startup.sh                 # PID 1：初始化、启动、监控与优雅关闭
├── exclude.sh                 # 从 exclude.conf 生成 ClamAV 排除项
├── config/                    # 镜像内置的默认配置示例
├── doc/                       # 接口、配置与持久化格式文档
│   ├── api.md                 # HTTP API 契约
│   ├── config.md              # 配置文件格式与运行参数
│   ├── job_file_schema.md     # 任务文件 v3/v4 与版本跨度
│   └── history_db_schema.md   # 历史数据库 schema v3
├── server/                    # Go API、认证、SQLite、队列和业务逻辑
│   ├── main.go                # 服务装配与路由
│   ├── auth.go / users.go     # 账户、session、登录限制与 admin 管理
│   ├── scans.go / cron*.go    # 统一队列、定时规则与 robfig/cron 调度
│   ├── scan_executor.go       # 遍历、文件锁、扫描与格式化事件
│   ├── clamd_client.go        # socket 协议与文件描述符传递
│   ├── history_store.go       # 任务结果生成、增量索引及全量重建
│   ├── scan_actions.go        # 隔离、失败清理与回滚
│   ├── whitelist.go           # SHA-256 allow-list
│   ├── history_*.go           # 历史索引、查询与统计
│   ├── quarantine.go          # 隔离查询、恢复和清理
│   └── internal/applog/       # Go 日志等级、脱敏、并发写入与轮转
└── ClamAV-Web/                # React 前端
    └── src/features/          # 按页面拆分的业务功能
```

旧版前端源码已从 `frontend/` 与 `server/web/` 清理；当前生产前端唯一来源是 **`ClamAV-Web/`**。Dockerfile 只会构建该目录，并将 `dist/` 复制到 Go 的 `web-dist` 嵌入路径。

## 启动链路

1. Docker 前端阶段在 `ClamAV-Web/` 执行 `npm ci && npm run build`。
2. Go 阶段复制 `server/*.go`、`server/internal/` 和前端 `dist/`，以静态链接方式编译 `/server`。
3. 运行阶段的 `startup.sh` 创建持久化目录和缺失配置，生成排除规则。
4. `startup.sh` 启动上游 `/init`，等待 `clamd` 可 ping。
5. 就绪后启动 Go 服务，由其校验规则并启动内存调度器；Go 服务退出时容器结束。

`/startup.sh --wake` 是管理员唤醒 ClamAV 时使用的专用路径：它只重启/确认 ClamAV 并清除休眠锁，不会重复启动 Web 服务或调度器。

## 后端设计

### 路由、认证与授权

路由在 [server/main.go](server/main.go) 注册，并统一经过 `requireAuth`：

- 匿名接口仅有首次运行状态、首次管理员注册和登录。
- 登录成功后下发 `clamavweb_session` Cookie；Cookie 为 `HttpOnly`、`SameSite=Strict`，绝对有效期 24 小时、空闲有效期 2 小时。
- 非安全方法会校验 `Origin` 与请求 Host 一致，以降低跨站请求风险。
- `/api/admin/*` 和 ClamAV 休眠/唤醒额外要求 admin。

密码使用 Argon2id 和每个密码独立 salt，当前参数每次运算约占用 19 MiB 工作内存。所有由
HTTP 请求触发的 Argon2id 哈希与校验共享 5 个并发槽位，满载时立即返回 `429`，以将这部分
峰值工作内存约束在 95 MiB。登录限流以客户端 IP 和全局十分钟窗口计数；可信反向代理的
取 IP 规则不可自行假设，必须遵守 [doc/config.md](doc/config.md) 中的
`SERVER_TRUSTED_REVERSEPROXY` 定义。

HTTP 监听支持通过 `SCANNER_ADDR` 指定多个 IP，所有 listener 共享同一个 HTTP Server 和
业务实例，使用统一的认证与中间件。启动时先完成全部绑定，失败则回收已有监听；运行异常
与退出信号均进入统一关闭流程，等待最多 5 秒，超时则强制关闭连接。错误同时写入 Go 日志
和容器标准输出。地址格式与限制见 [服务端配置](doc/config.md)。

HTTP Server 使用固定的连接级安全边界：请求头读取最多 10 秒、完整请求读取最多 30 秒、
keep-alive 空闲最多 60 秒，请求头最多 64 KiB。响应写入上限为 25 分钟，特意高于 ClamAV
唤醒流程的 21 分钟上限。这些值不是运行时配置项；直接暴露服务时由 Go 后端兜底，使用反向
代理时应在代理侧设置相当或更严格的限制。

Go 服务统一为静态资源、API 和错误响应设置 MIME 嗅探、点击劫持、CSP、Referrer Policy
和 Permissions Policy 防护。CSP 仅允许同源脚本和连接；React 动态样式需要
`style-src 'unsafe-inline'`。HSTS 不由后端发送，应由确认始终使用 HTTPS 的反向代理配置。

### 状态与持久化

完整字段和版本边界分别见 [任务文件格式](doc/job_file_schema.md)、[历史数据库结构](doc/history_db_schema.md) 和 [配置文件说明](doc/config.md)。

| 位置 | 所有者 | 内容 |
| --- | --- | --- |
| `/data/users.db` | Go | users、密码哈希、sessions。 |
| `/data/history.db` | Go | 由任务 JSON 增量更新、并可周期重建的历史索引。 |
| `/state/jobs/*.json` | Go HistoryStore | 每次扫描的 version 4 任务结果，兼容读取 version 3。 |
| `/config/cron_scan.conf` | Go | 带元数据的定时扫描规则。 |
| `/config/exclude.conf` | Go + shell | 信任区相关的文件哈希与规则来源。 |
| `/config/clamavweb.conf` | Go | 服务配置；日志三项只在启动时读取，其他管理项支持 API 更新。 |
| `/log/clamavweb.log*` | Go | 结构化后端日志及按大小轮转的历史文件。 |
| `/log` 扫描与检出日志、`/quarantine` | Go | 扫描记录、检出记录与隔离文件；启动和白名单脚本仍使用 log.sh。 |

配置和隔离文件仍使用必要的安全发布与回滚。运行状态由 Go 内存管理，不依赖 status.json、scan.lock 或 sleep.lock；startup.sh 仅清理旧休眠标记以兼容已有数据目录。

### 扫描与结果

ScanExecutor 通过 FILDES 逐文件扫描，目标目录按层遍历。LOCK_FILE 默认关闭；开启后，按最多 128 个直属文件一批尝试非阻塞独占 flock，文件提交后显式解锁。锁失败只记录 warn，不改变扫描结果。它是协作式锁，不阻止忽略锁的外部进程修改文件。

任务汇总顺序为 found > error > clean。单文件错误会记录后继续扫描其他文件；任何有效检出都保留为 found，包括隔离/删除失败及同时有扫描错误的情况。扫描错误数、处置失败数和结构化检出保存在任务 JSON 中。断连、超时、空响应、截断记录、daemon ERROR 和非法响应均记录明确错误日志，不能被当作 clean。

### 历史索引同步

HistoryStore 独占任务结果文件生成与索引写入；文件读写、清理和索引共享进程内同步。完成文件写入后主动刷新单任务索引，不使用 fsnotify。version 4 任务文件内含结构化检出，重建不依赖日志保留周期；version 3 文件继续支持旧检出日志解析。

HISTORY_INDEX_REFRESH_INTERVAL 控制周期完整目录检查，只重读 mtime 变化的文件。管理 API 更改间隔后通过 channel 重置 timer。管理员 POST /api/results/index 会忽略 mtime 强制解析所有任务，在一个事务中重建全部用户索引；损坏文件保留上次有效索引并记录 warn，数据库失败或取消则回滚。启动时将遗留 waiting/running 任务标记为中断失败，不恢复扫描。

### Go 定时调度与休眠

使用 github.com/robfig/cron/v3 的五段解析器，采用容器 TZ / time.Local，兼容星期日 0/7、数字范围、步长和列表。日期与星期均受限时按 OR 匹配；不存在的夏令时分钟跳过，重复的实际分钟分别触发。停机期间不补跑。规则保存或 reload 后替换调度器快照，过期回调会重新校验规则；同一规则按实际分钟去重。

所有触发进入统一队列，保持用户内 FIFO、用户间随机选择，不跳过重叠触发。定时任务 wake=true 时仍调用 startup.sh --wake；wake=false 且引擎休眠时生成失败历史。SCAN_WAIT_MAX_SECONDS 限制定时任务排队等待，0 为无限；旧 SCAN_WAIT_INTERVAL 不再使用。

自动休眠 timer 的 generation 机制保留；手动和定时任务开始、完成、唤醒及配置更新都会请求重置。有等待或活动任务时拒绝休眠。白名单仍由 exclude.sh 生成，通过统一 socket 客户端请求 clamd 重载。

### 后端日志

`server/internal/applog` 基于标准库 `log/slog`，是 Go 后端和扫描记录的统一文件日志入口。任务扫描与检出记录通过 RecordLog 保留时间戳与可读格式，独立于后端 LOG_LEVEL，复用轮转及并发写入。业务模块只提交等级、消息和结构化
字段；日志包统一负责过滤、单行转义、敏感字段兜底识别和轮转。明确的敏感值必须通过
`applog.Secret(key, value)` 标记，业务代码不得自行拼接掩码。日志包仍会按字段名二次检查，
防止普通键值调用意外泄漏密码、token、Cookie、Authorization 或 secret。

日志配置来自 `clamavweb.conf`，只在进程启动时生效。`LOG_FILE_NUM` 包含当前文件；轮转由
日志包互斥执行，因此 HTTP handler、扫描 worker 和历史 watcher 可以并发记录。轮转或写入
失败时直接回退到 stderr，不能再次调用日志包形成递归。HTTP 日志中的 `peer_ip` 始终来自
`RemoteAddr`；`client_ip` 则使用与登录限流相同的可信代理规则，并额外记录来源类型。

### 异步查询模式

历史记录、统计和隔离区查询采用“创建 lookup → 轮询结果”的模式，避免大目录或大量任务阻塞 HTTP 请求。三类查询共享每用户两个 pending 配额和 30 秒执行超时；每个用户最多保留 10 个 lookup。服务级 context 负责退出取消，统一 janitor 每分钟清理，创建请求也会执行相同的过期清理。terminal 结果保留 15 分钟，账户删除会取消并移除该用户的 lookup。

结果列表的数字分页范围单次最多 500 条；统计范围仍为 1–31 天。隔离区不使用 `scope`，通过分批目录读取响应取消，但仍返回当前用户的完整条目列表。lookup 存在内存中，重启即失效；前端调用时应接受 `202` 并轮询对应资源，具体请求形状见 API 文档。

## 前端设计

入口在 `ClamAV-Web/src/main.tsx`，应用壳在 `src/App.tsx`。前端使用多 HTML 入口：

- `/`：已登录后的主控制台；
- `/login/`：登录；
- `/first_run/`：首次管理员注册与初始化。

页面功能在 `src/features/` 中按域拆分；可复用布局与组件位于 `src/components/`，HTTP 封装在 `src/lib/api.ts`，类型定义在 `src/lib/types.ts`。页面切换使用 URL hash，而不是前端路由库。

`api()` 始终以 `credentials: "same-origin"` 请求；后端返回 `401` 时会分发 `clamav-auth-expired` 事件并回到登录页。新增需要写入的接口时，优先复用 `jsonRequest()` 与 `ApiError`，保持错误呈现和 session 过期行为一致。

## 本地开发

### 前置条件

- Docker（建议用于完整扫描链路）；
- Go **1.25+**；
- Node.js 与 npm（锁定依赖使用 npm）；
- 运行后端时可用的 ClamAV、保留脚本及对应目录结构。

### 前端

```sh
cd ClamAV-Web
npm ci
npm run dev
```

Vite 固定监听 `http://127.0.0.1:5174`，将 `/api` 代理到 `http://127.0.0.1:8080`。后端地址不同时：

```sh
cd ClamAV-Web
VITE_API_PROXY_TARGET=http://127.0.0.1:9000 npm run dev
```

常用检查：

```sh
cd ClamAV-Web
npm run typecheck
npm run lint
npm run build
```

> `npm run format` 会改写文件；请只在明确需要格式化时运行。

### 后端与完整环境

后端依赖容器内的默认路径（如 `/state`、`/config`、`/tmp/clamd.sock` 和 `clamd`），因此最可靠的端到端环境是完整 Docker 镜像：

```sh
docker build -t clamav-web:dev .
docker run --rm -p 8080:8080 \
  -e ADMIN_REGISTER_TOKEN='development-only-token' \
  -v "$(pwd)/config:/config" \
  -v "$(pwd)/data:/data" \
  -v "$(pwd)/scan:/scan" \
  -v "$(pwd)/quarantine:/quarantine" \
  -v "$(pwd)/log:/log" \
  -v "$(pwd)/state:/state" \
  clamav-web:dev
```

单独运行 Go 服务适合纯 HTTP 或单元测试，但需要显式提供脚本、目录和 ClamAV 依赖；不要把它误当作可独立完成真实扫描的开发模式。

## 测试与验证

```sh
cd server
go test ./...
```

修改前端后至少执行 `npm run typecheck` 和 `npm run build`；修改镜像、shell 或嵌入产物后至少执行一次 `docker build`。涉及扫描、隔离、恢复或权限的变更，建议在隔离测试目录用 EICAR 测试文件验证，切勿在重要数据上直接验证 `move` 或 `remove`。

测试文件主要覆盖配置解析、数据库拆分、首次运行、登录限流、多用户隔离、扫描队列、cron 唤醒、休眠/唤醒和历史统计。API 集成测试可通过 `SCANNER_TEST_SERVER` 指向已运行的测试服务，详见 `server/api_test_server.md`。

## 开发约定与常见陷阱

- 保持 API 的用户隔离：新资源必须绑定当前 `actor.ID`，用户名只用于显示和日志；跨用户访问最好返回 `404`，不要泄露资源存在性。
- 路径输入必须使用现有安全路径校验，不能自行拼接 `/scan` 字符串。
- 扫描和白名单操作必须遵守统一队列与引擎互斥；不要引入绕过串行调度的直接扫描路径。
- cron 规则需要兼容带空格的目标路径、owner 与 `wake` 标志。由 Go 校验并热更新调度，不生成 `/etc/cron.d`。
- SQLite 结构或数据迁移应保持幂等，并兼顾已存在的持久化数据库。
- 生产资源由 Go `embed` 提供。只修改 React 源码而不构建前端，最终镜像不会得到更新。
- Dockerfile 会复制 `server/*.go` 和 `server/internal/`；新增其他后端子包或非 Go 运行资源时，应同步审查 COPY 规则。

## 文档索引

| 文档 | 内容 |
| --- | --- |
| [README.md](README.md) | 产品介绍、部署与使用说明。 |
| [doc/api.md](doc/api.md) | 全部 HTTP API、状态码与请求/响应示例。 |
| [doc/config.md](doc/config.md) | 全部运行配置、关联文件格式和环境变量。 |
| [doc/job_file_schema.md](doc/job_file_schema.md) | 任务文件 v3/v4、字段含义及生效跨度。 |
| [doc/history_db_schema.md](doc/history_db_schema.md) | 历史数据库 schema v3 的表、约束与索引实现。 |
| [server/api_test_server.md](server/api_test_server.md) | 面向运行中服务的 API 测试说明。 |
