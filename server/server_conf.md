# ClamAV TimeDock 服务端配置

本文档记录 `/config/clamavweb.conf` 中各配置项的作用、默认值、允许范围和运行时行为。
配置文件使用一行一个 `KEY=VALUE` 的格式，允许空行和以 `#` 开头的注释，不允许重复键
或未识别的键。服务创建或重写配置文件时使用 `0600` 权限和原子替换。

## 1. 默认配置

```ini
HISTORY_INDEX_REFRESH_INTERVAL=60
WEB_FIRSTRUN_COMPLETED=0
WEB_LOGIN_MAX_TRIES=10
WEB_LOGIN_MAX_TRIES_OVERALL=100
WEB_LOGIN_COOLDOWN_INTERVAL=600
SERVER_TRUSTED_REVERSEPROXY=
```

旧配置文件未包含新增键时使用对应默认值；后续通过管理 API 保存配置时会写出全部键。

## 2. 历史索引

### `HISTORY_INDEX_REFRESH_INTERVAL`

- 类型：整数，单位为秒；
- 默认值：`60`；
- 允许范围：`5–86400`；
- 作用：控制运行中扫描 `/state/jobs/*.json` 并按需重建 `history.db` 索引的间隔。

服务启动时无论该值为何都会先刷新一次历史索引。运行时修改后，后台索引器会在下一轮读取
新值，无需重启。

## 3. 首次运行

### `WEB_FIRSTRUN_COMPLETED`

- 类型：整数；
- 默认值：`0`；
- 允许值：`0`、`2`；
- `0`：首次运行尚未完成；
- `2`：首次运行已经完成。

该值由首次运行完成接口维护。用户表为空时，只有值为 `0` 才允许使用
`ADMIN_REGISTER_TOKEN` 创建首位管理员；值为 `2` 时不会因为用户表意外为空而重新开放
匿名注册。

## 4. 登录请求限制

登录统计窗口固定为十分钟。所有获准进入登录处理流程的请求都会计数，包括登录成功、密码
错误、用户名不存在和请求体错误。处于冷却状态的请求直接返回 `429`，不会查询用户数据库、
执行 Argon2 或创建新的 IP 状态。

### `WEB_LOGIN_MAX_TRIES`

- 类型：正整数；
- 默认值：`10`；
- 允许范围：`1–10000`；
- 作用：单个客户端 IP 在十分钟窗口内允许进入处理流程的登录请求数。

达到上限后只冷却该客户端 IP，不影响其他来源。

### `WEB_LOGIN_MAX_TRIES_OVERALL`

- 类型：正整数；
- 默认值：`100`；
- 允许范围：不小于 `WEB_LOGIN_MAX_TRIES`，且不大于 `1000000`；
- 作用：所有客户端 IP 在同一个十分钟窗口内允许进入处理流程的登录请求总数。

达到上限后暂停所有新的登录请求，并立即清空单 IP 状态映射。全局冷却期间不会创建新的
映射条目。单 IP 映射的最大容量也使用该值，后台每十分钟清理过期条目。

### `WEB_LOGIN_COOLDOWN_INTERVAL`

- 类型：正整数，单位为秒；
- 默认值：`600`；
- 允许范围：`1–86400`；
- 作用：单 IP 或全局限制触发后的冷却时间。

冷却只影响登录接口，已有 Cookie session 和其他 API 会继续正常工作。`429` 响应包含
`Retry-After` 头。管理员通过 `/api/config` 修改以上任一登录限制后，当前计数和冷却状态
会被清空，使新配置立即生效。

## 5. 可信反向代理

### `SERVER_TRUSTED_REVERSEPROXY`

- 类型：字符串；
- 默认值：空；
- 允许值：空，或单个合法 IPv4/IPv6 地址；不得填写主机名、CIDR、端口或多个地址；
- 作用：指定唯一可信的直接反向代理来源。

为空时，登录限制只使用连接的 `RemoteAddr`，并忽略 `X-Forwarded-For`。配置有效地址时，
只有请求的直接连接来源与该地址匹配，后端才读取 `X-Forwarded-For` 的第一个 IP；请求头
缺失或首项不是合法 IP 时回退到 `RemoteAddr`。来自其他来源的转发头一律不可信。

可信代理必须覆盖或可靠清理客户端传入的 `X-Forwarded-For`，否则攻击者仍可能伪造来源
地址绕过单 IP 限制。IPv4 和 IPv6 使用规范化后的地址进行比较。

## 6. 相关环境变量

以下项目不写入 `clamavweb.conf`，但与本文件中的安全配置相关。

### `ADMIN_REGISTER_TOKEN`

用于用户表为空时创建首位管理员。首次注册请求必须在 JSON 请求体的 `token` 字段中提供
完全相同的非空值。后端使用固定长度摘要进行恒定时间比较，不会把该值写入数据库或配置
文件。环境变量未设置或为空时，首次管理员注册始终被拒绝。

首位管理员创建后，后续用户和管理员由已登录的 admin 管理，不再要求该环境令牌。建议使用
密码管理器生成的高熵随机值，并通过容器 secret 或等价机制注入，不要写入镜像或版本库。

### `CLAMAVWEB_CONFIG_FILE`

覆盖服务端配置文件路径，默认值为 `/config/clamavweb.conf`。

### `SCANNER_COOKIE_SECURE`

TLS 在反向代理终止时设置为 `true` 或 `1`，强制 session Cookie 带 `Secure` 属性。

## 7. 管理 API

只有 admin 可以调用 `GET /api/config` 和 `PATCH /api/config`。API 使用小写 JSON 字段：

| 配置文件键 | JSON 字段 |
|---|---|
| `HISTORY_INDEX_REFRESH_INTERVAL` | `history_index_refresh_interval` |
| `WEB_FIRSTRUN_COMPLETED` | `web_firstrun_completed`（只读，不接受 PATCH） |
| `WEB_LOGIN_MAX_TRIES` | `web_login_max_tries` |
| `WEB_LOGIN_MAX_TRIES_OVERALL` | `web_login_max_tries_overall` |
| `WEB_LOGIN_COOLDOWN_INTERVAL` | `web_login_cooldown_interval` |
| `SERVER_TRUSTED_REVERSEPROXY` | `server_trusted_reverseproxy` |

传入空字符串可清除可信反向代理。配置文件中的任何非法值会导致服务启动失败；管理 API
收到非法修改时返回 `400`，原配置保持不变。
