# CentOS 7 部署指南

本文档用于将 OpenHPC Web 部署到 Slurm 管理节点。当前验证环境为 CentOS 7、Slurm 25.05.4，服务监听 loopback 地址，并通过固定的 `sinfo`、`squeue` 命令读取集群快照。

## 1. 部署原则

- Web 服务使用专用的非 root 账户 `openhpc-web`。
- Web 服务不通过 SSH root 调用 Slurm，也不直接修改 Slurm 数据库。
- `/etc/openhpc-web/openhpc-web.env` 必须为 `root:root 0600`。
- `/var/lib/openhpc-web` 必须为 `openhpc-web:openhpc-web 0700`。
- 首次访问使用 SSH 端口转发；公网或局域网发布前必须增加 TLS 反向代理。

## 2. 验证服务账户

在管理节点上执行：

```bash
id openhpc-web

runuser -u openhpc-web -- /usr/local/bin/sinfo \
  --noheader --Node '--format=%N|%T|%C'

runuser -u openhpc-web -- /usr/local/bin/squeue \
  --noheader '--format=%T'
```

两个 Slurm 命令都必须成功。Web 进程不需要 root、sudo 或 MariaDB 写权限。

## 3. 构建与上传

在开发机仓库根目录构建：

```bash
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' \
  -o dist/openhpc-web-linux-amd64 ./cmd/server

shasum -a 256 dist/openhpc-web-linux-amd64
```

上传二进制和部署模板：

```bash
scp dist/openhpc-web-linux-amd64 \
  deploy/openhpc-web.service \
  deploy/openhpc-web.env.example \
  root@10.80.4.30:/tmp/
```

## 4. 安装文件

在管理节点上执行：

```bash
install -d -o openhpc-web -g openhpc-web -m 0700 /var/lib/openhpc-web
install -d -o root -g root -m 0750 /etc/openhpc-web

install -o root -g root -m 0755 \
  /tmp/openhpc-web-linux-amd64 \
  /usr/local/sbin/openhpc-web

install -o root -g root -m 0600 \
  /tmp/openhpc-web.env.example \
  /etc/openhpc-web/openhpc-web.env

install -o root -g root -m 0644 \
  /tmp/openhpc-web.service \
  /etc/systemd/system/openhpc-web.service
```

## 5. 配置环境文件

生成至少 12 个字符的随机密码。建议由密码管理器保存该密码：

```bash
openssl rand -base64 24
vi /etc/openhpc-web/openhpc-web.env
```

环境文件至少应包含：

```text
OPENHPC_ADMIN_USERNAME=admin
OPENHPC_ADMIN_PASSWORD=REPLACE_WITH_A_LONG_RANDOM_PASSWORD
OPENHPC_DATABASE_PATH=/var/lib/openhpc-web/openhpc.db
OPENHPC_ADDRESS=127.0.0.1:18080
OPENHPC_SECURE_COOKIES=false
OPENHPC_TRUSTED_PROXY_CIDRS=

OPENHPC_SLURM_ENABLED=true
OPENHPC_SLURM_BIN_DIR=/usr/local/bin
OPENHPC_SLURM_TIMEOUT=3s
OPENHPC_SLURM_MAX_OUTPUT=2097152
OPENHPC_SLURM_CACHE_TTL=10s
```

必须把 `REPLACE_WITH_A_LONG_RANDOM_PASSWORD` 替换为真实密码。用户名长度为 1 到 64，密码长度至少为 12。不要在聊天记录、命令历史或仓库中保存真实密码。

再次固定权限：

```bash
chown root:root /etc/openhpc-web/openhpc-web.env
chmod 0600 /etc/openhpc-web/openhpc-web.env
```

## 6. 启动与检查

```bash
systemctl daemon-reload
systemctl enable --now openhpc-web
systemctl status openhpc-web --no-pager
journalctl -u openhpc-web -n 50 --no-pager
ss -lntp | grep 18080
```

预期状态为 `active (running)`，监听地址为 `127.0.0.1:18080`。

## 7. SSH 隧道访问

在开发机执行并保持连接：

```bash
ssh -L 18080:127.0.0.1:18080 root@10.80.4.30
```

浏览器访问 <http://127.0.0.1:18080>。

## 8. TLS 反向代理

通过 HTTPS 反向代理发布时：

1. 将 `OPENHPC_SECURE_COOKIES` 改为 `true`。
2. 将 `OPENHPC_TRUSTED_PROXY_CIDRS` 设置为反向代理来源 CIDR。
3. 在反向代理入口配置 TLS、请求体限制和速率限制。
4. 不要将 OpenHPC Web 直接监听到 `0.0.0.0`。

修改后重启：

```bash
systemctl restart openhpc-web
```

## 9. 升级

```bash
systemctl stop openhpc-web
install -o root -g root -m 0755 \
  /tmp/openhpc-web-linux-amd64 \
  /usr/local/sbin/openhpc-web
systemctl start openhpc-web
systemctl status openhpc-web --no-pager
```

升级时不要覆盖 `/etc/openhpc-web/openhpc-web.env` 和 `/var/lib/openhpc-web/openhpc.db`。

## 10. 故障排查

### `admin username and password are required`

该错误也用于拒绝短于 12 个字符的密码。先停止自动重启，再编辑环境文件：

```bash
systemctl stop openhpc-web
vi /etc/openhpc-web/openhpc-web.env
systemctl start openhpc-web
systemctl status openhpc-web --no-pager
journalctl -u openhpc-web -n 50 --no-pager
```

只检查变量是否存在及长度，不显示秘密内容：

```bash
awk 'BEGIN{FS="="}
/^OPENHPC_ADMIN_USERNAME=/ {print "username length=" length(substr($0,index($0,"=")+1))}
/^OPENHPC_ADMIN_PASSWORD=/ {print "password length=" length(substr($0,index($0,"=")+1))}' \
  /etc/openhpc-web/openhpc-web.env
```

### Dashboard 显示 Slurm 数据不可用

```bash
runuser -u openhpc-web -- /usr/local/bin/sinfo \
  --noheader --Node '--format=%N|%T|%C'
runuser -u openhpc-web -- /usr/local/bin/squeue \
  --noheader '--format=%T'
journalctl -u openhpc-web -n 50 --no-pager
```

确认 `/usr/local/bin/sinfo`、`/usr/local/bin/squeue` 及其父目录均由 root 持有，并且 group/other 不可写。应用会拒绝符号链接或可被普通用户替换的命令路径。

### 服务启动但无法访问

```bash
systemctl status openhpc-web --no-pager
ss -lntp | grep 18080
```

确认 SSH 隧道仍在运行，并从开发机访问 `127.0.0.1:18080`，不是服务器的公网地址。
