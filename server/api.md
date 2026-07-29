# Scanner API

本文档记录 `server` 当前提供的全部 HTTP API。后续新增、修改或删除 API 时，请同步更新本文档。

所有 API 默认监听在 `SCANNER_ADDR`，默认值为 `:8080`。所有接口都启用 HTTP Basic Auth，账号从 `SCANNER_ACCOUNTS` 读取，格式为：

```text
user:password
user1:password1,user2:password2
```

示例请求中的 `admin:secret` 请替换为实际账号。

## 通用约定

- 响应格式：API 路径默认返回 JSON，`/` 返回 HTML。
- 错误格式：

```json
{
  "error": "error message"
}
```

- 路径限制：文件浏览和手动扫描目标均限制在 `/scan` 下。
- 符号链接：后端会解析真实路径，指向 `/scan` 外部的 symlink 会被拒绝。
- 扫描动作：
  - `warn`：只告警。
  - `move`：移动到隔离区。
  - `remove`：直接删除。

## `GET /`

用途：返回 Scanner WebUI 页面。

调用：

```sh
curl -u admin:secret http://localhost:8080/
```

响应：HTML 页面。

## `GET /api/status`

用途：获取服务状态。返回 `/state/status.json` 的内容，并实时执行一次 `clamdscan --ping` 检查 clamd。

调用：

```sh
curl -u admin:secret http://localhost:8080/api/status
```

响应示例：

```json
{
  "source": {
    "version": 1,
    "updated_at": "2026-07-07T10:30:00+0800",
    "clamd": {
      "status": "ready",
      "last_checked_at": "2026-07-07T10:30:00+0800",
      "message": "clamd is ready."
    },
    "scan": {
      "active_job_id": null,
      "last_job_id": "manual-20260707103334",
      "last_job_status": "finished",
      "last_job_result": "clean"
    }
  },
  "ping": "ready",
  "ping_message": "clamd is ready",
  "checked_at": "2026-07-07T10:31:00+08:00"
}
```

字段说明：

- `source`：状态文件内容。如果状态文件不存在，会返回说明信息。
- `source.clamd.status`：ClamAV 状态；休眠成功后为 `sleep`。
- `source.clamd.message`：ClamAV 状态说明；`status` 为 `sleep` 时固定为 `clamd is sleeping.`。
- `source.scan.last_job_status`：后端根据 `source.scan.last_job_id` 读取 `/state/jobs/<last_job_id>.json` 后补充，可能为 `running`、`finished`、`failed` 或 `null`。
- `source.scan.last_job_result`：后端根据同一 job 状态文件中的 `result` 字段补充；如果状态文件不存在或字段不存在则为 `null`。
- `ping`：实时 clamd ping 结果，可能为 `ready`、`error`、`timeout`。
- `ping_message`：ping 结果说明。
- `checked_at`：本次 API 检查时间。

## `POST /api/clamav/sleep`

用途：让 ClamAV 进入休眠。后端通过 `/tmp/clamd.sock` 发送 `SHUTDOWN`；命令无响应并正常关闭连接后，原子创建 `/state/sleep.lock` 空目录作为休眠状态标记。Go Web 服务和 cron 保持运行。

调用：

```sh
curl -u admin:secret -X POST http://localhost:8080/api/clamav/sleep
```

成功响应：

```json
{
  "status": "sleeping",
  "message": "ClamAV entered sleep mode."
}
```

返回字段：

| 字段 | 类型 | 可能值 | 对应情况 |
| --- | --- | --- | --- |
| `status` | `string` | `sleeping` | SHUTDOWN 命令成功且 `/state/sleep.lock` 已存在；也包括接口调用前 ClamAV 已休眠的幂等成功情况。 |
| `message` | `string` | 状态说明 | 首次成功休眠时为 `ClamAV entered sleep mode.`；已经处于休眠状态时为 `ClamAV is already sleeping.`。 |

说明：

- 接口具有幂等性；ClamAV 已休眠时仍返回 `200 OK` 和 `sleeping`。
- 休眠成功后会原子更新 `/state/status.json`，将 `clamd.status` 写为 `sleep`、`clamd.message` 写为 `clamd is sleeping.`，并保留其他状态字段。
- `/state/scan.lock` 表示扫描正在执行时，接口返回 `409 Conflict`，不会关闭 ClamAV。
- socket 连接、写入、响应或休眠锁创建失败时返回 `500 Internal Server Error`。
- socket 操作超时时返回 `504 Gateway Timeout`。
- `409`、`500` 和 `504` 错误响应使用通用 `{"error":"error message"}` 结构，不包含 `status` 字段；当前接口不会返回 `status: "failed"`。
- 本接口当前不会改变手动或 cron 扫描流程；休眠状态下触发扫描的行为将在后续功能中处理。

## `POST /api/clamav/wake`

用途：唤醒 ClamAV。后端执行 `/startup.sh --wake`；该模式只负责确认或启动官方 `/init`、等待 ClamAV 返回 PONG，并删除 `/state/sleep.lock`，不创建目录、不校验应用配置，也不启动 cron 或 Go Web 服务。

调用：

```sh
curl -u admin:secret -X POST http://localhost:8080/api/clamav/wake
```

成功响应：

```json
{
  "status": "awake",
  "message": "ClamAV woke successfully."
}
```

返回字段：

| 字段 | 类型 | 可能值 | 对应情况 |
| --- | --- | --- | --- |
| `status` | `string` | `awake` | `/startup.sh --wake` 执行成功、ClamAV 已通过 PONG 检查且 `/state/sleep.lock` 已删除；也包括接口调用前 ClamAV 已就绪的幂等成功情况。 |
| `message` | `string` | 状态说明 | 实际执行唤醒并成功时为 `ClamAV woke successfully.`；调用前已经处于工作状态时为 `ClamAV is already awake.`。 |

说明：

- 接口具有幂等性；ClamAV 已可正常 PONG 时，`startup.sh --wake` 不会重复启动 `/init`，只删除可能存在的陈旧 sleep lock，并返回 `200 OK` 和 `awake`。
- 唤醒失败时保留 `/state/sleep.lock`，返回 `500 Internal Server Error`。
- 唤醒超时时返回 `504 Gateway Timeout`。
- `500` 和 `504` 错误响应使用通用 `{"error":"error message"}` 结构，不包含 `status` 字段；当前接口不会返回 `status: "failed"`。
- `/startup.sh --wake` 在需要新启动 `/init` 时会原子更新 `/state/clamav-init.pid`，使容器 PID 1 能继续监督并在容器退出时优雅停止最新进程；ClamAV 已就绪时不会改写 PID。

## `GET /api/browse`

用途：浏览 `/scan` 下的文件和目录，用于前端选择手动扫描目标。

查询参数：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `path` | 否 | 要浏览的绝对路径。为空时默认浏览 `/scan`。 |

调用：

```sh
curl -u admin:secret "http://localhost:8080/api/browse?path=/scan"
```

响应示例：

```json
{
  "path": "/scan",
  "entries": [
    {
      "name": "docs",
      "path": "/scan/docs",
      "is_dir": true,
      "size": 4096,
      "modified": "2026-07-07T10:20:00+08:00"
    },
    {
      "name": "sample.zip",
      "path": "/scan/sample.zip",
      "is_dir": false,
      "size": 1024,
      "modified": "2026-07-07T10:20:00+08:00"
    }
  ],
  "roots": ["/scan"]
}
```

说明：

- 如果 `path` 指向文件，后端会返回该文件所在目录的列表。
- `parent` 仅在父目录仍位于允许根目录内时返回。
- 返回结果中目录会排在文件前面。

## `POST /api/scans`

用途：提交手动扫描批次。后端会把批次加入内存队列，由队列调度器逐个目标调用 `scan_once.sh`；不传 `--id`，由脚本生成 `job_id` 并通过 stdout 返回。

请求体：

```json
{
  "targets": ["/scan/docs", "/scan/sample.zip"],
  "action": "warn",
  "wait": true
}
```

字段说明：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `targets` | 是 | 扫描目标列表，必须位于 `/scan` 下且存在。 |
| `action` | 否 | `warn`、`move`、`remove`，为空时默认 `warn`。 |
| `wait` | 否 | 兼容保留字段。后端接受该字段但不处理，提交到 `scan_once.sh` 的命令始终不追加 `--wait`。 |

调用：

```sh
curl -u admin:secret \
  -H "Content-Type: application/json" \
  -d '{"targets":["/scan/docs"],"action":"warn","wait":true}' \
  http://localhost:8080/api/scans
```

响应状态码：`202 Accepted`

响应示例：

```json
{
  "id": "web-5f7c2e9b0a61b432",
  "status": "queued",
  "message": "Queued"
}
```

ClamAV 休眠时返回 `409 Conflict`，不会生成批次 ID、加入内存队列或执行 `scan_once.sh`：

```json
{
  "id": "",
  "status": "failed",
  "message": "ClamAV is sleeping."
}
```

说明：

- `id` 是 WebUI 批次 ID，固定以 `web-` 开头。
- 新提交的批次先进入内存队列，响应中的 `status` 通常为 `queued`，`message` 为 `Queued`。
- 真正的扫描任务 ID 由 `scan_once.sh` 生成，例如 `manual-20260707103334`。
- 当前队列信息只保存在 Web 服务进程内；扫描完成后从内存队列移除。
- 队列调度器在启动手动批次前会检查 `/state/scan.lock`。如果锁中的 `pid` 仍存活，说明已有自动或其他扫描正在运行，手动批次保持 `queued`，后端每 5 秒重试一次。
- 如果 `/state/scan.lock` 不存在，或锁中的 `pid` 缺失、为空、非法、已退出，后端认为该锁不阻塞手动队列，并启动 `scan_once.sh`；无效锁文件由 `scan_once.sh` 自己在抢锁流程中清理。
- `wait` 仅为兼容旧调用保留；手动扫描队列串行调度，后端不会因为该字段向 `scan_once.sh` 传递 `--wait`。
- 后端在解析扫描请求和创建队列任务前检查 `/state/sleep.lock`；存在时直接返回上述休眠响应。

## `GET /api/scans`

用途：列出当前内存队列中的手动扫描批次，包括正在运行和等待中的批次。已完成批次不会出现在此接口；历史扫描结果由 `/api/results` 读取 `/state/jobs/*.json` 提供。

查询参数：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `scope` | 否 | 返回范围，支持 `all` 或数字范围如 `1-10`，默认 `all`。当范围起始数字为 `1` 时，会一并返回运行中的任务；起始数字不为 `1` 时，不返回运行中的任务。 |

调用：

```sh
curl -u admin:secret http://localhost:8080/api/scans
curl -u admin:secret 'http://localhost:8080/api/scans?scope=1-10'
```

响应示例：

```json
[
  {
    "id": "web-5f7c2e9b0a61b432",
    "status": "running",
    "job_ids": ["manual-20260707103334"],
    "targets": ["/scan/docs"],
    "action": "warn",
    "started_at": "2026-07-07T10:33:34Z",
    "queue_number": 0
  },
  {
    "id": "web-6a8d2a8f91c0e124",
    "status": "queued",
    "job_ids": [],
    "targets": ["/scan/docs"],
    "action": "move",
    "started_at": null,
    "queue_number": 1
  }
]
```

说明：

- `queue_number` 在每次响应时根据当前内存队列计算。
- 正在运行的批次 `queue_number` 固定为 `0`，`status` 为 `running`。
- 等待中的批次 `queue_number` 从 `1` 开始递增，`status` 为 `queued`。
- `started_at` 是实际开始扫描的时间；等待中的批次为 `null`。
- 队列前进、重排或取消后，等待中批次的 `queue_number` 会在下次响应中重新计算。

## `POST /api/scans/reorder`

用途：调整等待队列中的批次顺序。只能调整 `queue_number >= 1` 的等待批次；正在运行的批次 `queue_number = 0`，不能通过此接口调整。

请求体：

```json
{
  "id": "web-6a8d2a8f91c0e124",
  "queue_number": 1
}
```

调用：

```sh
curl -u admin:secret \
  -H "Content-Type: application/json" \
  -d '{"id":"web-6a8d2a8f91c0e124","queue_number":1}' \
  http://localhost:8080/api/scans/reorder
```

响应示例：

```json
{
  "status": "success",
  "message": "Queue reordered"
}
```

说明：

- 请求成功后只返回状态；前端如需最新队列，应再次调用 `GET /api/scans`。
- 如果目标 ID 不在等待队列中，或目标是正在运行的批次，返回 `400` 和 `status: failed`。

## `POST /api/scans/cancel`

用途：取消等待队列中的批次。只能取消 `queue_number >= 1` 的等待批次；正在运行的批次 `queue_number = 0`，不能通过此接口取消。

请求体：

```json
{
  "id": "web-6a8d2a8f91c0e124",
  "cancel": "Y"
}
```

调用：

```sh
curl -u admin:secret \
  -H "Content-Type: application/json" \
  -d '{"id":"web-6a8d2a8f91c0e124","cancel":"Y"}' \
  http://localhost:8080/api/scans/cancel
```

响应示例：

```json
{
  "status": "success",
  "message": "Queued scan canceled"
}
```

说明：

- `cancel` 必须为大写 `Y`。
- 请求成功后只返回状态；前端如需最新队列，应再次调用 `GET /api/scans`。
- 如果目标 ID 不在等待队列中，目标是正在运行的批次，或 `cancel` 不是 `Y`，返回 `400` 和 `status: failed`。

## 定时扫描规则格式

定时扫描规则保存在 `/config/cron_scan.conf`。WebUI 只管理带元数据的规则块，其他用户注释和说明文本会保留。

启用规则：

```text
# scanner-cron-rule id=abc123def456gh78 enabled=true
30 3 * * * /scan/docs warn
```

禁用规则：

```text
# scanner-cron-rule id=abc123def456gh78 enabled=false
# 30 3 * * * /scan/docs warn
```

规则 ID 由后端生成，长度 16 位，只包含小写字母和数字。

`scan_once.sh` 支持仅用于 cron 任务的 `--wake` 参数。带该参数的 cron 调用会在发现 `/state/sleep.lock` 时执行 `/startup.sh --wake`，成功后继续扫描；不带该参数时会生成完整的失败任务记录并在任务日志中写入 `[ERROR] ClamAV is sleeping`。当前 `cron.sh` 生成 crontab 时不会自动追加 `--wake`，定时规则与该参数的配置接入将在后续实现。

## `GET /api/cron/rules`

用途：列出 WebUI 管理的定时扫描规则。

调用：

```sh
curl -u admin:secret http://localhost:8080/api/cron/rules
```

响应示例：

```json
{
  "rules": [
    {
      "id": "abc123def456gh78",
      "enabled": true,
      "minute": "30",
      "hour": "3",
      "day": "*",
      "month": "*",
      "weekday": "*",
      "target": "/scan/docs",
      "action": "warn",
      "line": 12
    }
  ]
}
```

说明：

- 只读取 `# scanner-cron-rule` 元数据行及其下一行。
- 没有元数据的旧规则不会出现在结果中。

## `POST /api/cron/rules`

用途：新增定时扫描规则。保存成功后会自动执行 `/cron.sh reload`。

请求体：

```json
{
  "enabled": true,
  "minute": "30",
  "hour": "3",
  "day": "*",
  "month": "*",
  "weekday": "*",
  "target": "/scan/docs",
  "action": "warn"
}
```

调用：

```sh
curl -u admin:secret \
  -H "Content-Type: application/json" \
  -d '{"enabled":true,"minute":"30","hour":"3","day":"*","month":"*","weekday":"*","target":"/scan/docs","action":"warn"}' \
  http://localhost:8080/api/cron/rules
```

响应状态码：`201 Created`

响应示例：

```json
{
  "rules": [
    {
      "id": "abc123def456gh78",
      "enabled": true,
      "minute": "30",
      "hour": "3",
      "day": "*",
      "month": "*",
      "weekday": "*",
      "target": "/scan/docs",
      "action": "warn",
      "line": 4
    }
  ],
  "message": "Cron rules reloaded: 1 rule(s)."
}
```

## `PUT /api/cron/rules/{id}`

用途：完整更新指定定时扫描规则。保存成功后会自动执行 `/cron.sh reload`。

路径参数：

| 参数 | 说明 |
| --- | --- |
| `id` | 16 位规则 ID。 |

请求体与 `POST /api/cron/rules` 相同。请求体中的 `id` 会被忽略，以路径中的 `id` 为准。

调用：

```sh
curl -u admin:secret \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{"enabled":true,"minute":"0","hour":"*/6","day":"*","month":"*","weekday":"*","target":"/scan/docs","action":"move"}' \
  http://localhost:8080/api/cron/rules/abc123def456gh78
```

响应示例：

```json
{
  "rules": [],
  "message": "Cron rules reloaded: 1 rule(s)."
}
```

## `PATCH /api/cron/rules/{id}/enabled`

用途：启用或禁用指定定时扫描规则。保存成功后会自动执行 `/cron.sh reload`。

禁用规则时，后端会把元数据写成 `enabled=false`，并注释掉对应规则行；启用时会恢复规则行。

请求体：

```json
{
  "enabled": false
}
```

调用：

```sh
curl -u admin:secret \
  -X PATCH \
  -H "Content-Type: application/json" \
  -d '{"enabled":false}' \
  http://localhost:8080/api/cron/rules/abc123def456gh78/enabled
```

响应示例：

```json
{
  "rules": [],
  "message": "Cron rules reloaded: 0 rule(s)."
}
```

## `DELETE /api/cron/rules/{id}`

用途：删除指定定时扫描规则。只删除对应的元数据行和下一行规则，其他用户注释会保留。保存成功后会自动执行 `/cron.sh reload`。

调用：

```sh
curl -u admin:secret \
  -X DELETE \
  http://localhost:8080/api/cron/rules/abc123def456gh78
```

响应示例：

```json
{
  "rules": [],
  "message": "Cron rules reloaded: 0 rule(s)."
}
```

## `POST /api/cron/reload`

用途：手动 reload 当前 `/config/cron_scan.conf`。此接口不修改配置文件，只调用 `/cron.sh reload`。

调用：

```sh
curl -u admin:secret \
  -X POST \
  http://localhost:8080/api/cron/reload
```

响应示例：

```json
{
  "rules": [],
  "message": "Cron rules reloaded: 1 rule(s)."
}
```

## 定时规则校验

保存定时规则时，后端会先做基础校验：

- cron 字段支持 `*`、数字、范围、步进、逗号列表。
- `target` 必须存在，且真实路径必须位于 `/scan` 下。
- `action` 必须为 `warn`、`move`、`remove`。

Go 校验通过后，后端会写入临时配置文件并调用：

```sh
/cron.sh validate <tmpfile>
```

脚本校验成功时输出：

```text
Cron config is valid: 1 rule(s).
```

脚本校验失败时返回类似：

```text
Invalid crontab rule at: minute, line 3, value 99
```

后端会把该错误信息返回给前端：

```json
{
  "error": "Invalid crontab rule at: minute, line 3, value 99"
}
```

## `GET /api/whitelist`

用途：读取 `/config/exclude.conf` 中当前有效的白名单条目。空行、注释行和无法解析的行不会出现在结果中。

调用：

```sh
curl -u admin:secret http://localhost:8080/api/whitelist
```

响应示例：

```json
{
  "status": "success",
  "entries": [
    {
      "path": "/scan/trusted",
      "line": 1
    },
    {
      "path": "/scan/sample file.zip",
      "line": 2
    }
  ]
}
```

说明：

- `path` 是解析后的白名单路径。
- `line` 是该条目在 `/config/exclude.conf` 中的行号。

## `POST /api/whitelist`

用途：新增白名单条目。后端会先确认扫描锁不存在，再更新 `/config/exclude.conf`，随后执行 `/exclude.sh` 重新生成 ClamAV allow-list 数据库，并请求 `clamd` reload 数据库。

请求体：

```json
{
  "path": "/scan/trusted"
}
```

调用：

```sh
curl -u admin:secret \
  -H "Content-Type: application/json" \
  -d '{"path":"/scan/trusted"}' \
  http://localhost:8080/api/whitelist
```

成功响应：

```json
{
  "status": "success",
  "entries": [
    {
      "path": "/scan/trusted",
      "line": 1
    }
  ],
  "message": "Exclude allow-list refreshed."
}
```

忙碌响应：

```json
{
  "status": "busy",
  "message": "clamav is running"
}
```

失败响应：

```json
{
  "status": "failed",
  "message": "script output",
  "error": "error message"
}
```

ClamAV 休眠响应：

```json
{
  "status": "failed",
  "message": null,
  "error": "ClamAV is sleeping"
}
```

说明：

- `path` 必须存在，且真实路径必须位于 `/scan` 下。
- 如果路径已经存在于白名单中，后端不会重复写入，但仍会执行 `/exclude.sh` 刷新 allow-list 数据库。
- 如果扫描锁目录存在，返回 `409 Conflict`，`status` 为 `busy`。
- 如果 `/state/sleep.lock` 存在，返回 `409 Conflict` 和上述休眠响应，不修改白名单，也不执行刷新或唤醒。
- 如果 `/exclude.sh` 或 `clamd` reload 失败，返回 `failed`。

## `DELETE /api/whitelist`

用途：删除白名单条目。后端会先确认扫描锁不存在，再更新 `/config/exclude.conf`，随后执行 `/exclude.sh` 重新生成 ClamAV allow-list 数据库，并请求 `clamd` reload 数据库。

请求体：

```json
{
  "path": "/scan/trusted"
}
```

调用：

```sh
curl -u admin:secret \
  -X DELETE \
  -H "Content-Type: application/json" \
  -d '{"path":"/scan/trusted"}' \
  http://localhost:8080/api/whitelist
```

成功响应：

```json
{
  "status": "success",
  "entries": [],
  "message": "Exclude allow-list refreshed."
}
```

说明：

- 删除不存在的条目会返回 `400 Bad Request`，`status` 为 `failed`。
- 如果扫描锁目录存在，返回 `409 Conflict`，`status` 为 `busy`。
- 如果 `/state/sleep.lock` 存在，返回与 POST 相同的 `409 Conflict` 休眠响应，不修改白名单，也不执行刷新或唤醒。
- 如果 `/exclude.sh` 或 `clamd` reload 失败，返回 `failed`。

## `POST /api/results/lookups`

用途：启动一次历史扫描结果列表查询。后端会异步扫描 `/state/jobs` 下符合 job ID 规范的任务状态文件，避免文件数量较多时阻塞前端。

查询参数：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `scope` | 否 | 返回范围，支持 `all` 或数字范围如 `1-10`，默认 `all`。范围按 `date` 倒序后的结果序号计算，序号从 `1` 开始。 |

只会匹配以下文件名：

```text
manual-YYYYMMDDHHMMSS.json
cron-YYYYMMDDHHMMSS.json
```

调用：

```sh
curl -u admin:secret \
  -X POST \
  http://localhost:8080/api/results/lookups

curl -u admin:secret \
  -X POST \
  'http://localhost:8080/api/results/lookups?scope=1-10'
```

响应状态码：`202 Accepted`

响应示例：

```json
{
  "status": "pending",
  "lookup_id": "result-5f7c2e9b0a61b432",
  "message": "result list is loading; poll /api/results/lookups/result-5f7c2e9b0a61b432"
}
```

说明：

- 前端收到 `lookup_id` 后，应调用 `GET /api/results/lookups/{lookup_id}` 轮询结果。
- 后端会忽略非法 job ID 文件、非 JSON 后缀文件和其他普通文件。
- 后端会先根据文件名提取 `type` 和 `date`，按 `date` 倒序排序后应用 `scope`，然后只读取范围内的 JSON 文件。目录枚举仍会遍历 `/state/jobs`，但范围外的任务状态文件不会被打开解析。
- 如果 `scope` 格式非法，直接返回 `400`，不会创建异步查询任务。

## `GET /api/results/lookups/{lookup_id}`

用途：获取历史扫描结果列表查询状态和最终结果。

调用：

```sh
curl -u admin:secret \
  http://localhost:8080/api/results/lookups/result-5f7c2e9b0a61b432
```

查询中响应：

```json
{
  "lookup_id": "result-5f7c2e9b0a61b432",
  "status": "pending",
  "total": 0,
  "started_at": "2026-07-07T17:40:00+08:00",
  "updated_at": "2026-07-07T17:40:00+08:00"
}
```

完成响应：

```json
{
  "lookup_id": "result-5f7c2e9b0a61b432",
  "status": "success",
  "total": 2,
  "results": [
    {
      "id": "manual-20260707173642",
      "type": "manual",
      "date": "20260707173642",
      "result": "found"
    },
    {
      "id": "cron-20260707094101",
      "type": "cron",
      "date": "20260707094101",
      "result": "clean"
    }
  ],
  "started_at": "2026-07-07T17:40:00+08:00",
  "updated_at": "2026-07-07T17:40:01+08:00"
}
```

说明：

- `id` 是 job ID，不含 `.json` 后缀。
- `type` 来自 job ID 前缀，可能为 `manual` 或 `cron`。
- `date` 是 job ID 中的 `YYYYMMDDHHMMSS` 时间部分。
- `result` 来自 `/state/jobs/<job_id>.json` 中的 `result` 字段，可能为 `clean`、`found`、`error` 或 `null`。
- `total` 是 `/state/jobs` 下所有符合 job ID 文件名规范的文件总数，不受 `scope` 范围限制。
- 结果按 `date` 倒序排列。

## `POST /api/results/detection`

用途：查询指定 job ID 对应的扫描日志和 ClamAV detection 日志。后端会先读取 `/log/<job_id>.log` 作为普通扫描日志，再尝试读取 `/log/clamav_detection_<job_id>.log`，并从 detection 日志中提取 `Source file` 和 `Detection reason`。

请求体：

```json
{
  "job_id": "manual-20260707173642"
}
```

调用：

```sh
curl -u admin:secret \
  -H "Content-Type: application/json" \
  -d '{"job_id":"manual-20260707173642"}' \
  http://localhost:8080/api/results/detection
```

找到日志时响应：

```json
{
  "job_id": "manual-20260707173642",
  "detections": [
    {
      "source_file": "/scan/eicar.txt",
      "detection_reason": "Eicar-Test-Signature"
    }
  ],
  "original": "2026-07-07 17:37:10 ------------ ClamAV Detection ------------\n...",
  "log": "2026-07-07 17:36:42 [INFO] Scan started\n..."
}
```

未找到 detection 日志时响应：

```json
{
  "job_id": "manual-20260707173642",
  "detections": [],
  "original": "",
  "log": "2026-07-07 17:36:42 [INFO] Scan started\n..."
}
```

说明：

- 未找到 detection 日志不是错误，通常表示该任务没有检出威胁；此时仍会返回普通扫描日志。
- `job_id` 必须符合 `manual-YYYYMMDDHHMMSS` 或 `cron-YYYYMMDDHHMMSS`。
- `detections` 返回所有检出记录；仅有单条记录时也返回数组。
- `original` 返回完整 detection 日志内容；如果 detection 日志不存在，则为空字符串。
- `log` 返回 `/log/<job_id>.log` 的完整内容；如果普通扫描日志不存在，则为空字符串。

## `POST /api/results/clean`

用途：清理历史扫描结果相关文件。当前只支持清理全部。

清理范围：

- `/log` 下所有以 `manual-`、`cron-`、`clamav_detection` 开头且以 `.log` 结尾的文件。
- `/state/jobs` 下所有以 `manual-`、`cron-` 开头且以 `.json` 结尾的文件。

请求体：

```json
{
  "clean_all": "Y"
}
```

也可以用查询参数：

```sh
curl -u admin:secret \
  -X POST \
  "http://localhost:8080/api/results/clean?clean_all=Y"
```

成功响应：

```json
{
  "status": "success",
  "deleted": 12
}
```

失败响应：

```json
{
  "status": "failed",
  "deleted": 3,
  "error": "error message"
}
```

说明：

- `deleted` 是成功删除的目录项数量。
- 当前必须传 `clean_all=Y`，否则返回 `400 Bad Request`。

## `POST /api/quarantine/clean`

用途：清理隔离区。当前只支持清理全部。

清理范围：

- `/quarantine` 下所有子项，包括文件和子目录。

请求体：

```json
{
  "clean_all": "Y"
}
```

也可以用查询参数：

```sh
curl -u admin:secret \
  -X POST \
  "http://localhost:8080/api/quarantine/clean?clean_all=Y"
```

成功响应：

```json
{
  "status": "success",
  "deleted": 4
}
```

失败响应：

```json
{
  "status": "failed",
  "deleted": 1,
  "error": "error message"
}
```

说明：

- `deleted` 是成功删除的隔离区子项数量。
- 当前必须传 `clean_all=Y`，否则返回 `400 Bad Request`。

## `POST /api/quarantine/lookups`

用途：启动一次隔离区文件列表查询。查询行为与 `POST /api/results/lookups` 一致，先返回 `lookup_id`，前端再轮询 `GET /api/quarantine/lookups/{lookup_id}`。

调用：

```sh
curl -u admin:secret \
  -X POST \
  http://localhost:8080/api/quarantine/lookups
```

响应状态码：`202 Accepted`

响应示例：

```json
{
  "status": "pending",
  "lookup_id": "quarantine-5f7c2e9b0a61b432",
  "message": "quarantine list is loading; poll /api/quarantine/lookups/quarantine-5f7c2e9b0a61b432"
}
```

## `GET /api/quarantine/lookups/{lookup_id}`

用途：获取隔离区文件列表查询状态和最终结果。

查询中响应：

```json
{
  "lookup_id": "quarantine-5f7c2e9b0a61b432",
  "status": "pending",
  "started_at": "2026-07-07T18:10:00+08:00",
  "updated_at": "2026-07-07T18:10:00+08:00"
}
```

完成响应：

```json
{
  "lookup_id": "quarantine-5f7c2e9b0a61b432",
  "status": "success",
  "subjects": [
    {
      "name": "eicar.txt",
      "source_file": "/scan/eicar.txt"
    }
  ],
  "started_at": "2026-07-07T18:10:00+08:00",
  "updated_at": "2026-07-07T18:10:01+08:00"
}
```

说明：

- `subjects[].name` 是隔离区内的文件名。
- `subjects[].source_file` 来自同名 `.rec` 文件内容，例如 `/quarantine/eicar.txt.rec`。
- `.rec` 文件本身不会作为隔离区主体返回。
- ClamAV 在隔离移动时可能生成的内部锁文件 `clamav-quarantine-lock` 不会作为隔离区主体返回。

## `POST /api/quarantine/delete/{filename}`

用途：删除隔离区指定文件。删除成功后，后端会一并删除同名 `.rec` 文件。

调用：

```sh
curl -u admin:secret \
  -X POST \
  http://localhost:8080/api/quarantine/delete/eicar.txt
```

成功响应：

```json
{
  "status": "success"
}
```

失败响应：

```json
{
  "status": "failed",
  "error": "error message"
}
```

说明：

- `{filename}` 必须是单个文件名，不能包含路径分隔符。
- 不允许直接删除 `.rec` 文件。

## `POST /api/quarantine/recover/{filename}`

用途：恢复隔离区指定文件。后端会读取同名 `.rec` 文件，取其中半角双引号包裹的源文件路径，将隔离文件恢复到该路径；同一文件系统内优先移动，跨 Docker 挂载或跨设备时自动改为复制后删除隔离文件。恢复成功后删除 `.rec` 文件。

调用：

```sh
curl -u admin:secret \
  -X POST \
  http://localhost:8080/api/quarantine/recover/eicar.txt
```

成功响应：

```json
{
  "status": "success"
}
```

失败响应：

```json
{
  "status": "failed",
  "error": "error message"
}
```

说明：

- `.rec` 内容格式必须类似：`"/scan/eicar.txt"`。
- 如果目标源文件路径已存在，恢复会失败，避免覆盖现有文件。
- 恢复失败时不会删除 `.rec` 文件。

## 状态码

| 状态码 | 场景 |
| --- | --- |
| `200` | 请求成功。 |
| `201` | 定时扫描规则已创建。 |
| `202` | 扫描批次已接受。 |
| `400` | 请求参数错误，例如路径不在 `/scan` 下、action 不合法。 |
| `401` | 未提供 Basic Auth 或账号密码错误。 |
| `403` | 目录存在但不可读。 |
| `404` | 路径或扫描批次不存在。 |
| `405` | HTTP 方法不允许。 |
| `409` | 白名单编辑时扫描锁存在，ClamAV 正在运行。 |
