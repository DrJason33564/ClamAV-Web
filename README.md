# ClamAV Scheduled Scan Container

这个目录提供了一个基于 ClamAV 的定时扫描容器。容器启动后会初始化 ClamAV，生成或加载定时扫描配置，并通过 Debian cron 执行扫描任务。

## 文件说明

- `Dockerfile`: 构建镜像，安装 `cron`，复制入口脚本、示例配置和 `clamd.conf`。
- `startup.sh`: 容器入口，准备配置文件、启动 ClamAV 和 cron。
- `cron.sh`: 校验定时扫描配置，生成 `/etc/cron.d/clamav-scheduled-scan`，启动 cron。
- `config.sh`: 解析并校验 cron 扫描规则。
- `scan_once.sh`: 执行单次扫描，写入任务状态、扫描日志和检出日志。
- `exclude.sh`: 根据排除配置生成 ClamAV SHA256 allow-list 数据库。
- `log.sh`: 统一日志写入和日志轮转。
- `cron_scan_example.conf`: 首次启动时释放到配置目录的示例定时扫描配置。

## 构建镜像

```sh
docker build -t clamav-scheduled-scan .
```

## 推荐目录挂载

```sh
docker run -d --name clamav-scan \
  -v /host/config:/config \
  -v /host/scan:/scan \
  -v /host/quarantine:/quarantine \
  -v /host/log:/log \
  -v /host/state:/state \
  -e SCANNER_ACCOUNTS="admin:asdewq,leo:zxcdsa" \
  clamav-scheduled-scan
```

真实示例：

```
docker run -d --name clamav-scan -p 8080:8080 -v ./config:/config -v ./scan:/scan -v ./quarantine:/quarantine -v ./log:/log -v ./state:/state -e SCANNER_ACCOUNTS="admin:asdewq,leo:zxcdsa" clamav-scheduled-scan
```

首次启动如果 `/config/cron_scan.conf` 不存在，容器会复制 `cron_scan_example.conf` 到该路径后退出。请编辑配置文件后重新启动容器。

## 定时扫描配置

默认配置文件路径为 `/config/cron_scan.conf`，每条有效规则一行:

```text
分钟 小时 日期 月份 星期 扫描目标 检出后行为
```

示例:

```text
30 3 * * * /scan warn
0 */6 * * * /scan move
15 2 * * 0 "/scan/My Folder" remove
```

检出后行为:

- `warn`: 只记录告警，不修改文件。
- `move`: 将检出的文件移动到 `/quarantine`。
- `remove`: 直接删除检出的文件，请谨慎使用。

路径包含空格时必须使用英文双引号。配置文件支持空行和以 `#` 开头的注释行。

## 排除配置

默认排除配置文件路径为 `/config/exclude.conf`。每行填写一个可信文件或目录:

```text
/scan/trusted-file.zip
"/scan/Trusted Folder"
```

容器启动时会为这些文件生成 SHA256 allow-list 数据库。目录会被递归展开。不存在、不可读或格式错误的条目会被跳过并写入启动日志。

## 日志和状态

- `/log/startup.log`: 启动、配置释放、排除数据库生成等日志。
- `/log/cron.log`: cron 规则加载和 cron 启动日志。
- `/log/<job_id>.log`: 单次扫描任务日志。
- `/log/clamav_detection_<job_id>.log`: 检出详情日志。
- `/state/status.json`: 当前 ClamAV 和扫描状态。
- `/state/jobs/<job_id>.json`: 每个扫描任务的状态文件。

日志默认超过 `5242880` 字节会轮转，可通过 `SCAN_LOG_MAX_BYTES` 调整。

## 常用环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CRON_CONFIG_FILE` | `/config/cron_scan.conf` | 定时扫描配置文件 |
| `EXCLUDE_CONFIG_FILE` | `/config/exclude.conf` | 排除配置文件 |
| `SCANNER_ACCOUNTS` | 无 | WebUI 账号，格式为 `user:password`，多个账号用逗号、分号或换行分隔 |
| `SCANNER_ADDR` | `:8080` | WebUI 监听地址 |
| `SCAN_LOG_DIR` | `/log` | 日志目录 |
| `QUARANTINE_DIR` | `/quarantine` | 隔离目录 |
| `STATUS_DIR` | `/state` | 状态目录 |
| `SCAN_WAIT_INTERVAL` | `30` | 扫描锁等待间隔秒数 |
| `SCAN_WAIT_MAX_SECONDS` | `0` | 等待扫描锁的最大秒数，`0` 表示不限 |
| `CLAMD_WAIT_RETRIES` | `180` | 等待 ClamAV 就绪的重试次数 |
| `CLAMD_WAIT_INTERVAL` | `2` | 等待 ClamAV 就绪的重试间隔秒数 |

## 手动执行一次扫描

进入容器后可直接调用:

```sh
/scan_once.sh --type manual --target /scan --action warn
```

如果希望已有扫描运行时等待锁释放:

```sh
/scan_once.sh --type manual --target /scan --action move --wait
```

## 注意事项

- `move` 和 `remove` 会修改被扫描目录中的文件，请先在测试目录验证规则。
- WebUI 文件浏览和手动扫描都固定限制在 `/scan` 下。
- cron 任务会串行等待扫描锁，避免多个扫描同时运行。
- Dockerfile 会把当前目录中的 `clamd.conf` 安装到 `/etc/clamav/clamd.conf`，脚本中的 `clamdscan` 也默认使用该配置。
