# Template API Test Server

`api_test_server_test.go` 提供一个仅用于前端联调的模板化 HTTP API 服务。它覆盖
[`api.md`](api.md) 中记录的全部后端接口，但不会调用 ClamAV、扫描脚本或配置脚本，
也不会读写真实的扫描目录、状态目录、日志、白名单、定时规则或隔离区。

> 此服务仅用于开发和测试。不要将它作为真实服务部署。

## 启动

进入 `server` 目录后执行：

```sh
SCANNER_TEST_SERVER=1 SCANNER_TEST_ADDR=:8081 \
  go test -run '^TestTemplateAPIServer$' -count=1 -v
```

环境变量：

| 变量 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `SCANNER_TEST_SERVER` | 是 | 无 | 值非空时启动测试服务；未设置时该测试会跳过。 |
| `SCANNER_TEST_ADDR` | 否 | `:8081` | 测试服务的监听地址。 |

服务启动后会持续运行，按 `Ctrl+C` 停止。

## Basic Auth

所有页面和 API 都要求请求包含 HTTP Basic Auth。测试服务只确认请求使用了 Basic
Auth，不校验用户名和密码，因此任意凭据均可：

```sh
curl -u test:anything http://localhost:8081/api/status
```

未提供 Basic Auth 时返回：

```json
{
  "error": "basic auth is required"
}
```

响应状态码为 `401 Unauthorized`，并包含 `WWW-Authenticate` 响应头。

## 测试数据约定

### 服务状态

每次请求 `GET /api/status` 时：

- `source.updated_at` 为本次请求的处理时间。
- `source.clamd.last_checked_at` 为本次请求的处理时间。
- `source.scan.last_job_id` 恒为 `manual-20260728070000`。
- `checked_at` 为本次请求的处理时间。
- `source.clamd.status` 从 `ready`、`error`、`timeout` 中随机选择。
- `source.clamd.message` 与所选状态匹配。
- `ping` 与 `source.clamd.status` 相同。
- `ping_message` 与 `source.clamd.message` 相同。
- `is_timedock` 固定为 `false`。

状态与消息的对应关系：

| 状态 | 消息 |
| --- | --- |
| `ready` | `clamd is ready` |
| `error` | `clamd test connection failed` |
| `timeout` | `clamd ping timed out` |

同一次响应中的状态、消息和 ping 始终相互匹配。

### ClamAV 休眠与唤醒

`POST /api/clamav/sleep` 和 `POST /api/clamav/wake` 每次请求时随机选择成功或失败
响应。它们只返回示例数据，不会连接 ClamAV、执行启动脚本或创建和删除锁目录。

成功响应与真实后端一致：

| 接口 | 状态码 | `status` | `message` |
| --- | --- | --- | --- |
| sleep | `200` | `sleeping` | `ClamAV entered sleep mode.` 或 `ClamAV is already sleeping.` |
| wake | `200` | `awake` | `ClamAV woke successfully.` 或 `ClamAV is already awake.` |

失败响应使用真实后端的通用 `{"error":"error message"}` 结构，不包含 `status`：

| 接口 | 可能状态码 | 示例情况 |
| --- | --- | --- |
| sleep | `409` | 扫描正在运行，不能休眠。 |
| sleep | `500` | socket 操作或休眠锁创建失败。 |
| sleep | `504` | SHUTDOWN 操作超时。 |
| wake | `500` | 唤醒脚本执行失败。 |
| wake | `504` | 唤醒操作超时。 |

### 文件浏览

`GET /api/browse` 返回从 `/scan` 开始的固定三层目录树。目录数据仅存在于测试服务
内存中，不要求本机实际存在这些路径。

```text
/scan
├── documents
│   ├── approved
│   │   ├── policy.pdf
│   │   └── handbook.docx
│   ├── reports
│   │   ├── report-q1.xlsx
│   │   ├── report-q2.xlsx
│   │   └── summary.pdf
│   ├── contract.pdf
│   └── notes.txt
├── uploads
│   ├── incoming
│   │   ├── upload-001.dat
│   │   └── upload-002.dat
│   ├── archive
│   │   ├── archive-01.tar
│   │   ├── archive-02.tar
│   │   └── manifest.json
│   ├── example-safe.zip
│   ├── photo.jpg
│   └── payload.bin
├── README.txt
├── sample.iso
└── test-data.zip
```

不传 `path` 时默认返回 `/scan`。可以逐层请求，例如：

```sh
curl -u test:anything \
  'http://localhost:8081/api/browse?path=/scan/documents/reports'
```

请求测试树以外的路径时返回 `404 Not Found`。

### 扫描队列

`GET /api/scans` 固定返回四个示例任务：

| 序号 | 状态 | 目标 | 动作 |
| --- | --- | --- | --- |
| `0` | `running` | `/scan/documents` | `warn` |
| `1` | `queued` | `/scan/uploads` | `move` |
| `2` | `queued` | `/scan/documents/reports` | `warn` |
| `3` | `queued` | `/scan/uploads/incoming` | `remove` |

序号 `0` 是运行中任务，其余三个是等待中任务。调整顺序和取消请求仍只返回模板化
成功信息，不会改变这四个固定任务。

### 定时扫描规则

`GET /api/cron/rules` 固定返回两个示例任务：

- `testenabled00001`：已启用，每天 `02:00` 扫描 `/scan/documents`，执行时唤醒 ClamAV。
- `testdisabled0002`：未启用，每周日 `04:30` 扫描 `/scan/uploads`，不主动唤醒 ClamAV。

新增、更新、启用、禁用、删除和 reload 请求均返回包含这两个任务的成功响应，不会
修改返回内容，也不会写入真实配置文件。

### 白名单

白名单固定包含三个条目：

- `/scan/trusted`
- `/scan/documents/approved`
- `/scan/uploads/example-safe.zip`

新增和删除请求均返回成功以及相同的三个条目，不会写入配置文件。

### 历史扫描结果

`POST /api/results/lookups` 每次创建一个新的查询 ID，并以 `202 Accepted` 返回
`status: pending`。测试服务同时为该查询生成 1–5 秒的随机准备时间。

```sh
curl -u test:anything -X POST \
  http://localhost:8081/api/results/lookups
```

使用响应中的 `lookup_id` 轮询 `GET /api/results/lookups/{lookup_id}`：

- 随机准备时间结束前返回 `202 Accepted` 和 `status: pending`。
- 准备时间结束后返回 `200 OK`、`status: success` 以及 20 条示例结果。
- 未创建过或已被清理的查询 ID 返回 `404 Not Found`。

结果类型包含 `manual` 和 `cron`，结果值循环使用 `clean`、`found`、`error` 和
`null`。查询状态保存在测试服务进程的内存中，超过 15 分钟的旧查询会在创建新查询
时清理。

`POST /api/results/detection` 不读取请求体中的任务 ID，始终返回固定测试任务以及：

```json
{
  "original": "Example Log File",
  "log": "Example Log File"
}
```

### 隔离区

`POST /api/quarantine/lookups` 与历史结果查询使用相同的异步流程：每次创建新的查询
ID，先返回 `202 Accepted` 和 `status: pending`，并在 1–5 秒的随机准备时间内保持
pending。准备完成后，轮询 GET 接口会返回 `200 OK`、`status: success` 以及 20 条
示例隔离文件记录。未知查询 ID 返回 `404 Not Found`。

删除、恢复和清空操作均返回成功，但不会操作任何文件。

## API 行为总览

| 方法 | 路径 | 测试服务行为 |
| --- | --- | --- |
| `GET` | `/` | 返回测试服务说明 HTML。 |
| `GET` | `/api/status` | 返回时间动态、状态随机且字段匹配的完整状态。 |
| `POST` | `/api/clamav/sleep` | 随机返回真实结构的休眠成功或失败响应，不操作 ClamAV。 |
| `POST` | `/api/clamav/wake` | 随机返回真实结构的唤醒成功或失败响应，不操作 ClamAV。 |
| `GET` | `/api/browse` | 返回固定三层 `/scan` 目录树。 |
| `POST` | `/api/scans` | 返回固定的已入队批次，不启动扫描。 |
| `GET` | `/api/scans` | 返回一个运行中和三个等待中的示例批次。 |
| `POST` | `/api/scans/reorder` | 返回成功，不改变队列。 |
| `POST` | `/api/scans/cancel` | 返回成功，不取消任务。 |
| `GET` | `/api/cron/rules` | 返回两个固定示例任务。 |
| `POST` | `/api/cron/rules` | 返回成功及固定规则，不新增任务。 |
| `PUT` | `/api/cron/rules/{id}` | 返回成功及固定规则，不更新任务。 |
| `PATCH` | `/api/cron/rules/{id}/enabled` | 返回成功及固定规则，不改变启用状态。 |
| `DELETE` | `/api/cron/rules/{id}` | 返回成功及固定规则，不删除任务。 |
| `POST` | `/api/cron/reload` | 返回成功，不执行脚本。 |
| `GET` | `/api/whitelist` | 返回三个固定白名单条目。 |
| `POST` | `/api/whitelist` | 返回成功及固定条目，不新增白名单。 |
| `DELETE` | `/api/whitelist` | 返回成功及固定条目，不删除白名单。 |
| `POST` | `/api/results/lookups` | 创建查询并返回新的 ID 和 pending 状态。 |
| `GET` | `/api/results/lookups/{lookup_id}` | 1–5 秒内返回 pending，随后返回 20 条示例结果。 |
| `POST` | `/api/results/detection` | 恒定返回 `Example Log File`。 |
| `POST` | `/api/results/clean` | 返回成功和 `deleted: 0`，不删除文件。 |
| `POST` | `/api/quarantine/clean` | 返回成功和 `deleted: 0`，不删除文件。 |
| `POST` | `/api/quarantine/lookups` | 创建查询并返回新的 ID 和 pending 状态。 |
| `GET` | `/api/quarantine/lookups/{lookup_id}` | 1–5 秒内返回 pending，随后返回 20 条示例隔离记录。 |
| `POST` | `/api/quarantine/delete/{filename}` | 返回成功，不删除文件。 |
| `POST` | `/api/quarantine/recover/{filename}` | 返回成功，不恢复文件。 |

除上述接口外，未知路径返回 `404 Not Found`；已知接口使用不支持的 HTTP 方法时返回
`405 Method Not Allowed`。

## 自动化验证

正常运行项目测试时，长期运行的测试服务不会自动启动。`TestTemplateAPIServer` 会在
没有设置 `SCANNER_TEST_SERVER` 时跳过，而 `TestTemplateAPIResponses` 会直接测试
handler，不监听端口。

```sh
go test ./...
```

自动化测试会检查：

- Basic Auth 必须存在，但任意凭据均可通过。
- `/api/status` 的状态、消息和 ping 相互匹配。
- ClamAV 休眠与唤醒接口只返回契约允许的随机成功或失败结构。
- 文件浏览使用固定目录树。
- 扫描队列固定包含一个运行中任务和三个等待中任务。
- 结果与隔离区查询先返回 pending，并在准备完成后返回 20 条记录。
- 每次创建异步查询时都会生成新的查询 ID。
- detection 日志恒为 `Example Log File`。
- `api.md` 中记录的每个 API 路由均有对应响应。

## 安全边界

测试服务的所有模板数据均定义在 `api_test_server_test.go` 中。它不会实例化真实
`server`、不会调用真实 handler，也不会访问以下生产资源：

- ClamAV 或 `clamdscan`
- `/scan`
- `/state` 和任务状态文件
- `/log`
- `/config/cron_scan.conf`
- `/config/exclude.conf`
- `/quarantine`
- `scan_once.sh`、`cron.sh` 或 `exclude.sh`

因此任何新增、编辑、删除、恢复、清空或扫描请求都只会生成模板化 HTTP 响应。
