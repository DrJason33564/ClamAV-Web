# 配置文件与关联持久化文件

本文记录后端运行配置、定时规则、信任区、ClamAV 配置和关联恢复文件。配置格式以提交 `3776316` 起的当前实现为准；这些配置文件均没有项目级 schema version 字段，不应与任务文件 v4、历史数据库 schema v3 混用。

HTTP 请求与响应见 [api.md](api.md)；任务 JSON 见 [job_file_schema.md](job_file_schema.md)；历史数据库见 [history_db_schema.md](history_db_schema.md)。前端和构建工具的 tsconfig、package.json 等开发配置不属于容器运行配置。

| 文件 | 镜像来源 / 初始化 | 运行位置 | 管理方 |
|---|---|---|---|
| 服务配置 | `config/clamavweb.conf` → `/clamavweb_example.conf`，缺失时复制 | `/config/clamavweb.conf` | Go |
| 定时扫描规则 | `cron_scan_example.conf` → `/cron_scan_example.conf`，缺失时复制 | `/config/cron_scan.conf` | Go |
| 信任区条目 | 缺失时创建空文件 | `/config/exclude.conf` | Go + exclude.sh |
| 引擎配置 | `clamd.conf` 构建时安装 | `/etc/clamav/clamd.conf` | ClamAV / 上游 init |
| 更新器配置 | 上游镜像提供，仓库没有独立模板 | `/etc/clamav/freshclam.conf` | 上游 init / freshclam |
| 生成的信任签名库 | exclude.sh 调用 sigtool 生成 | `/var/lib/clamav/excluded.sfp` | exclude.sh |

## 服务配置文件的语法

以下第 1–7 节均对应 `/config/clamavweb.conf`，示例见 [默认配置](../config/clamavweb.conf)。
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
LOG_LEVEL=warn
```

旧配置文件未包含新增键时，服务启动会在文件末尾仅追加缺失键及其默认值，已有配置、注释、
顺序和格式保持不变。通过管理 API 保存配置时也只改写实际变化的键。

## 2. 历史索引

### `HISTORY_INDEX_REFRESH_INTERVAL`

- 类型：整数，单位为秒；
- 默认值：`60`；
- 允许范围：`5–86400`；
- 作用：控制运行中完整扫描 `/state/jobs/*.json` 并按需重建 `history.db` 索引的兜底间隔。

此间隔控制周期兜底刷新；任务写入后另有即时索引，不受该间隔限制。修改后重置计时，无需重启；完整机制见 [历史数据库说明](history_db_schema.md#索引生成与重建)。

## 3. ClamAV 定时休眠

### `CLAMAV_SLEEP_TIMER`

- 类型：整数，单位为秒；
- 默认值：`3600`（一小时）；
- 允许值：`0`，或 `600–9223372036`（秒）；
- 作用：ClamAV 空闲达到指定时间后，调用与手动休眠接口相同的安全休眠逻辑。

配置项缺失时使用默认值；配置文件写成 `CLAMAV_SLEEP_TIMER=0` 时关闭定时休眠。管理 API
同样使用 `clamav_sleep_timer: 0` 表示关闭。空值、空字符串和 `1–599` 都会被拒绝。

Go 服务启动时开始计时。手动或定时扫描开始、完成、管理员唤醒以及配置修改均重置 timer。休眠成功后暂停计时。有等待或活动任务时延后休眠，临时错误按完整间隔重试。状态和互斥由 Go 内存维护，不使用跨进程扫描锁。

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

Go 后端将结构化诊断日志写入 `/log/clamavweb.log`；任务扫描和检出记录写入各自文件。`LOG_FILE_MAX_SIZE` 与 `LOG_FILE_NUM` 分别作用于这些 Go 日志文件，`LOG_LEVEL` 只过滤诊断日志，不过滤必要扫描记录。Shell 的 `startup.log` 仍由 log.sh 管理。轮转文件命名为原路径追加 `.1`、`.2` 等。

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
- 默认值：`warn`；
- 允许值：`debug`、`info`、`warn`、`error`；
- 作用：设置最低写入等级。例如 `info` 会写入 info、warn 和 error。

以上三项只在 Go 服务启动时读取，不通过管理 API 暴露，也不支持热加载。修改时应停止服务、编辑配置并重新启动；不要把通过 API 更新其他配置当作日志配置重新加载。

HTTP 的成功 GET 请求（包括状态、列表轮询和静态资源）记录为 debug；状态变更请求记录为
info；认证拒绝记录为 warn；服务端错误记录为 error。日志保留服务端采纳的 `client_ip` 和
直接连接来源 `peer_ip`，不对 IP 脱敏。请求体、Cookie 和 Authorization 原文不会写入。
显式标记的秘钥及密码、token、secret 等敏感字段会统一脱敏：长度大于八个字符时保留首尾
各四个字符，中间替换为八个星号；更短的非空值完全替换为八个星号。

## 8. 相关环境变量

以下项目不写入 `clamavweb.conf`。除显式说明外，在启动或脚本执行时读取；路径变量应在 Go 与保留脚本之间保持一致。

### `SCANNER_ADDR` 与 `SCANNER_PORT`

`SCANNER_ADDR` 默认为空，空值（含纯空白）或小写字符串 `any` 使用通配监听，
沿用 Go TCP 的全部地址监听行为；实际 IPv4/IPv6 可用性取决于容器网络环境。
也可填写单个 IP，或用半角逗号分隔多个 IP，每项两侧空白会被移除，重复 IP 会规范化去重。
IPv6 无需添加方括号。不支持主机名、CIDR、附带端口或 zone 的地址以及空列表项。

```sh
SCANNER_ADDR=192.168.1.10,192.168.1.11,2001:db8::10
SCANNER_PORT=8080
```

所有地址共用 `SCANNER_PORT`，默认端口为 `8080`，仅填写不带冒号的端口数字。
`any`、`0.0.0.0`、`::` 只能单独使用，不允许与其他地址混用。
指定 IP 必须存在于容器的网络命名空间中。这两个环境变量只在启动时读取，不写入配置文件。

启动时全部地址绑定成功后才提供 HTTP 服务；任一绑定失败都会关闭已绑定的监听并退出。
运行期间任一 listener 异常退出也会触发统一关闭。绑定失败和运行异常均以 error 等级写入
Go 服务日志，同时输出到容器标准输出，包含相关地址和原因。正常关闭不作为监听异常记录。
退出时最多等待 5 秒供请求完成；超时后同样向两处记录 error，并强制关闭连接。

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

### 运行路径与扫描设置

| 环境变量 | 默认值 | 含义 |
|---|---|---|
| `IS_TIMEDOCK` | `N` | 仅允许 `Y`、`N` 或空；`Y` 启用账户目录限制 |
| `TZ` | 上游镜像默认值 | Go `time.Local` 和保留脚本的时区，例如 `Asia/Shanghai` |
| `DATA_DIR` | `/data` | 默认数据库父目录 |
| `USER_DATABASE_FILE` | `${DATA_DIR}/users.db` | 用户与 session 数据库；不由历史任务文件重建 |
| `HISTORY_DATABASE_FILE` | `${DATA_DIR}/history.db` | 历史索引数据库 |
| `STATUS_DIR` | `/state` | 默认任务目录和上游 init PID 记录的父目录；不是运行状态真源 |
| `JOBS_DIR` | `${STATUS_DIR}/jobs` | 任务 JSON 目录 |
| `SCAN_LOG_DIR` | `/log` | Go 日志及 startup.sh 默认日志目录 |
| `QUARANTINE_DIR` | `/quarantine` | 隔离文件及恢复记录目录 |
| `CRON_CONFIG_FILE` | `/config/cron_scan.conf` | 定时规则文件 |
| `EXCLUDE_CONFIG_FILE` | `/config/exclude.conf` | 信任区条目文件 |
| `CLAMD_SOCKET` | `/tmp/clamd.sock` | Go 客户端连接的 Unix socket，须与 clamd 的 `LocalSocket` 一致 |
| `CLAMD_CONF` | `/etc/clamav/clamd.conf` | Go 配置对象保留此路径；startup.sh 的探测仍固定使用 `/etc/clamav/clamd.conf`，不可只改此变量期待切换整套配置 |
| `SCAN_WAIT_MAX_SECONDS` | `0` | 定时任务最大排队时间，单位秒；`0` 无限制。当前实现按 duration 秒字符串解析，非法值或负数回退为 `0` |

扫描根目录固定为 `/scan`；Go 单文件扫描超时为 15 分钟、控制命令默认 3 秒、唤醒超时为 21 分钟，当前没有对应的运行时配置键。`CLAMAV_SLEEP_TIMER` 是配置文件键，不通过同名环境变量覆盖。

旧 `SCAN_WAIT_INTERVAL`、`SCAN_LOCK_DIR`、`STATUS_FILE`、`SCAN_ONCE_SCRIPT`、`CRON_SCRIPT` 不再被 Go 扫描链路使用。

### 保留脚本的配置

| 环境变量 | 默认值 | 读取方 / 说明 |
|---|---|---|
| `STARTUP_SCRIPT` | `/startup.sh` | Go 唤醒入口，只传 `--wake` |
| `EXCLUDE_SCRIPT` | `/exclude.sh` | Go 与 startup.sh 使用的信任签名库生成入口 |
| `LOG_SCRIPT` | `/log.sh` | startup.sh、exclude.sh 的日志工具 |
| `CLAMAV_INIT` | `/init` | startup.sh 启动的上游初始化程序 |
| `SCANNER_CMD` | `/server` | startup.sh 启动的 Go 服务 |
| `STOP_GRACE_SECONDS` | `5` | startup.sh 停止子进程后的等待秒数 |
| `CLAMAV_INIT_PID_FILE` | `${STATUS_DIR}/clamav-init.pid` | startup.sh 记录 / 接管上游 init 的 PID |
| `SLEEP_LOCK_DIR` | `${STATUS_DIR}/sleep.lock` | startup.sh 仅用于清理旧休眠目录；Go 不读取它判断休眠 |
| `CRON_SCAN_EXAMPLE` | `/cron_scan_example.conf` | 首次启动释放定时规则的模板 |
| `CLAMAVWEB_CONFIG_EXAMPLE` | `/clamavweb_example.conf` | 首次启动释放服务配置的模板 |
| `STARTUP_LOG_FILE` | `/log/startup.log` | 启动 / 信任库生成日志，可覆盖完整路径 |
| `STARTUP_LOG_DIR` | `/log` | exclude.sh 单独调用时的默认日志父目录；startup.sh 则使用 `SCAN_LOG_DIR` |
| `SCAN_LOG_MAX_BYTES` | `5242880` | log.sh 的轮转阈值，不控制 Go 日志 |
| `EXCLUDED_DB` | `/var/lib/clamav/excluded.sfp` | exclude.sh 的生成目标，应位于 clamd 的签名目录 |

startup.sh 在脚本内固定设置 `CLAMD_WAIT_RETRIES=240` 和 `CLAMD_WAIT_INTERVAL=5`，不能用同名环境变量覆盖。它们与 Go 侧 21 分钟唤醒超时配套。

## 9. 定时扫描规则 cron_scan.conf

当前格式无版本字段。仓库示例见 [cron_scan_example.conf](../cron_scan_example.conf)；当前 Go 调度行为自 `3776316` 起生效。

推荐每条规则使用两行：

```text
# scanner-cron-rule id=abc123def456gh78 enabled=true
30 3 * * * "/scan/Team A" move a1b2c3d4 Y
```

规则列顺序固定为：

```text
minute hour day month weekday target action owner_user_id wake
```

| 字段 | 格式 / 取值 |
|---|---|
| 元数据 `id` | 16 位小写英文字母或数字，文件内唯一 |
| 元数据 `enabled` | `true` 或 `false` |
| `minute` | `0–59` |
| `hour` | `0–23` |
| `day` | `1–31` |
| `month` | `1–12` |
| `weekday` | `0–7`，`0` 和 `7` 均为星期日 |
| `target` | 绝对路径；API 管理时限制在当前用户可访问的 `/scan` 范围内 |
| `action` | `warn`、`move`、`remove` |
| `owner_user_id` | 不可变的用户 ID：8 位 `[a-z0-9]`，至少一个字母和一个数字 |
| `wake` | `Y` 或 `N`，文件中不是 JSON 布尔值 |

时间字段只支持数字、`*`、升序闭区间、正整数步长和逗号列表，例如 `*/5`、`0-23/2`、`1,6,12`；不接受秒字段、名称或 `@daily` 等描述符。路径包含空格必须用双引号包裹，不支持路径中的双引号、换行和 NUL。非空非注释行必须为合法规则，不支持行尾注释。

停用规则保留元数据，并注释规则行：

```text
# scanner-cron-rule id=abc123def456gh78 enabled=false
# 30 3 * * * "/scan/Team A" move a1b2c3d4 Y
```

空行和普通 `#` 注释会保留。当前读取器还接受缺少元数据的有效规则行，在内存中补出元数据，后续保存时写入；建议通过管理接口保存，使 ID 稳定。

保存先验证完整文件，随后原子替换并通知 Go 调度器热更新。reload 失败保留此前生效的调度。运行期使用 `robfig/cron/v3`，不再生成 `/etc/cron.d` 文件：

- 使用容器时区；日期与星期均受限时按 OR 匹配；
- 不存在的夏令时分钟跳过，重复的实际分钟分别触发；
- 停机期间不补跑，同一规则按实际分钟去重；
- 每次触发进入统一内存队列；在有等待任务的用户中随机选择一个用户，再执行其队首任务。`Y` 允许调用 startup.sh --wake，`N` 在休眠时生成失败历史；
- 规则修改、禁用或删除后，已派发但未接受的旧回调会重新校验；已入队任务保留其提交时的扫描参数。

## 10. 信任区条目 exclude.conf

当前格式无版本字段。默认 `/config/exclude.conf`，示例见 [config/exclude.conf](../config/exclude.conf)。每个有效行只有路径和 owner 两列：

```text
/scan/trusted.dat a1b2c3d4
"/scan/Team A/trusted file.dat" a1b2c3d4
```

允许空行和整行 `#` 注释。owner 采用相同的 8 位不可变用户 ID。带空格的路径用双引号包裹，不支持嵌入双引号和行尾附加列；它不是 shell 脚本，不展开环境变量或转义表达式。

Go 保存条目时验证当前用户路径权限。文件可为普通文件或目录；exclude.sh 对目录递归生成普通文件的 SHA-256 信任签名，缺失目标跳过并记录 warn，非法条目也跳过并记录 warn。Go 写条目时保留原有注释并原子替换文件。存在排队或活动扫描、或者引擎休眠时，常规信任区更新会被拒绝。

`exclude.conf` 按 owner 管理，但生成的 allow-list 加载到共享 clamd 后对所有扫描全局生效。文件内容变动后，需要重新生成签名才能信任新内容。

## 11. 扫描引擎 clamd.conf

当前配置采用 ClamAV 的 `Directive Value` 文本语法，完整模板见 [clamd.conf](../clamd.conf)。`#` 开头为注释；这些选项由 ClamAV 自身解析，不使用 `KEY=VALUE` 或项目 schema version。

Docker 构建将仓库模板安装到 `/etc/clamav/clamd.conf`。它没有自动从 `/config` 释放的独立持久化副本。上游 `/init` 支持 `CLAMD_CONF_<Directive>` 形式的覆盖；具体覆盖行为取决于使用的上游镜像，不能把它视为 Go 配置解析器的一部分。

当前仓库显式启用的指令如下，未启用指令及其上游默认值以完整模板和实际 ClamAV 版本为准：

| 指令 | 仓库值 |
|---|---|
| `LogFile` | `/log/clamav-default.log` |
| `LogFileMaxSize` | `5M` |
| `LogTime`、`LogRotate`、`ExtendedDetectionInfo` | `yes` |
| `LocalSocket` | `/tmp/clamd.sock` |
| `MaxThreads` | `1` |
| `MaxQueue` | `20` |
| `MaxDirectoryRecursion` | `20` |
| `FollowDirectorySymlinks`、`FollowFileSymlinks` | `no` |
| `ConcurrentDatabaseReload` | `no` |
| `ExitOnOOM` | `yes` |
| `AlertEncryptedArchive` | `no` |
| `ScanArchive` | `yes` |
| `MaxScanTime` | `600000`（毫秒） |
| `MaxScanSize` | `1G` |
| `MaxFileSize` | `2G` |
| `MaxRecursion` | `10` |

`LocalSocket` 必须与 `CLAMD_SOCKET` 对齐。Go 的文件描述符提交、遍历限制和控制超时独立于 ClamAV 配置；修改引擎配置不会自动改变这些 Go 常量。

## 12. freshclam.conf 与生成的 excluded.sfp

### 病毒库更新配置

`/etc/clamav/freshclam.conf` 由上游镜像提供，使用与 ClamAV 相同的指令式文本语法。本仓库不维护其完整默认内容。上游 `/init` 支持 `FRESHCLAM_CONF_<Directive>` 覆盖；`CLAMAV_NO_FRESHCLAMD`、`FRESHCLAM_CHECKS` 等也是上游启动参数，应以所用镜像为准。

Go 扫描迁移不接管 freshclam。应保持更新器和 clamd 使用同一签名目录；已有上游配置的含义以实际容器内文件为准。

### SHA-256 信任签名库

`/var/lib/clamav/excluded.sfp` 是生成产物，不是用户维护的条目配置。exclude.sh 使用 `sigtool --sha256` 生成记录，按行去重排序后写入，典型记录形如：

```text
SHA256:文件字节数:文件名
```

内容权限为 `0644`；没有有效签名时删除生成文件，而不是保留空 `.sfp`，因为 ClamAV 会将空本地签名库视为无效。改变 `EXCLUDED_DB` 时，需自行保证它仍位于引擎加载的目录中。

## 13. 隔离元数据 .rec

`.rec` 是隔离文件的恢复记录，不是运行配置；在此记录其文件语法，避免把持久化内部格式写入 HTTP API 文档。它没有独立 version 字段。

### 当前写入格式：JSON（3776316 起）

`/quarantine/<name>.rec` 对应 `/quarantine/<name>`：

```json
{"source_file":"/scan/Team A/eicar.com","user_id":"a1b2c3d4"}
```

`source_file` 为原始非空路径，`user_id` 为合法的不可变用户 ID。JSON 支持原路径内的引号和换行。元数据只有在文件成功移动后才发布；同名、`.rec` 保留名和目标文件系统 NAME_MAX 均参与分配检查。跨文件系统复制完成后才删除源文件，发布元数据失败会尝试无覆盖回滚；无法回滚时尽量保留仅存副本与恢复记录。

### 兼容读取格式：带 owner 的引号文本

生成跨度为 `dbb7d08` 起至 `3776316` 前；迁移后继续兼容读取：

```text
"/scan/Team A/eicar.com" a1b2c3d4
```

该格式没有通用引号转义能力。早期仅有原始路径、没有合法 owner 的记录不能确定归属，不会被自动分配给管理员或列入普通隔离区查询。空 `.rec` 也不是有效记录；旧脚本用其作名称预留，发布完成前不会作为正常隔离条目显示。

## 14. 持久化目录与升级边界

| 位置 | 内容 / 格式 |
|---|---|
| `/data/users.db` | 用户与 session 数据库，当前 schema 2；独立于历史数据库 |
| `/data/history.db` | [历史数据库 schema 3](history_db_schema.md) |
| `/config` | 服务配置、定时规则和信任区条目 |
| `/state/jobs` | [v3/v4 任务文件](job_file_schema.md) |
| `/state/clamav-init.pid` | 保留脚本使用的十进制 PID 文本；不是 Go 引擎状态 |
| `/log` | Go、ClamAV 与保留脚本的日志 |
| `/quarantine` | 隔离文件与 `.rec` 恢复记录 |

`/data`、`/config`、`/state`、`/log`、`/quarantine` 及扫描目标应按部署需要持久化。历史索引可从任务文件重建，但用户和 session 不可由任务文件恢复；owner ID 必须与用户库保持一致。

早期的用户名 owner、任务文件 v2 及相应数据库不能自动转换为不可变用户 ID。遇到这类开发数据，应先备份并明确归属后再决定如何重建，当前后端不会自动认领。已有历史数据库 schema 2 则支持在启动时升级至 schema 3，详见 [数据库升级规则](history_db_schema.md#初始化与升级)。
