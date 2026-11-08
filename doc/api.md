# ClamAV TimeDock 后端 API

本文档只记录 HTTP 接口的认证、请求、响应与调用行为。服务默认监听 `:8080`。

配置文件格式见 [config.md](config.md)，任务文件格式见 [job_file_schema.md](job_file_schema.md)，历史数据库表结构与索引实现见 [history_db_schema.md](history_db_schema.md)。

## 1. 通用约定

### 1.1 认证

后端使用内置用户和 Cookie session，不再支持 HTTP Basic Auth。

所有静态页面、API 和错误响应统一包含 `X-Content-Type-Options: nosniff`、禁止页面嵌入的
`Content-Security-Policy` / `X-Frame-Options`、`Referrer-Policy: no-referrer` 和限制浏览器
敏感能力的 `Permissions-Policy`。后端不发送 HSTS；始终使用 HTTPS 的部署应由 TLS 终止
反向代理按实际域名配置 `Strict-Transport-Security`。

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
- `409`：扫描忙碌、休眠状态、索引重建、最后一名 admin 或用户删除冲突；
- `429`：登录请求达到单 IP/全局限制，或全局 Argon2 处理容量已满；响应包含 `Retry-After`；
- `500`：后端、文件系统、SQLite 或外部命令失败。

对其他用户资源的访问通常返回 `404`，避免泄露资源是否存在。

### 1.4 用户名、时间和路径

- 用户名格式：`[A-Za-z0-9._-]{1,64}`；
- 用户 ID 由后端生成，为同时包含小写字母和数字的 8 位 `[a-z0-9]` 字符串，不作为登录凭证；
- API 时间字段按具体响应说明使用 RFC3339 或格式化日期字符串；
- 文件浏览、手动扫描、cron 和白名单路径限制在 `/scan`；
- 文件浏览不会返回符号链接；扫描、cron、白名单和隔离区恢复路径的任意现有层级包含符号链接时都会被拒绝。

`IS_TIMEDOCK=Y` 时还会启用 TimeDock 账户目录限制。`timedock_account` 最多 64 个 Unicode 字符，只允许汉字、英文字母和数字，匹配文件夹名称时区分大小写。

## 2. 首次运行

### `GET /api/first-run/status`

无需登录。只返回首次运行状态，不返回 ClamAV 或任务信息。

```json
{"first_run":"not_completed"}
```

首次运行已完成时返回 `completed`，尚未完成时返回 `not_completed`。

### `POST /api/first-run/complete`

需要 admin。完成前必须至少存在一名状态为 `active` 的 admin。

成功后持久保存首次运行已完成状态。响应：

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

密码不会以明文返回。

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
冷却。冷却期间返回 `429` 和 `Retry-After`，其他 API 不受影响。

所有密码相关操作共享全局 5 个并发槽位，包括登录、注册、修改
密码、删除账户和管理员设置密码。槽位满载时请求不排队，立即返回 `429` 和
`Retry-After: 1`，请求不会进入密码处理阶段。登录计数限流与该并发边界相互独立。

默认仅使用连接的 `RemoteAddr`。设置 `SERVER_TRUSTED_REVERSEPROXY` 后，可使用英文半角
逗号配置多个可信 IPv4/IPv6 地址；只有直接来源匹配列表中任意地址的请求才优先使用
`X-Forwarded-For` 首项，头缺失或非法时回退到 `RemoteAddr`。完整配置语义见
[配置说明](config.md)。

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
    "clamd": {"status":"ready"},
    "scan": {
      "active_job_id": null,
      "last_job_id": "manual-1783433000",
      "last_job_status": "finished",
      "last_job_result": "clean"
    }
  },
  "ping": "ready",
  "ping_message": "clamd is ready",
  "clamd_version": "1.5.4",
  "database_version": "28098",
  "database_date": "Thu Aug 14 14:24:22 2026",
  "checked_at": "2026-08-04T19:00:00+08:00",
  "first_run": "completed",
  "is_timedock": true
}
```

`active_job_id` 和最近任务字段仅返回当前用户可见的任务，不会返回其他用户任务。

`ping` 与 `ping_message` 表示引擎连通性。引擎就绪时返回程序及病毒库版本；探测失败时相应版本字段为空字符串。

上述实时状态由后端缓存 5 秒；`checked_at` 是当前缓存结果的探测完成时间。缓存期内的并发状态请求不会重复执行 ping 或 VERSION 查询，ClamAV 休眠或唤醒成功后缓存立即失效。

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
见 [配置说明](config.md)。修改任一登录限制会清空当前登录计数与冷却状态；可信
反代修改立即应用。传入空字符串可取消可信反代。

### `POST /api/clamav/sleep`

仅 admin。存在排队或活动扫描时返回 `409`。成功响应：

```json
{"status":"sleeping","message":"ClamAV entered sleep mode."}
```

### `POST /api/clamav/wake`

仅 admin。唤醒引擎，成功响应：

```json
{"status":"awake","message":"ClamAV woke successfully."}
```

sleep/wake 均为幂等操作。

自动休眠的配置与生效规则见 [配置说明](config.md#3-clamav-定时休眠)。

## 6. 文件浏览

### `GET /api/browse?path=/scan`

列出 `/scan` 下的文件和目录。响应包含 `path`、`parent`、`entries`、`roots` 和 `target`。路径为空时默认 `/scan`。

`target` 返回经过后端校验和规范化的请求目标，其中 `path` 为目标路径，`is_dir` 表示目标是否为目录。目录请求的 `target.path` 与响应 `path` 相同；文件请求的 `target.path` 为文件路径，响应 `path` 则为文件所在目录。

```json
{
  "path": "/scan/docs",
  "parent": "/scan",
  "entries": [],
  "roots": ["/scan"],
  "target": {"path": "/scan/docs/example.dat", "is_dir": false}
}
```

如果路径指向文件，则返回文件所在目录。目录排在文件之前；符号链接不会出现在 `entries` 中，直接请求符号链接路径会返回 `400`。

### TimeDock 模式

`IS_TIMEDOCK=Y` 时：

- `/scan` 的直接子目录中只识别真实目录 `extdev` 或名称匹配 `usb[0-9]+` 的真实目录；
- 请求 `/scan` 时，只返回第一层中实际包含当前用户账户目录的设备。例如存在 `/scan/usb1/Jason`、`/scan/usb2/Jason` 和 `/scan/usb3/James` 时，账户为 `Jason` 的用户只看到 `usb1` 和 `usb2`；
- 请求 `/scan/usb1` 时，只返回名称与 `timedock_account` 完全匹配的第一层账户目录；
- 进入 `/scan/usb1/Jason` 后按普通模式继续浏览其文件和子目录；
- 不能访问没有当前账户目录的设备或其他账户目录；所有符号链接均隐藏并拒绝，不区分其目标是否仍在当前账户内；
- `timedock_account` 为空时返回 `400`：`{"error":"Please set your TimeDock account"}`。

`IS_TIMEDOCK` 未设置、为空或为 `N` 时保持原有文件浏览行为。其他值会导致服务拒绝启动。

## 7. 手动扫描与内存队列

### `POST /api/scans`

请求：

```json
{
  "targets": ["/scan/docs", "/scan/example.dat"],
  "action": "warn",
  "wait": false,
  "LOCK_FILE": false
}
```

- `action`：`warn`、`move` 或 `remove`，默认 `warn`；
- `wait` 只为兼容旧请求保留；
- `LOCK_FILE` 为可选 JSON 布尔值，默认 `false`。为 `true` 时尝试在扫描前锁定文件，提交给引擎后释放；锁定失败会记录警告并继续扫描。`null`、字符串和数字返回 `400`。
- owner ID 从登录 Cookie 对应的用户记录获得，客户端不能指定。

TimeDock 模式下，每个 `targets` 路径都必须位于当前用户匹配到的账户目录内；直接构造请求不能绕过文件浏览器的目录限制。
所有模式下，`targets` 的任意路径层级均不得为符号链接。

响应：

```json
{"id":"web-5f7c2e9b0a61b432","status":"queued","message":"Queued"}
```

ClamAV 休眠时返回 `409`，请求不会进入队列。

### 队列调度规则

扫描全局串行。同一用户等待任务默认保持 FIFO，并可通过重排接口调整；跨用户不承诺先到先执行，也不暴露其他用户队列位置。调度实现见 [开发指南](../DEV.md)。

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

## 8. 定时扫描

规则的 HTTP 字段为 `enabled`、`minute`、`hour`、`day`、`month`、`weekday`、`target`、`action`、`wake`；后五段时间字段使用字符串表示。支持范围、步长和列表，星期日可为 `0` 或 `7`。`wake` 为布尔值，表示引擎休眠时是否主动唤醒；为 `false` 时，休眠期间触发的任务会记录失败。

规则文件的语法与调度规则见 [配置说明](config.md#9-定时扫描规则-cron_scanconf)。

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

后端生成规则 ID，规则归属当前登录用户；客户端不能指定其他 owner。

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

重新加载有效的定时规则，响应只返回当前用户规则。配置非法时返回 `400`，保留此前生效的调度。

## 9. 白名单

文件格式见 [配置说明](config.md#10-信任区条目-excludeconf)。

### `GET /api/whitelist`

仅返回当前用户条目。

### `POST /api/whitelist`

```json
{"path":"/scan/trusted file.dat"}
```

将条目加入当前用户信任区，并请求引擎应用更新。响应成功表示重载请求已接受，不表示已通过扫描验证新签名库生效。

TimeDock 模式下，新增和删除请求中的路径都必须位于当前用户账户目录内。

### `DELETE /api/whitelist`

请求体同 POST，只删除 `path + 当前用户` 匹配的条目。相同路径属于其他用户时不会被删除。

存在排队或活动扫描、或 ClamAV 休眠时不允许修改白名单。

注意：ClamAV allow-list 对共享 clamd 实例全局生效。API 所有权隔离不能改变一个用户的信任哈希会影响所有扫描这一 clamd 固有行为。

## 10. 历史任务与索引管理

任务文件格式见 [任务文件说明](job_file_schema.md)，索引存储与重建机制见 [历史数据库说明](history_db_schema.md)。除明确标注的管理员全局索引重建外，以下接口仅返回当前用户可见的历史。

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

列表、总数、排序和分页均限定在当前用户的历史中。

响应字段及可能值：

| 字段 | 类型 | 可能值及含义 |
|---|---|---|
| `lookup_id` | string | 创建查询时生成的 `result-<随机ID>`；在该 lookup 生命周期内不变。 |
| `status` | string | `pending`：查询尚未完成；`success`：查询成功；`failed`：查询失败。 |
| `results` | array | `success` 时为本次分页范围内的任务数组；无匹配任务时为空数组。`pending` 或 `failed` 时可能省略。 |
| `results[].id` | string | 任务 ID，应作为不透明字符串传递；持久化命名规则见 [任务文件说明](job_file_schema.md)。 |
| `results[].type` | string | `manual`：手动扫描；`cron`：定时扫描。 |
| `results[].date` | string | 任务开始时间，格式为 `YYYYMMDDHHMMSS`，使用服务端时区。 |
| `results[].result` | string | `unknown`：等待中或运行中；`clean`：未检出威胁；`found`：检出威胁；`error`：扫描失败。 |
| `results[].action` | string | `warn`：仅记录；`move`：移动到隔离区；`remove`：直接删除检出文件。取值与提交扫描时的处理方式一致。 |
| `total` | integer | 当前用户符合查询条件的历史任务总数，不受当前分页范围限制；未完成时为 `0`。 |
| `error` | string | 仅在 `failed` 时出现，内容为查询失败原因。 |
| `started_at` | string | lookup 创建时间，RFC3339 格式。 |
| `updated_at` | string | lookup 最近一次状态更新时间，RFC3339 格式。 |

接口状态码：`202` 对应 `pending`，`200` 对应 `success`，`500` 对应 `failed`；lookup 不存在或不属于当前用户时返回 `404`。

结果、统计和隔离区 lookup 共用当前用户最多两个 pending 查询的配额；达到上限时创建接口
返回 `429`。每个用户最多保留 10 个 lookup，超出时淘汰最旧的非 pending 条目。查询执行
最多 30 秒，terminal 状态保留 15 分钟；服务退出和账户删除会取消尚未结束的查询。

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

统计仅包含当前用户历史，按日和结果聚合；异常结果归入 `unknown`。状态码为：等待时
`202`、成功时 `200`、失败时 `500`；lookup 不存在或属于其他用户时返回 `404`。

### `POST /api/results/detection`

```json
{"job_id":"manual-1783433000"}
```

只返回当前用户所属任务的结构化检出、原始检出日志和扫描日志：

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

### `POST /api/results/index`

仅 admin 可调用，无必填请求体。读取所有用户的任务文件，忽略 mtime，强制重建任务和检出索引；不返回其他用户任务内容。

```json
{"status":"success","checked":120,"updated":120,"failed":0}
```

成功返回 `200`：`checked` 为检查的任务条目数，`updated` 为成功重建数，`failed` 为无法解析的条目数。存在损坏文件时仍返回 `200`、`status=partial`，并保留这些文件此前的有效索引；缺失文件对应的索引会被清理。重复重建返回 `409`，普通用户返回 `403`，后端错误返回 `500`。重建失败不会发布部分数据库更新，扫描可继续执行。

### `POST /api/results/clean`

请求 query 或 JSON 中必须包含 `clean_all=Y`：

```json
{"clean_all":"Y"}
```

只清理当前用户已结束任务的历史及相关日志；正在运行的任务保留。admin 也不能清理其他用户历史。

## 11. 隔离区

隔离记录的内部格式见 [配置与关联文件说明](config.md#13-隔离元数据-rec)。

### `POST /api/quarantine/lookups`

创建当前用户的异步隔离区查询。隔离区不使用 `scope`，返回该用户的
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

也接受 POST。只删除当前用户所属的隔离文件及其恢复记录。

### `POST /api/quarantine/recover/{filename}`

恢复前验证 owner ID。TimeDock 模式下，元数据中的原路径还必须位于用户当前绑定的账户目录内；即使文件来自该用户此前绑定的账户目录，也不会恢复到当前授权范围之外。原路径已经存在时拒绝覆盖；恢复成功后移除隔离区副本和恢复记录。

### `POST /api/quarantine/clean`

请求必须包含 `clean_all=Y`。只删除当前用户的隔离文件和 `.rec`，不会清空整个隔离目录。
