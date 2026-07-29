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

状态与消息的对应关系：

| 状态 | 消息 |
| --- | --- |
| `ready` | `clamd is ready` |
| `error` | `clamd test connection failed` |
| `timeout` | `clamd ping timed out` |

同一次响应中的状态、消息和 ping 始终相互匹配。

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

### 定时扫描规则

`GET /api/cron/rules` 固定返回两个示例任务：

- `testenabled00001`：已启用，每天 `02:00` 扫描 `/scan/documents`。
- `testdisabled0002`：未启用，每周日 `04:30` 扫描 `/scan/uploads`。

新增、更新、启用、禁用、删除和 reload 请求均返回包含这两个任务的成功响应，不会
修改返回内容，也不会写入真实配置文件。

### 白名单

白名单固定包含三个条目：

- `/scan/trusted`
- `/scan/documents/approved`
- `/scan/uploads/example-safe.zip`

新增和删除请求均返回成功以及相同的三个条目，不会写入配置文件。

### 历史扫描结果

`POST /api/results/lookups` 立即返回成功信息和固定查询 ID：

```text
result-test-20260728070000
```

随后请求：

```sh
curl -u test:anything \
  http://localhost:8081/api/results/lookups/result-test-20260728070000
```

会立即得到 `status: success` 以及固定生成的 20 条结果。结果类型包含 `manual` 和
`cron`，结果值循环使用 `clean`、`found`、`error` 和 `null`。

`POST /api/results/detection` 不读取请求体中的任务 ID，始终返回固定测试任务以及：

```json
{
  "original": "Example Log File",
  "log": "Example Log File"
}
```

### 隔离区

`POST /api/quarantine/lookups` 立即返回成功信息和固定查询 ID：

```text
quarantine-test-20260728070000
```

轮询对应的 GET 接口会立即返回 `status: success` 以及 20 条固定隔离文件记录。
删除、恢复和清空操作均返回成功，但不会操作任何文件。

## API 行为总览

| 方法 | 路径 | 测试服务行为 |
| --- | --- | --- |
| `GET` | `/` | 返回测试服务说明 HTML。 |
| `GET` | `/api/status` | 返回时间动态、状态随机且字段匹配的完整状态。 |
| `GET` | `/api/browse` | 返回固定三层 `/scan` 目录树。 |
| `POST` | `/api/scans` | 返回固定的已入队批次，不启动扫描。 |
| `GET` | `/api/scans` | 返回一个运行中和一个等待中的示例批次。 |
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
| `POST` | `/api/results/lookups` | 立即返回成功及固定查询 ID。 |
| `GET` | `/api/results/lookups/{lookup_id}` | 立即返回 20 条固定结果。 |
| `POST` | `/api/results/detection` | 恒定返回 `Example Log File`。 |
| `POST` | `/api/results/clean` | 返回成功和 `deleted: 0`，不删除文件。 |
| `POST` | `/api/quarantine/clean` | 返回成功和 `deleted: 0`，不删除文件。 |
| `POST` | `/api/quarantine/lookups` | 立即返回成功及固定查询 ID。 |
| `GET` | `/api/quarantine/lookups/{lookup_id}` | 立即返回 20 条固定隔离记录。 |
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
- 文件浏览使用固定目录树。
- 历史结果查询立即返回 20 条记录。
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
