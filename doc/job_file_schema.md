# 扫描任务文件格式

本文记录 `/state/jobs/<job_id>.json` 的持久化格式。目录可由 `JOBS_DIR` 覆盖，默认取 `${STATUS_DIR:-/state}/jobs`。任务 JSON 的 `version` 独立于历史数据库 schema 版本，也独立于项目发布版本。

当前写入版本为 **v4**；当前读取和重建同时支持 **v3、v4**。任务文件保存历史事实，不作为内存队列、引擎状态或进程锁的依据。接口契约见 [api.md](api.md)，索引表结构见 [history_db_schema.md](history_db_schema.md)。

## 版本跨度

以下范围依据本仓库现有 Git 历史和标签，提交边界包含起点、不包含终点；“当前”指仍在使用该格式的代码线，不预设未来发布版本。

| 格式 | 生成生效跨度 | 已知发布标签 | 当前读取状态 |
|---|---|---|---|
| v3 | `dbb7d08`（2026-08-20）起，至 `3776316` 前；最后一个迁移前提交为 `67ccf79` | `v2.0.0`、`v2.0.1` | 兼容读取；不再用于新任务 |
| **v4（当前）** | `3776316`（2026-11-08）起至当前 | 当前仓库尚无包含该提交的发布标签 | 当前写入 / 读取格式 |

v3 的 owner 从用户名改为不可变的用户 ID；v2 及更早版本不在当前索引器的支持范围。v3 已完成任务不会因启动新版服务而批量改写成 v4。

## 两个版本的公共字段

以下表格描述生成器的字段约定；当前索引器实际强制校验的最小集合另见文末。

| 字段 | JSON 类型 | 含义 |
|---|---|---|
| `version` | integer | 本文件格式版本，`3` 或 `4` |
| `job_id` | string | 与文件名完全一致的任务 ID（不含 `.json`） |
| `type` | string | `manual` 或 `cron`，须与 ID 前缀一致 |
| `user_id` | string | 8 位 `[a-z0-9]`，至少一个字母和一个数字；不随用户名变化 |
| `status` | string | 任务执行状态，具体取值见各版本 |
| `target` | string | 此任务对应的一个文件或目录目标 |
| `action` | string | `warn`、`move` 或 `remove` |
| `pid` | integer / null | v3 外部扫描进程 PID；v4 为 null |
| `started_at` | integer | 创建任务记录时的 Unix 秒时间戳，严格大于 0；不一定是引擎开始扫描时间 |
| `finished_at` | integer / null | 结束时的 Unix 秒时间戳；尚未结束时为 null |
| `exit_code` | integer / null | 结果兼容码，两个版本含义有差别 |
| `result` | string | `unknown`、`clean`、`found`、`error` |
| `log_file` | string | 扫描日志路径，通常为 `/log/<job_id>.log` |
| `detection_log` | string / null | 检出日志路径，通常为 `/log/clamav_detection_<job_id>.log` |
| `message` | string | 可读的任务状态或结束说明 |

所有时间数值仍使用秒；v4 仅任务 ID 的数字部分使用纳秒。日志路径使用实际 `SCAN_LOG_DIR`，不是永远固定在 `/log`。

## v3：脚本生成格式（历史格式，当前兼容读取）

**生成跨度：`dbb7d08` ≤ 提交 < `3776316`；覆盖已知标签 `v2.0.0`、`v2.0.1`。当前后端继续读取，旧后端不识别 v4。**

### 完整示例

```json
{
  "version": 3,
  "job_id": "manual-1783433000",
  "type": "manual",
  "user_id": "a1b2c3d4",
  "status": "finished",
  "target": "/scan/docs",
  "action": "warn",
  "pid": 123,
  "started_at": 1783433000,
  "finished_at": 1783433010,
  "exit_code": 1,
  "result": "found",
  "log_file": "/log/manual-1783433000.log",
  "detection_log": "/log/clamav_detection_manual-1783433000.log",
  "message": "Threat found while scanning."
}
```

### 生成与结果约定

- 原生成器为 scan_once.sh，任务 ID 通常为 `manual-<Unix秒>` 或 `cron-<Unix秒>`，同一 ID 冲突时追加 `-001` 等三位序号；
- 状态依次可能为 `waiting`、`running`，最终为 `finished` 或 `failed`；`pid` 在实际启动 clamdscan 前为 null；
- `exit_code` 包括 clamdscan 结果码 `0`（无检出）、`1`（检出）、`2`（执行错误），也可能有脚本产生的 `74`（隔离失败）或 `75`（扫描冲突 / 等待超时）等；
- `result` 是持久化结果，不能仅根据脚本自身最终退出码推断；
- 无顶层 `detections`、`summary`、`LOCK_FILE`。检出路径和原因依赖 `detection_log` 指向的可读日志，隔离失败可能使任务成为 `failed/error`，即使已发现威胁；
- 当时的临时文件重命名、目录锁及 status.json 是旧执行链路的同步机制，不是迁移后必须保留的任务格式。

### 当前读取行为

存在 `detection_log` 时，当前后端验证它等于配置日志目录下的 `clamav_detection_<job_id>.log`，再解析日志中的源文件与原因；此行为不再仅限 `result=found`，因此有错误的历史任务也能保留已有检出。日志缺失、路径不符或文件损坏时不能完整重建该任务的检出，记录警告并保留此前有效索引。

## v4：Go 生成格式（当前在用）

**生成 / 读取跨度：`3776316`（2026-11-08）起至当前；尚未对应现有发布标签。**

### 新增字段

| 字段 | JSON 类型 | 含义 |
|---|---|---|
| `LOCK_FILE` | boolean | 是否要求扫描前尝试文件锁，默认 false，字段名为大写 |
| `detections` | array，可省略 | 直接用于索引的结构化检出；为空时生成器会省略此顶层字段 |
| `summary` | object | 扫描与处置汇总；运行中计数尚未形成最终结果 |
| `summary.scanned` | integer | 收到无错误扫描响应的文件数；不是已发现文件总数 |
| `summary.scan_errors` | integer | 扫描、打开、遍历、预检查等错误次数，不保证等于失败文件数 |
| `summary.action_succeeded` | integer | 隔离或删除成功次数；仅告警不增加此计数 |
| `summary.action_failed` | integer | 隔离或删除失败次数；不计入 scan_errors |
| `summary.detections` | array / null | 与顶层相同的检出列表，由当前生成器同时保存 |
| `summary.errors` | array / null | 错误列表，包含扫描错误及处置错误 |

`detections` 和 `summary.detections` 中每项为：

```json
{"source_file":"/scan/docs/eicar.com","detection_reason":"Eicar-Test-Signature"}
```

`summary.errors` 中每项为：

```json
{"file":"/scan/docs/unreadable.dat","stage":"open","error":"permission denied"}
```

`stage` 表示错误阶段，例如 `open`、`stat`、`traverse`、`preflight`、`connect`、`command`、`descriptor`、`response`、`scan`、`action`、`canceled` 或 `history`；它是诊断字段，不是固定数据库枚举。错误文本按实际底层错误生成。

### 完整示例：检出与错误并存

```json
{
  "version": 4,
  "job_id": "manual-1794096000000000000",
  "type": "manual",
  "user_id": "a1b2c3d4",
  "status": "finished",
  "result": "found",
  "action": "move",
  "started_at": 1794096000,
  "finished_at": 1794096010,
  "detection_log": "/log/clamav_detection_manual-1794096000000000000.log",
  "detections": [
    {"source_file":"/scan/docs/eicar.com","detection_reason":"Eicar-Test-Signature"}
  ],
  "target": "/scan/docs",
  "log_file": "/log/manual-1794096000000000000.log",
  "message": "Threat found while scanning.",
  "LOCK_FILE": true,
  "exit_code": 1,
  "pid": null,
  "summary": {
    "scanned": 1,
    "scan_errors": 1,
    "action_succeeded": 0,
    "action_failed": 1,
    "detections": [
      {"source_file":"/scan/docs/eicar.com","detection_reason":"Eicar-Test-Signature"}
    ],
    "errors": [
      {"file":"/scan/docs/unreadable.dat","stage":"open","error":"permission denied"},
      {"file":"/scan/docs/eicar.com","stage":"action","error":"quarantine move failed"}
    ]
  }
}
```

### 生成、持久化与汇总

由 HistoryStore 分配单调的 Unix 纳秒任务 ID，进程内用原子计数避免时钟回退或同纳秒重复；仍保留 `manual-` / `cron-` 前缀。它不是跨进程任务锁。

执行器组织扫描并交付格式化汇总，HistoryStore 统一生成最终字段、写任务 JSON，并即时更新索引。写入采用进程内互斥下的直接文件写入，不再依赖文件锁或任务 JSON 原子重命名；索引、读取和清理遵循同一同步边界。进程崩溃仍可能产生不完整 JSON，后续刷新会诊断它，不因此删除此前有效索引。

当前生成器开始时写入 `running/unknown`；尚未开始的内存队列项没有相应任务文件。`pid` 保留为 null，`exit_code` 为 Go 生成的兼容结果码：

| 条件 | status | result | exit_code |
|---|---|---|---|
| 有任何有效检出，包括同时有扫描或处置错误 | `finished` | `found` | `1` |
| 无检出，但有扫描或处置错误 | `failed` | `error` | `2` |
| 无检出且无错误，包括空目录 | `finished` | `clean` | `0` |

单文件出错不阻止目录后续文件扫描。加锁失败记录 warn 并继续，不增加 scan_errors。顶层检出保存在文件内，重建索引不依赖检出日志是否仍存在；当前重建器读取顶层 `detections`，不会从 `summary.detections` 补回缺失的顶层内容。

## 当前读取约束与升级

当前索引器接受且强制检查：

1. `version` 为 3 或 4；
2. `job_id` 匹配 `^(manual|cron)-[0-9]{10,19}(?:-[0-9]{3})?$`，文件名严格为 `<job_id>.json`；
3. `type` 为 manual/cron，且与 ID 前缀一致；
4. `user_id` 为合法 8 位不可变用户 ID，`started_at > 0`；
5. `action` 为 warn/move/remove；
6. v3 检出日志符合上述读取要求，v4 结构化检出可按 JSON 类型解析。

生成器字段约定比这些最小校验更完整。例如状态 / 结果的约定取值并非全部由 JSON 索引器枚举校验，不能据此认为任意第三方 JSON 已得到完整格式验证。

启动时对合法的遗留 `waiting/running` 任务写入 `failed/error`、`exit_code=2`、结束时间和中断说明，不自动恢复扫描；这也可能改写未完成的 v3 文件，但不会将其 version 改为 4。已结束的 v3 文件保持原样。回退旧后端时，旧读取器只接受 v3，不能索引新产生的 v4 文件。
