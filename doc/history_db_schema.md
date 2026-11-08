# 历史数据库结构

本文记录 `/data/history.db` 的表、约束和索引维护方式，路径可由 `HISTORY_DATABASE_FILE` 覆盖。当前在用版本为 **历史数据库 schema v3**；它与 [任务 JSON v3/v4](job_file_schema.md)、项目发布版本是三个独立的编号体系。

用户数据库 `/data/users.db` 是另一个 SQLite 文件，当前 schema 2，不包含在本文件的 v3 表清单中。HTTP 查询和管理契约见 [api.md](api.md)，运行路径与刷新配置见 [config.md](config.md)。

## schema v3：当前在用版本

- 生效提交跨度：`35b7881`（2026-08-24，增加结构化检出索引）起至当前；
- 已知发布跨度：首次包含于 `v2.0.1`，后续当前代码继续使用；尚未定义替代它的 schema 版本；
- `3776316` 将新任务文件升级至 v4，但没有升级数据库 schema；
- 当前服务接受 schema 3；已有 schema 2 可在启动时升级，其他历史数据库版本由升级逻辑拒绝。

### schema_migrations

| 列 | SQLite 类型 | SQL 约束 | 含义 |
|---|---|---|---|
| `version` | `INTEGER` | `PRIMARY KEY` | 已应用的历史数据库迁移编号 |
| `applied_at` | `INTEGER` | `NOT NULL` | 迁移时间，Unix 秒 |

通过 `MAX(version)` 判断当前版本，空表视为版本 0。新库直接写入版本 3；由版本 2 升级的库可同时保留版本 2、3 的迁移记录。

### history_jobs

| 列 | SQLite 类型 | SQL 约束 | 含义 |
|---|---|---|---|
| `job_id` | `TEXT` | `PRIMARY KEY` | 任务唯一标识；SQL 未额外声明 NOT NULL，应用写入要求非空 |
| `job_type` | `TEXT` | `NOT NULL`、`CHECK IN ('manual','cron')` | 手动 / 定时任务 |
| `status` | `TEXT` | `NOT NULL` | 任务状态，例如 running/finished/failed |
| `result` | `TEXT` | `NOT NULL` | unknown/clean/found/error 等持久化结果 |
| `action` | `TEXT` | `NOT NULL` | warn/move/remove，合法值由文件读取层校验 |
| `started_at` | `INTEGER` | `NOT NULL` | 开始记录时间，Unix 秒 |
| `finished_at` | `INTEGER` | 可为 NULL | 结束时间，Unix 秒 |
| `user_id` | `TEXT` | `NOT NULL` | 不可变的用户 ID |
| `json_file` | `TEXT` | `NOT NULL UNIQUE` | 任务文件路径，用于定位、重建和删除协调 |
| `file_mtime_ns` | `INTEGER` | `NOT NULL` | 最近成功索引的任务文件 mtime，Unix 纳秒 |
| `indexed_at` | `INTEGER` | `NOT NULL` | 最近索引时间，Unix 秒 |

复合索引 `history_jobs_user_started(user_id, started_at DESC, job_id DESC)` 支持按用户读取、按时间及任务 ID 倒序排序。`job_id` 主键及 `json_file` 唯一约束还会形成 SQLite 的相应内部索引，不需要手工添加重复索引。

`status`、`result` 和 `action` 在 SQL 中没有枚举 CHECK，不能将应用层约定误写成数据库约束。`user_id` 不存在跨文件的 users 外键；所有权由应用校验，普通历史查询始终按当前用户过滤。

任务文件中的 target、message、pid、exit_code、LOCK_FILE、summary 和日志内容都不存入此表；表内的任务结果用于列表和统计，详细事实仍在任务文件或日志中。

### history_detections

| 列 | SQLite 类型 | SQL 约束 | 含义 |
|---|---|---|---|
| `job_id` | `TEXT` | `NOT NULL`、外键指向 history_jobs(job_id)，`ON DELETE CASCADE` | 所属任务 |
| `sequence` | `INTEGER` | `NOT NULL`、`CHECK(sequence >= 0)` | 在该任务检出列表中的 0 起始序号 |
| `source_file` | `TEXT` | `NOT NULL` | 原始检出文件路径 |
| `detection_reason` | `TEXT` | `NOT NULL` | 威胁 / 检出原因 |

主键为 `(job_id, sequence)`；不按路径去重，同一路径的不同检出记录可以分别保存。该表不保存处置成功 / 失败信息，处置计数及错误详见任务文件 summary。

每次重新索引一个任务时，在同一事务内 upsert history_jobs、删除该任务旧检出、插入新检出。删除父任务时，启用的外键约束会级联删除对应检出。

### 完整建表 SQL

下列定义与 `server/database.go` 中 schema v3 的表及显式索引一致：

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS history_jobs (
    job_id TEXT PRIMARY KEY,
    job_type TEXT NOT NULL CHECK(job_type IN ('manual','cron')),
    status TEXT NOT NULL,
    result TEXT NOT NULL,
    action TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    user_id TEXT NOT NULL,
    json_file TEXT NOT NULL UNIQUE,
    file_mtime_ns INTEGER NOT NULL,
    indexed_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS history_jobs_user_started ON history_jobs(user_id, started_at DESC, job_id DESC);
CREATE TABLE IF NOT EXISTS history_detections (
    job_id TEXT NOT NULL REFERENCES history_jobs(job_id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK(sequence >= 0),
    source_file TEXT NOT NULL,
    detection_reason TEXT NOT NULL,
    PRIMARY KEY(job_id, sequence)
);
```

建表后由初始化 / 迁移逻辑记录版本，使用 `INSERT ... version=3`；该记录不是业务任务，也不受 owner 过滤。

## 初始化与升级

`server/database.go` 负责创建基础表和连接设置，HistoryStore 的升级逻辑负责已存在数据库的迁移：

| 检测版本 | 行为 |
|---|---|
| 空的新库 / 版本 0 | 初始化三张表与显式索引，直接记录 schema 3 |
| schema 2 | 创建 history_detections，强制读取可用任务文件补全索引；不执行缺失文件清除，随后记录版本 3 |
| schema 3 | 沿用当前结构；正常启动执行索引检查和未完成任务恢复 |
| 其他版本 | 拒绝启动并报告不支持的历史数据库版本 |

升级保留已有 schema_migrations 记录，不把历史 owner 从用户名自动转换为用户 ID。初始化还要求 history_jobs.user_id 的声明类型为 TEXT，不符合时提示数据库不兼容；并非完整检查或修复每个已存在列和约束。

连接以 WAL 模式运行，启用 `foreign_keys=ON`、`busy_timeout=5000`、`synchronous=NORMAL`，每个数据库连接池最多 4 个连接。foreign_keys 与 busy_timeout 同时配置进 DSN，以应用到每个池连接。

## 索引生成与重建

### 即时索引

当前链路为 ScanExecutor 形成扫描汇总，HistoryStore 写任务 JSON 后直接索引该任务。新任务初次 running 记录和最终结果都可以进入索引。文件写入、索引和历史清理由同一进程内同步边界协调，不使用 fsnotify。

v4 检出从任务文件顶层 detections 读取；v3 从其 detection_log 对应日志解析。日志不完整会影响 v3 重建能力，但不影响有效 v4 文件内的检出。只有合法且具备 owner 的任务文件才能新增或更新索引，最低校验见 [任务文件读取约束](job_file_schema.md#当前读取约束与升级)。

### 周期检查

`HISTORY_INDEX_REFRESH_INTERVAL` 控制周期完整目录枚举：批量读取数据库已记录的 mtime，跳过 mtime 未变化的任务文件，重读变化文件；目录中确实已不存在的任务会从索引删除。目录枚举失败时停止本次刷新，不把不可读取的整个目录当成空目录清除索引。

单个损坏 / 非法文件会被记录并跳过，其上次有效索引保留。与原 fsnotify 链路不同，文件损坏不再直接触发索引删除。

### 手动全量重建

管理员接口触发全局重建，忽略 mtime，遍历所有 owner 的任务文件。它与即时索引、周期检查和清理共享 HistoryStore 的互斥，在一个数据库事务内提交：

- 有效任务及其检出全部重新索引；
- 损坏文件记录警告，保留其上次有效索引；
- 完整枚举后，清除确认不存在的文件对应索引；
- 数据库错误或请求取消时回滚，避免发布半套更新。

文件系统和 SQLite 不是一个跨资源事务：进程崩溃、任务写入成功但数据库更新失败等情况，仍需周期检查或手动重建修复。任务文件也是持久化事实，因此应和索引库一起保留。

## 兼容性边界

当前 schema 3 同时索引任务 JSON v3、v4，两者都使用相同的 history_jobs/history_detections 列。任务 v4 新增结构化字段不等于数据库升为 schema 4。

若仅 history.db 损坏或丢失，可由合法任务文件重新建立；users.db 中的用户 ID、密码和 session 无法由历史文件恢复。旧版本任务文件不被更早读取器支持时，不能通过单独降低数据库版本号实现兼容回退。
