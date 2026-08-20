# ClamAV TimeDock 服务端配置

本文档记录 `/config/clamavweb.conf` 中各配置项的作用、默认值、允许范围和运行时行为。
配置文件使用一行一个 `KEY=VALUE` 的格式，允许空行和以 `#` 开头的注释，不允许重复键
或未识别的键。新建配置文件使用 `0600` 权限；更新时使用原子替换并保留已有权限。

## 1. 默认配置

```ini
HISTORY_INDEX_REFRESH_INTERVAL=60
CLAMAV_SLEEP_TIMER=3600
WEB_FIRSTRUN_COMPLETED=0
WEB_LOGIN_MAX_TRIES=10
WEB_LOGIN_MAX_TRIES_OVERALL=100
WEB_LOGIN_COOLDOWN_INTERVAL=600
SERVER_TRUSTED_REVERSEPROXY=
LOG_FILE_MAX_SIZE=5242880
LOG_FILE_NUM=5
LOG_LEVEL=info
```

旧配置文件未包含新增键时，服务启动会在文件末尾仅追加缺失键及其默认值，已有配置、注释、
顺序和格式保持不变。通过管理 API 保存配置时也只改写实际变化的键。

## 2. 历史索引

### `HISTORY_INDEX_REFRESH_INTERVAL`

- 类型：整数，单位为秒；
- 默认值：`60`；
- 允许范围：`5–86400`；
- 作用：控制运行中完整扫描 `/state/jobs/*.json` 并按需重建 `history.db` 索引的兜底间隔。

服务启动时无论该值为何都会先刷新一次历史索引。通过管理 API 修改后，后台索引器会停止并
排空现存周期 timer，再立即按新间隔启动 timer，无需重启。正常情况下任务文件通过文件系统
通知进行单文件增量索引；此间隔不限制该
即时更新，只负责定期校验完整目录，并在监听不可用时继续发现任务变化。

## 3. ClamAV 定时休眠

### `CLAMAV_SLEEP_TIMER`

- 类型：整数，单位为秒；
- 默认值：`3600`（一小时）；
- 允许值：`0`，或不小于 `600`；
- 作用：ClamAV 空闲达到指定时间后，调用与手动休眠接口相同的安全休眠逻辑。

配置项缺失时使用默认值；配置文件写成 `CLAMAV_SLEEP_TIMER=0` 时关闭定时休眠。管理 API
同样使用 `clamav_sleep_timer: 0` 表示关闭。空值、空字符串和 `1–599` 都会被拒绝。

Go 服务启动时开始计时。每个手动扫描进程成功启动、管理员手动唤醒 ClamAV 或通过管理 API
修改该值后，都会停止并排空旧 timer，再按最新配置重新计时。手动或定时休眠成功后计时暂停，
直到 ClamAV 再次被手动唤醒。cron 扫描不直接重置 Go 定时器，但定时器到期时仍会通过现有
跨进程扫描锁检查避免在 cron 扫描期间休眠。

定时器到期时如果手动扫描正在排队或运行，或者现有休眠逻辑发现扫描锁，当前休眠会被延后，
并按完整间隔重新计时。临时休眠错误同样会记录日志并按完整间隔重试，不会进入快速重试循环。

## 4. 首次运行

### `WEB_FIRSTRUN_COMPLETED`

- 类型：整数；
- 默认值：`0`；
- 允许值：`0`、`2`；
- `0`：首次运行尚未完成；
- `2`：首次运行已经完成。

该值由首次运行完成接口维护。用户表为空时，只有值为 `0` 才允许使用
`ADMIN_REGISTER_TOKEN` 创建首位管理员；值为 `2` 时不会因为用户表意外为空而重新开放
匿名注册。

## 5. 登录请求限制

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

## 6. 可信反向代理

### `SERVER_TRUSTED_REVERSEPROXY`

- 类型：字符串；
- 默认值：空；
- 允许值：空，或使用英文半角逗号分隔的一个或多个合法 IPv4/IPv6 地址；不得填写主机名、
  CIDR、端口或空列表项；
- 作用：指定可信的直接反向代理来源列表。

例如：

```ini
SERVER_TRUSTED_REVERSEPROXY=192.0.2.10,2001:db8::10
```

为空时，登录限制只使用连接的 `RemoteAddr`，并忽略 `X-Forwarded-For`。配置有效列表时，
只有请求的直接连接来源匹配列表中的任意地址，后端才读取 `X-Forwarded-For` 的第一个 IP；
请求头缺失或首项不是合法 IP 时回退到 `RemoteAddr`。来自其他来源的转发头一律不可信。
每项两侧空白会被移除，合法地址会规范化，并以无多余空格的逗号列表写回配置文件和 API
响应。

可信代理必须覆盖或可靠清理客户端传入的 `X-Forwarded-For`，否则攻击者仍可能伪造来源
地址绕过单 IP 限制。IPv4 和 IPv6 使用规范化后的地址进行比较。

## 7. Go 后端日志

Go 后端将结构化文本日志写入 `/log/clamavweb.log`。该文件与 Shell 的 `startup.log`、扫描
日志和检出日志相互独立。轮转文件依次命名为 `clamavweb.log.1`、`clamavweb.log.2` 等。

### `LOG_FILE_MAX_SIZE`

- 类型：正整数，单位为字节；
- 默认值：`5242880`（5 MiB）；
- 允许范围：`65536–1073741824`；
- 作用：写入下一条完整日志前，若预计超过该大小则执行轮转。单条日志本身不会被截断。

### `LOG_FILE_NUM`

- 类型：正整数；
- 默认值：`5`；
- 允许范围：`1–100`；
- 作用：限制日志文件总数，包含当前的 `clamavweb.log`。设为 `1` 时不保留历史文件。

### `LOG_LEVEL`

- 类型：字符串；
- 默认值：`info`；
- 允许值：`debug`、`info`、`warn`、`error`；
- 作用：设置最低写入等级。例如 `info` 会写入 info、warn 和 error。

以上三项只在 Go 服务启动时读取，不通过管理 API 暴露，也不支持热加载。修改时应停止
服务、编辑配置并重新启动。服务运行期间手工修改后又通过管理 API 保存其他配置，内存中的
旧日志配置会被写回文件，因此不支持这种操作顺序。

HTTP 的成功 GET 请求（包括状态、列表轮询和静态资源）记录为 debug；状态变更请求记录为
info；认证拒绝记录为 warn；服务端错误记录为 error。日志保留服务端采纳的 `client_ip` 和
直接连接来源 `peer_ip`，不对 IP 脱敏。请求体、Cookie 和 Authorization 原文不会写入。
显式标记的秘钥及密码、token、secret 等敏感字段会统一脱敏：长度大于八个字符时保留首尾
各四个字符，中间替换为八个星号；更短的非空值完全替换为八个星号。

## 8. 相关环境变量

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

## 9. 管理 API

只有 admin 可以调用 `GET /api/config` 和 `PATCH /api/config`。API 使用小写 JSON 字段：

| 配置文件键 | JSON 字段 |
|---|---|
| `HISTORY_INDEX_REFRESH_INTERVAL` | `history_index_refresh_interval` |
| `CLAMAV_SLEEP_TIMER` | `clamav_sleep_timer`（`0` 表示关闭） |
| `WEB_FIRSTRUN_COMPLETED` | `web_firstrun_completed`（只读，不接受 PATCH） |
| `WEB_LOGIN_MAX_TRIES` | `web_login_max_tries` |
| `WEB_LOGIN_MAX_TRIES_OVERALL` | `web_login_max_tries_overall` |
| `WEB_LOGIN_COOLDOWN_INTERVAL` | `web_login_cooldown_interval` |
| `SERVER_TRUSTED_REVERSEPROXY` | `server_trusted_reverseproxy` |

日志配置没有对应 JSON 字段；管理 API 更新其他键时会原样保留文件中的日志配置。

传入空字符串可清除可信反向代理。配置文件中的任何非法值会导致服务启动失败；管理 API
收到非法修改时返回 `400`，原配置保持不变。
