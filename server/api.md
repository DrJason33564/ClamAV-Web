# ClamAV TimeDock 后端 API

本文档按业务模块记录 Go 后端提供的 HTTP API。服务默认监听 `:8080`。

## 1. 通用约定

### 1.1 认证

后端使用内置用户和 Cookie session，不再支持 HTTP Basic Auth。

登录成功后返回名为 `clamavweb_session` 的 Cookie。Cookie 使用：

- `HttpOnly`；
- `SameSite=Strict`；
- `Path=/`；
- 24 小时绝对有效期；
- 2 小时空闲有效期；
- HTTPS 请求时设置 `Secure`；TLS 在反向代理终止时需设置 `SCANNER_COOKIE_SECURE=true`。

除以下接口和静态页面外，所有 `/api/` 请求都需要有效 Cookie：

- `POST /api/auth/register`：仅数据库为空且提供正确 `ADMIN_REGISTER_TOKEN` 时可匿名创建首任 admin；
- `POST /api/auth/login`；
- `GET /api/first-run/status`。

命令行示例：

```sh
curl -c cookie.txt \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"replace-this-password"}' \
  http://localhost:8080/api/auth/login

curl -b cookie.txt http://localhost:8080/api/status
```

### 1.2 用户隔离

扫描队列、cron、白名单、历史任务、任务日志、异步 lookup 和隔离区都按用户数据库中的不可变 `users.id` 隔离。用户名只用于登录、显示和日志；删除账户后重新创建同名账户会获得新的 ID，因此不会继承旧账户残留资产。admin 调用普通业务 API 时也只能操作自己的数据。

admin 额外拥有的权限仅包括：

- 用户管理；
- 服务配置；
- ClamAV sleep/wake 和其他全局 ClamAV 控制。

不存在 admin 查看所有用户任务、日志、cron、白名单或隔离文件的接口。

### 1.3 错误与状态码

通用错误格式：

```json
{"error":"error message"}
```

常见状态码：

- `200`：成功；
- `201`：资源创建成功；
- `202`：异步 lookup 或扫描已接受；
- `400`：请求格式或字段非法；
- `401`：未登录、Cookie 失效或密码错误；
- `403`：角色权限不足或跨来源请求被拒绝；
- `404`：资源不存在或资源不属于当前用户；
- `409`：扫描锁、休眠状态、最后一名 admin 或用户删除冲突；
- `429`：登录请求达到单 IP 或全局限制；响应包含 `Retry-After`；
- `500`：后端、文件系统、SQLite 或外部命令失败。

对其他用户资源的访问通常返回 `404`，避免泄露资源是否存在。

### 1.4 用户名、时间和路径

- 用户名格式：`[A-Za-z0-9._-]{1,64}`；
- 用户 ID 由后端生成，为同时包含小写字母和数字的 8 位 `[a-z0-9]` 字符串，不作为登录凭证；
- 任务 JSON 的时间为 Unix 秒时间戳；
- API 中 Go 运行时对象的时间仍可能使用 RFC3339；
- 文件浏览、手动扫描、cron 和白名单路径限制在 `/scan`；
- 后端解析符号链接，指向允许根目录之外的路径会被拒绝。

`IS_TIMEDOCK=Y` 时还会启用 TimeDock 账户目录限制。`timedock_account` 最多 64 个 Unicode 字符，只允许汉字、英文字母和数字，匹配文件夹名称时区分大小写。

## 2. 首次运行

### `GET /api/first-run/status`

无需登录。只返回首次运行状态，不返回 ClamAV 或任务信息。

```json
{"first_run":"not_completed"}
```

`WEB_FIRSTRUN_COMPLETED=2` 时返回 `completed`，其他情况返回 `not_completed`。

### `POST /api/first-run/complete`

需要 admin。完成前必须至少存在一名状态为 `active` 的 admin。

成功后原子更新 `/config/clamavweb.conf`：

```text
WEB_FIRSTRUN_COMPLETED=2
```

响应：

```json
{"status":"success","first_run":"completed"}
```

接口可重复调用。

## 3. 认证与当前账户

### `POST /api/auth/register`

请求：

```json
{
  "username": "alice",
  "password": "a sufficiently long password",
  "role": "user",
  "timedock_account": "Alice30",
  "token": "value from ADMIN_REGISTER_TOKEN"
}
```

两种行为：

1. 用户表为空且 `WEB_FIRSTRUN_COMPLETED=0`：允许匿名调用，密码必填，首个用户强制为 `admin`；请求体的 `token` 必须与非空环境变量 `ADMIN_REGISTER_TOKEN` 完全一致；
2. 用户表非空：仅 admin 可调用，`role` 可为 `user` 或 `admin`，密码可省略。

环境令牌只保护首位管理员注册。用户表非空后，`token` 可省略且不参与后续 admin 创建用户
的权限判断。环境变量未设置、令牌缺失或令牌错误时返回 `403`；首次运行已经完成但用户表
为空时返回 `409`，不会重新开放匿名注册。

密码省略时账户会被创建，但在 admin 设置密码前不能登录。

响应：

```json
{
  "status": "success",
  "username": "alice",
  "role": "user",
  "timedock_account": "Alice30",
  "password_set": true
}
```

密码使用带独立随机 salt 的 Argon2id 哈希保存，数据库不保存明文密码。

### `POST /api/auth/login`

请求：

```json
{"username":"alice","password":"password"}
```

成功响应会同时签发 session Cookie：

```json
{"status":"success","username":"alice","role":"user"}
```

被禁用、正在删除、无密码、用户名不存在或密码错误时统一返回 `401`。

登录限制按客户端 IP 和全局十分钟窗口计数，用户名变化不会产生新的限额。所有进入处理
流程的登录请求都会计数。达到单 IP 上限后仅该 IP 进入冷却；达到全局上限后所有登录进入
冷却。冷却期间返回 `429` 和 `Retry-After`，且不查询数据库或执行 Argon2，其他 API 不受
影响。

默认仅使用连接的 `RemoteAddr`。设置 `SERVER_TRUSTED_REVERSEPROXY` 后，可使用英文半角
逗号配置多个可信 IPv4/IPv6 地址；只有直接来源匹配列表中任意地址的请求才优先使用
`X-Forwarded-For` 首项，头缺失或非法时回退到 `RemoteAddr`。完整配置语义见
[`server_conf.md`](server_conf.md)。

### `POST /api/auth/logout`

撤销当前 session，并清除 Cookie。

```json
{"status":"success"}
```

### `GET /api/auth/me`

```json
{"username":"alice","role":"user","timedock_account":"Jason"}
```

### `PUT /api/auth/password`

请求：

```json
{
  "current_password": "old password",
  "new_password": "new password"
}
```

修改成功后撤销该用户的全部 session，并清除当前 Cookie，用户需要重新登录。

### `DELETE /api/auth/account`

用户主动注销。请求体必须再次提供当前密码：

```json
{"password":"current password"}
```

注销执行 clean delete：

- 撤销全部 session；
- 删除等待中的手动扫描；
- 删除异步 lookup；
- 删除该用户 cron 并 reload；
- 删除该用户白名单并重建 allow-list；
- 删除该用户隔离文件；
- 删除任务 JSON、任务日志、检出日志和历史索引；
- 最后物理删除用户。

存在 `waiting` 或 `running` 扫描时返回 `409`，不强制终止 ClamAV。最后一名有效 admin 不能注销。

## 4. 用户管理（admin）

本模块只返回账户管理字段，不能返回用户业务数据。

### `GET /api/admin/users`

响应：

```json
{
  "status": "success",
  "users": [
    {
      "username": "alice",
      "role": "user",
      "status": "active",
      "timedock_account": "Jason",
      "password_set": true,
      "created_at": 1783433000,
      "updated_at": 1783433000
    }
  ]
}
```

响应顶层包含 `"status":"success"`。`password_set` 只表示账户当前是否设置了密码，不返回密码哈希。`timedock_account` 可为空；TimeDock 模式下为空的用户不能浏览或提交扫描路径。

### `POST /api/admin/users`

与非首次运行时的 `POST /api/auth/register` 相同。建议管理端使用本路径。

```json
{
  "username": "bob",
  "password": "a sufficiently long password",
  "role": "user",
  "timedock_account": "Bob30"
}
```

`timedock_account` 可省略或设为空字符串，并使用与 PATCH 相同的校验规则。创建成功响应会返回 `timedock_account` 和 `password_set`。

### `PATCH /api/admin/users/{username}`

所有字段均可选：

```json
{
  "role": "admin",
  "status": "active",
  "timedock_account": "Jason",
  "password": "new password"
}
```

规则：

- `role` 只能是 `user` 或 `admin`；
- `status` 只能是 `active` 或 `disabled`；
- `timedock_account` 可为空；非空时最多 64 个 Unicode 字符，且只允许汉字、英文字母和数字；
- TimeDock 模式下修改 `timedock_account` 会停用该用户已启用的 cron，避免旧规则继续访问此前绑定的账户目录；
- `password: ""` 会清除密码，使用户无法登录；
- 设置或清除密码会撤销该用户全部 session；
- 禁用会撤销 session，并停用该用户当前启用的 cron；
- 重新启用用户时不会自动恢复 cron；
- 最后一名有效 admin 不能被降级或禁用。

禁用保留用户的历史、白名单和隔离文件，与注销/删除不同。

### `DELETE /api/admin/users/{username}`

执行与账户注销相同的 clean delete，但不需要目标用户密码。

- admin 不能通过此接口删除自己，应使用账户注销接口；
- 最后一名有效 admin 不能删除；
- 目标用户存在活动扫描时返回 `409`；
- 中途失败时用户保持 `deleting` 状态，禁止登录，admin 可重试删除。

## 5. 服务状态、配置与 ClamAV 控制

### `GET /api/status`

返回 ClamAV 全局状态和当前用户的扫描状态：

```json
{
  "source": {
    "version": 1,
    "clamd": {"status":"ready","message":"clamd is ready."},
    "scan": {
      "active_job_id": null,
      "last_job_id": "manual-1783433000",
      "last_job_status": "finished",
      "last_job_result": "clean"
    }
  },
  "ping": "ready",
  "ping_message": "clamd is ready",
  "checked_at": "2026-08-04T19:00:00+08:00",
  "first_run": "completed",
  "is_timedock": true
}
```

`status.json` 是全局文件，但 API 会过滤 `active_job_id`，并从 SQLite 查询当前用户自己的最近任务。不会返回其他用户任务。

`is_timedock` 在环境变量 `IS_TIMEDOCK=Y` 时为 `true`；未设置、为空或为 `N` 时为 `false`。

### `GET /api/config`

仅 admin：

```json
{
  "history_index_refresh_interval": 60,
  "clamav_sleep_timer": 3600,
  "web_firstrun_completed": 2,
  "web_login_max_tries": 10,
  "web_login_max_tries_overall": 100,
  "web_login_cooldown_interval": 600,
  "server_trusted_reverseproxy": "192.0.2.10,2001:db8::10"
}
```

### `PATCH /api/config`

仅 admin。当前可修改：

```json
{
  "history_index_refresh_interval": 120,
  "clamav_sleep_timer": 3600,
  "web_login_max_tries": 10,
  "web_login_max_tries_overall": 100,
  "web_login_cooldown_interval": 600,
  "server_trusted_reverseproxy": "192.0.2.10,2001:db8::10"
}
```

历史刷新间隔允许范围为 5–86400 秒。`clamav_sleep_timer` 为 `0` 时关闭定时休眠，启用时
必须是至少 600 的整数；空字符串不被接受。修改后立即重启对应计时器。登录限制和可信反代字段的范围、默认值及安全要求
见 [`server_conf.md`](server_conf.md)。修改任一登录限制会清空当前登录计数与冷却状态；可信
反代修改立即应用。传入空字符串可取消可信反代。

### `POST /api/clamav/sleep`

仅 admin。扫描锁存在时返回 `409`。成功响应：

```json
{"status":"sleeping","message":"ClamAV entered sleep mode."}
```

### `POST /api/clamav/wake`

仅 admin。执行 `/startup.sh --wake`，成功响应：

```json
{"status":"awake","message":"ClamAV woke successfully."}
```

sleep/wake 均为幂等操作。

定时休眠使用同一套 sleep 逻辑。服务启动、手动扫描成功启动及手动唤醒后会开始或重置计时；
休眠成功后暂停计时。扫描正在运行时不会强制关闭 ClamAV，定时器会在完整间隔后重试。

## 6. 文件浏览

### `GET /api/browse?path=/scan`

列出 `/scan` 下的文件和目录。响应包含 `path`、`parent`、`entries` 和 `roots`。路径为空时默认 `/scan`。

如果路径指向文件，则返回文件所在目录。目录排在文件之前，指向 `/scan` 外部的符号链接会被拒绝。

### TimeDock 模式

`IS_TIMEDOCK=Y` 时：

- `/scan` 的直接子目录中只识别真实目录 `extdev` 或名称匹配 `usb[0-9]+` 的真实目录；
- 请求 `/scan` 时，只返回第一层中实际包含当前用户账户目录的设备。例如存在 `/scan/usb1/Jason`、`/scan/usb2/Jason` 和 `/scan/usb3/James` 时，账户为 `Jason` 的用户只看到 `usb1` 和 `usb2`；
- 请求 `/scan/usb1` 时，只返回名称与 `timedock_account` 完全匹配的第一层账户目录；
- 进入 `/scan/usb1/Jason` 后按普通模式继续浏览其文件和子目录；
- 不能访问没有当前账户目录的设备、其他账户目录或通过符号链接跳转到其他账户；
- `timedock_account` 为空时返回 `400`：`{"error":"Please set your TimeDock account"}`。

`IS_TIMEDOCK` 未设置、为空或为 `N` 时保持原有文件浏览行为。其他值会导致服务拒绝启动。

## 7. 手动扫描与内存队列

### `POST /api/scans`

请求：

```json
{
  "targets": ["/scan/docs", "/scan/example.dat"],
  "action": "warn",
  "wait": false
}
```

- `action`：`warn`、`move` 或 `remove`，默认 `warn`；
- `wait` 只为兼容旧请求保留；
- owner ID 从登录 Cookie 对应的用户记录获得，客户端不能指定。

TimeDock 模式下，每个 `targets` 路径都必须位于当前用户匹配到的账户目录内；直接构造请求不能绕过文件浏览器的目录限制。

响应：

```json
{"id":"web-5f7c2e9b0a61b432","status":"queued","message":"Queued"}
```

ClamAV 休眠时返回 `409`，请求不会进入队列。

### 队列调度规则

ClamAV 扫描全局串行。每次选择下一任务时：

1. 收集当前拥有等待任务的用户；
2. 随机选择一名用户；
3. 执行该用户队列中的第一项。

同一用户内部保持 FIFO，并可重排。该策略避免先清空某个用户的完整队列再处理其他用户。

### `GET /api/scans?scope=all`

只返回当前用户的活动批次和等待批次：

```json
[
  {
    "id": "web-5f7c2e9b0a61b432",
    "status": "queued",
    "job_ids": [],
    "targets": ["/scan/docs"],
    "action": "warn",
    "started_at": null,
    "queue_number": 1
  }
]
```

`queue_number` 是当前用户队列内的相对位置，不暴露其他用户任务数量或位置。支持 `all` 或 `1-10` 形式的 scope。

### `POST /api/scans/reorder`

```json
{"id":"web-5f7c2e9b0a61b432","queue_number":1}
```

只改变当前用户任务之间的顺序，不移动、覆盖或泄露其他用户任务。运行中的任务不能重排。

### `POST /api/scans/cancel`

```json
{"id":"web-5f7c2e9b0a61b432","cancel":"Y"}
```

只能取消当前用户尚未开始的任务。

### 任务文件 version 3

`scan_once.sh` 生成：

```json
{
  "version": 3,
  "job_id": "manual-1783433000",
  "type": "manual",
  "user_id": "a1b2c3d4",
  "status": "finished",
  "target": "/scan/docs",
  "action": "warn",
  "started_at": 1783433000,
  "finished_at": 1783433010,
  "result": "clean"
}
```

任务 ID 使用 `manual-<Unix秒>` 或 `cron-<Unix秒>`。同一秒冲突时追加 `-001`、`-002`。运行中 `finished_at` 为 `null`。

## 8. 定时扫描

cron 配置实际结构：

```text
分钟 小时 日期 月份 星期 "扫描路径" action owner_user_id wake
```

例如：

```text
30 3 * * * /scan warn a1b2c3d4 N
0 */6 * * * "/scan/Team A" move a1b2c3d4 Y
```

`wake` 只能为 `Y` 或 `N`。`Y` 表示执行该规则时向 `scan_once.sh` 附加 `--wake`，在扫描前唤醒睡眠状态的 ClamAV；`N` 表示不主动唤醒。

### `GET /api/cron/rules`

只返回当前用户 ID 所属规则。owner ID 不由 API 输出，也不能由客户端修改。响应中的 `wake` 为布尔值。

### `POST /api/cron/rules`

```json
{
  "enabled": true,
  "minute": "30",
  "hour": "3",
  "day": "*",
  "month": "*",
  "weekday": "*",
  "target": "/scan",
  "action": "warn",
  "wake": true
}
```

后端生成规则 ID，并强制将当前 `users.id` 写入 owner 字段。`wake` 为 `true` 时规则文件写入 `Y`，为 `false` 时写入 `N`。

TimeDock 模式下，新增、完整更新和重新启用规则时都会校验 `target` 位于当前用户账户目录内。

### `PUT /api/cron/rules/{id}`

完整更新当前用户规则，字段结构与新增接口相同。不能更新其他用户同 ID 规则。

### `PATCH /api/cron/rules/{id}/enabled`

```json
{"enabled":false}
```

### `DELETE /api/cron/rules/{id}`

删除当前用户规则。

### `POST /api/cron/reload`

校验完整配置并重建系统 cron 文件。响应只返回当前用户规则。cron 文件的读写和 reload 使用进程锁，避免多用户并发保存导致丢失更新。

## 9. 白名单

`/config/exclude.conf` 每行格式：

```text
"路径" owner_user_id
```

### `GET /api/whitelist`

仅返回当前用户条目。

### `POST /api/whitelist`

```json
{"path":"/scan/trusted file.dat"}
```

后端写入当前 `users.id`，然后重新生成 SHA-256 allow-list 并让 ClamAV reload。

TimeDock 模式下，新增和删除请求中的路径都必须位于当前用户账户目录内。

### `DELETE /api/whitelist`

请求体同 POST，只删除 `path + 当前用户` 匹配的行。相同路径属于其他用户时不会被删除。

扫描运行或 ClamAV 休眠时不允许修改白名单。

注意：ClamAV allow-list 对共享 clamd 实例全局生效。API 所有权隔离不能改变一个用户的信任哈希会影响所有扫描这一 clamd 固有行为。

## 10. 历史任务与 SQLite 索引

服务启动时扫描一次 `/state/jobs`，运行期间监听任务 JSON 的新增、替换、修改和删除并进行单文件增量索引；`HISTORY_INDEX_REFRESH_INTERVAL` 控制周期完整刷新，作为文件事件丢失或监听不可用时的兜底。只接受包含合法 `user_id` 的 version 3 JSON，其他版本、旧文件名和无 owner ID 文件不会进入索引。

周期完整刷新会批量读取已索引文件的 mtime，并在内存中完成比对；mtime 未变化时不会重复解析 JSON。文件事件触发的增量索引不依赖 mtime，以免同一时间粒度内的连续原子替换被跳过。文件删除、损坏或变成非 version 3 后，相应索引会被删除。

### `POST /api/results/lookups?scope=1-20`

创建当前用户的异步历史查询。`scope` 必须显式提供为闭区间，单次最多包含 500 条；例如
`1-500` 和 `501-1000` 合法，空值、`all` 和 `1-501` 返回 `400`。分页范围不影响响应中的
完整任务总数。

```json
{
  "status": "pending",
  "lookup_id": "result-0123456789abcdef",
  "message": "result list is loading; poll /api/results/lookups/result-0123456789abcdef"
}
```

### `GET /api/results/lookups/{lookup_id}`

只有创建 lookup 的用户可轮询。成功响应：

```json
{
  "lookup_id": "result-0123456789abcdef",
  "status": "success",
  "results": [
    {"id":"manual-1783433000","type":"manual","date":"20260707173640","result":"clean","action":"warn"}
  ],
  "total": 1,
  "started_at": "2026-08-04T19:00:00+08:00",
  "updated_at": "2026-08-04T19:00:00+08:00"
}
```

列表、总数、排序和分页均由 SQLite 完成，并始终包含 `user_id` 条件。

响应字段及可能值：

| 字段 | 类型 | 可能值及含义 |
|---|---|---|
| `lookup_id` | string | 创建查询时生成的 `result-<随机ID>`；在该 lookup 生命周期内不变。 |
| `status` | string | `pending`：查询尚未完成；`success`：查询成功；`failed`：查询失败。 |
| `results` | array | `success` 时为本次分页范围内的任务数组；无匹配任务时为空数组。`pending` 或 `failed` 时可能省略。 |
| `results[].id` | string | 任务 ID，形式为 `manual-<Unix秒>` 或 `cron-<Unix秒>`，冲突时可能带三位序号后缀。 |
| `results[].type` | string | `manual`：手动扫描；`cron`：定时扫描。 |
| `results[].date` | string | 任务开始时间，格式为 `YYYYMMDDHHMMSS`，使用服务端时区。 |
| `results[].result` | string | `unknown`：等待中或运行中；`clean`：未检出威胁；`found`：检出威胁；`error`：扫描失败。 |
| `results[].action` | string | `warn`：仅记录；`move`：移动到隔离区；`remove`：直接删除检出文件。值来自 `history_jobs.action`。 |
| `total` | integer | 当前用户符合查询条件的历史任务总数，不受当前分页范围限制；未完成时为 `0`。 |
| `error` | string | 仅在 `failed` 时出现，内容为查询失败原因。 |
| `started_at` | string | lookup 创建时间，RFC3339 格式。 |
| `updated_at` | string | lookup 最近一次状态更新时间，RFC3339 格式。 |

接口状态码：`202` 对应 `pending`，`200` 对应 `success`，`500` 对应 `failed`；lookup 不存在或不属于当前用户时返回 `404`。

结果、统计和隔离区 lookup 共用当前用户最多两个 pending 查询的配额；达到上限时创建接口
返回 `429`。每个用户最多保留 10 个 lookup，超出时淘汰最旧的非 pending 条目。查询执行
最多 30 秒，terminal 状态保留 15 分钟；服务退出和账户删除会取消尚未结束的查询。后台
清理器每分钟运行一次，创建新查询时也会顺带清理过期条目。

### `POST /api/results/statistics/lookups?scope=N`

创建当前用户过去 `N` 个本地自然日的扫描统计查询。`scope` 必须显式提供，且必须是
`1` 到 `31` 之间的正整数。统计范围包含请求当天及之前的 `N-1` 天；当前日只统计到
请求发生时。日期边界使用服务进程的本地时区（即容器时区）。

创建成功返回 `202 Accepted`：

```json
{
  "status": "pending",
  "lookup_id": "statistics-0123456789abcdef",
  "scope": 3,
  "message": "history statistics are loading; poll /api/results/statistics/lookups/statistics-0123456789abcdef"
}
```

缺少 `scope`、非整数、零、负数或大于 `31` 时返回 `400 Bad Request`，且不会创建
lookup。

### `GET /api/results/statistics/lookups/{lookup_id}`

只有创建 lookup 的用户可以轮询。查询尚未完成时返回 `202 Accepted` 和
`status: pending`；成功后返回 `200 OK`：

```json
{
  "lookup_id": "statistics-0123456789abcdef",
  "status": "success",
  "scope": 3,
  "total": 10,
  "20260803": {"unknown":1,"clean":2,"found":1,"error":0},
  "20260804": {"unknown":0,"clean":0,"found":0,"error":0},
  "20260805": {"unknown":1,"clean":4,"found":0,"error":1},
  "started_at": "2026-08-05T12:00:00+08:00",
  "updated_at": "2026-08-05T12:00:01+08:00"
}
```

响应字段及可能值：

| 字段 | 类型 | 可能值及含义 |
|---|---|---|
| `lookup_id` | string | 创建时生成的 `statistics-<随机ID>`。 |
| `status` | string | `pending`：正在统计；`success`：统计完成；`failed`：查询失败。 |
| `scope` | integer | 创建 lookup 时指定的天数，范围为 `1–31`。 |
| `total` | integer | 当前用户在统计范围内的任务总数；等于所有日期四种结果计数之和。 |
| `YYYYMMDD` | object | 对应容器本地自然日的结果计数；范围内无任务的日期也会返回。 |
| `YYYYMMDD.unknown` | integer | `result` 为 `unknown` 的任务数；数据库中的其他未知结果也归入此项。 |
| `YYYYMMDD.clean` | integer | `result` 为 `clean`、即未检出威胁的任务数。 |
| `YYYYMMDD.found` | integer | `result` 为 `found`、即检出威胁的任务数。 |
| `YYYYMMDD.error` | integer | `result` 为 `error`、即扫描失败的任务数。 |
| `error` | string | 仅在 `failed` 时出现，内容为查询失败原因。 |
| `started_at` | string | lookup 创建时间，RFC3339 格式。 |
| `updated_at` | string | lookup 最近一次状态更新时间，RFC3339 格式。 |

统计直接从 `history.db` 的 `history_jobs` 表读取，始终包含当前 `users.id` 条件。接口按日和
结果在 SQLite 中聚合，不读取任务 JSON。异常结果归入 `unknown`。状态码为：等待时
`202`、成功时 `200`、失败时 `500`；lookup 不存在或属于其他用户时返回 `404`。

### `POST /api/results/detection`

```json
{"job_id":"manual-1783433000"}
```

后端先通过 SQLite 验证任务属于当前用户，再读取任务日志和检出日志：

```json
{
  "job_id": "manual-1783433000",
  "detections": [
    {"source_file":"/scan/eicar.txt","detection_reason":"Eicar-Test-Signature"}
  ],
  "original": "raw detection log",
  "log": "full scan log"
}
```

### `POST /api/results/clean`

请求 query 或 JSON 中必须包含 `clean_all=Y`：

```json
{"clean_all":"Y"}
```

只删除当前用户的任务 JSON、普通日志、检出日志和 SQLite 索引。admin 也不能清理其他用户历史。

## 11. 隔离区

隔离元数据格式：

```text
"原始路径" owner_user_id
```

旧单字段 `.rec` 不会被列出或自动归属。

### `POST /api/quarantine/lookups`

创建当前用户的异步隔离区查询。隔离区不使用 `scope`，查询会分批读取目录并返回该用户的
完整隔离条目列表，但仍受上述共享 pending 配额、30 秒执行超时和 lookup 保留规则约束。

### `GET /api/quarantine/lookups/{lookup_id}`

只有创建 lookup 的用户可轮询。成功响应：

```json
{
  "lookup_id": "quarantine-0123456789abcdef",
  "status": "success",
  "subjects": [
    {"name":"eicar.txt","source_file":"/scan/eicar.txt"}
  ]
}
```

### `DELETE /api/quarantine/delete/{filename}`

也接受 POST。只有 `.rec` owner ID 与当前 `users.id` 一致时才删除隔离文件和元数据。

### `POST /api/quarantine/recover/{filename}`

恢复前验证 owner ID。TimeDock 模式下，元数据中的原路径还必须位于用户当前绑定的账户目录内；即使文件来自该用户此前绑定的账户目录，也不会恢复到当前授权范围之外。原路径已经存在时拒绝覆盖；跨文件系统时使用复制、保留权限、删除隔离文件的回退流程。

### `POST /api/quarantine/clean`

请求必须包含 `clean_all=Y`。只删除当前用户的隔离文件和 `.rec`，不会清空整个隔离目录。

## 12. 持久化与配置文件

| 路径 | 用途 |
|---|---|
| `/data/users.db` | 用户、密码哈希和 session |
| `/data/history.db` | 历史任务索引 |
| `/config/clamavweb.conf` | 服务端配置 |
| `/config/cron_scan.conf` | 所有用户 cron 规则 |
| `/config/exclude.conf` | 所有用户白名单条目 |
| `/state/jobs/*.json` | version 3 任务事实文件 |
| `/log/*.log` | 扫描和检出日志 |
| `/quarantine/*` | 隔离文件及 owner 元数据 |

`/data`、`/config`、`/state`、`/log` 和 `/quarantine` 都应持久化。`USER_DATABASE_FILE` 和 `HISTORY_DATABASE_FILE` 可分别覆盖两个数据库路径。历史索引库可以从 version 3 JSON 重建，但用户和 session 只能从用户库恢复。用户 ID 是其他资产的所有权依据，因此重建 `users.db` 时必须同时清理 cron、白名单、任务状态和隔离区资产。

当前用户 ID 与资产格式不兼容早期的“用户名 owner / version 2 任务”开发数据。检测到旧 `users.db` 或 `history.db` schema 时服务会拒绝启动并提示重建；升级开发环境时应同时清理旧用户库、历史库、cron 规则、白名单、任务 JSON 和隔离区数据，不进行自动归属迁移。

`clamavweb.conf` 的全部字段、默认值和校验规则见 [`server_conf.md`](server_conf.md)。
