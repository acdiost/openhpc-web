# OpenHPC Web

面向单集群环境的轻量 HPC 管理平台。目前仓库已完成平台登录、安全会话、可查询的 SQLite 只读审计日志、集群总览，以及 Slurm 节点、分区、作业、账户、用户、关联和 QoS 只读视图。模块导航、中英文和科研红/Slurm 蓝主题已接入；LDAP、文件和终端页面已经建立模块边界，真实系统适配器将在后续阶段接入。

CentOS 7 的完整安装、升级和故障排查步骤见 [部署指南](docs/deployment-centos7.md)。

## 快速启动

需要 Go 1.24 或更高版本。

```bash
export OPENHPC_ADMIN_USERNAME=admin
export OPENHPC_ADMIN_PASSWORD='请替换为高强度密码'
export OPENHPC_DATABASE_PATH=state/openhpc.db
go run ./cmd/server
```

浏览器访问 <http://127.0.0.1:8080>。监听地址可通过 `OPENHPC_ADDRESS` 修改。

生产环境必须通过 loopback 上的本机 TLS 反向代理访问，并设置 `OPENHPC_SECURE_COOKIES=true`；服务会拒绝监听非 loopback 地址。
反向代理还必须配置入口总速率限制，并通过 `OPENHPC_TRUSTED_PROXY_CIDRS` 指定其 CIDR（多个值用逗号分隔）；仅这些地址提供的 `X-Forwarded-For` 会被信任。
SQLite 所在目录必须由服务账户持有且权限为 `0700`；数据库、WAL 和 SHM 文件会收紧为 `0600`。

```bash
go test ./... -cover
go build ./cmd/server
```

程序允许以 root 运行，并在启动日志输出安全警告。生产环境仍推荐使用专用非特权账户；root 模式会扩大 Slurm 子进程和文件读取功能的权限范围，仅应在确有跨用户查询等需求时启用。

## 当前结构

```text
cmd/server/          服务启动、环境配置和优雅关闭
internal/platform/  SQLite 平台数据与审计
internal/web/       Echo 路由、认证、模板和静态资源
```

当前 MVP 已使用 Echo、服务端模板和 SQLite。界面资源为本地嵌入的 CSS/JavaScript；HTMX 局部刷新与 Tailwind 构建链会随首个真实 Slurm/LDAP CRUD 模块接入，避免在仅有占位页面时引入无实际用途的前端依赖。

## Slurm 只读集成

第一阶段通过管理节点本机的固定 Slurm CLI 获取 Dashboard 实时快照，不使用 SSH root，也不直连或修改 Slurm accounting 数据库。

```bash
export OPENHPC_SLURM_ENABLED=true
export OPENHPC_SLURM_BIN_DIR=/usr/local/bin
export OPENHPC_SLURM_TIMEOUT=3s
export OPENHPC_SLURM_MAX_OUTPUT=2097152
export OPENHPC_SLURM_CACHE_TTL=10s
# 默认关闭；启用前按部署指南配置最小只读目录权限
export OPENHPC_JOB_OUTPUT_ROOTS=
```

适配器只会执行：

```text
/usr/local/bin/sinfo --Node --json
/usr/local/bin/squeue --json
/usr/local/bin/sacct --json --allocations --allusers --starttime=<generated> --endtime=<generated>
/usr/local/bin/sstat --jobs=32943 --allsteps --noheader --parsable2 --format=JobID,AveCPU,AveRSS,MaxRSS,AveVMSize,MaxVMSize,TRESUsageInTot
/usr/local/bin/sacctmgr --json show account WithAssoc
/usr/local/bin/sacctmgr --json show user WithAssoc
/usr/local/bin/sacctmgr --json show qos
```

命令通过 `exec.CommandContext` 直接执行，固定 C locale，禁止 shell 和调用方自定义参数。适配器只解析页面所需字段。读取失败时保留页面和导航，但将实时数据标记为不可用。各类快照使用独立的 10 秒缓存与并发合并边界。分区容量和利用率由节点快照按分区聚合，与节点表复用同一次 `sinfo --Node --json` 缓存；分区和节点嵌入同一个“节点与分区”主页面，旧 `/slurm/partitions` 地址仅重定向到页面内分区区域。作业资源弹窗每 5 秒串行采样一次 `sstat`，服务端最多同时执行 4 个资源采样，并展示总 CPU 时间、最大 RSS、近期曲线和 step 明细。核时统计内嵌在“QoS 与核时”页面，仅支持过去 24 小时、7 天和 30 天三个固定周期；口径为 allocation 分配 CPU 数乘以窗口内墙钟占用时间，不代表实际 CPU 利用率，也不包含 GPU/TRES 计费。

作业详情中的输出预览默认关闭。配置 `OPENHPC_JOB_OUTPUT_ROOTS` 后，服务端仅接受作业 ID 与 `stdout`/`stderr` 类型，文件路径由当前 Slurm 作业元数据决定；文件必须位于允许根目录及作业工作目录内、为非符号链接普通文件，且 UID 与作业用户一致。接口只返回最新 256 KiB 纯文本。服务账号还需要对应目录的只读权限。

`sstat` 通常只允许作业所有者、root 或 SlurmUser 查询 step 数据。默认部署使用专用非特权账号，因此集群若执行 UID 校验，跨用户资源查询会返回不可用；以 root 运行可满足此类部署需求，但应评估权限扩大带来的风险。

当前兼容基线已在 CentOS 7 管理节点、Slurm 25.05.4 上验证。CentOS 7 部署模板位于 `deploy/`。构建静态 Linux 二进制：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/openhpc-web-linux-amd64 ./cmd/server
```

部署前必须由 root 创建受限目录和环境文件；systemd 会在启动程序前进入工作目录，因此不能依赖应用自行创建 `/var/lib/openhpc-web`：

```bash
install -d -o openhpc-web -g openhpc-web -m 0700 /var/lib/openhpc-web
install -d -o root -g root -m 0750 /etc/openhpc-web
install -o root -g root -m 0600 deploy/openhpc-web.env.example /etc/openhpc-web/openhpc-web.env
install -o root -g root -m 0644 deploy/openhpc-web.service /etc/systemd/system/openhpc-web.service
```

将环境文件中的管理员密码替换为高强度随机值。仅通过 SSH 端口转发进行初次访问时保留 `OPENHPC_SECURE_COOKIES=false`；接入本机 TLS 反向代理后必须改为 `true`，并设置 `OPENHPC_TRUSTED_PROXY_CIDRS`。

如需以 root 运行，systemd 覆盖配置可将 `User` 和 `Group` 设为 `root`，同时必须将状态目录调整为当前运行账户持有：

```bash
chown root:root /var/lib/openhpc-web
chmod 0700 /var/lib/openhpc-web
```

默认服务模板仍使用 `openhpc-web`，并保留 `NoNewPrivileges`、loopback 监听和目录权限检查等防护。

## 功能

- 登录
- 总览
- slurm 节点与分区只读状态
- ldap 管理
- slurm 配置文件管理
- slurm 账户与用户只读目录
- slurm 关联只读明细
- slurm QoS 只读视图
- slurm 核时管理
- slurm 作业只读详情与受限输出预览
- 系统文件管理
- 终端管理
- 独立管理平台用户（和slurm用户关联）
- 只读审计日志（保留最近 100000 条事件）

## 技术栈

单体轻量应用，默认使用专用非 root 账户，也支持在明确需要系统权限时以 root 运行；模块化组件可复用

- Golang、Echo
- HTMX
- TailwindCSS
- SQLite

## 技术系统点

- Slurm 25.05.4（当前验证版本）
- openldap
- MariaDB
- CentOS 7（当前部署目标）

## 风格

- UI：简约 科技 平面 亮色
- 多语言支持：中、英
- 主题色：科研红、slurm 蓝
- 布局：左侧抽屉菜单，顶部导航，右上角用户信息和通知等，组件功能包含使用操作说明提示，用户友好
- 体验：字体美感，近视友好
- 兼容：旧版浏览器 Chrome 83
