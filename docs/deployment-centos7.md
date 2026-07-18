# CentOS 7 部署指南

本文档用于将 OpenHPC Web 部署到 Slurm 管理节点。当前验证环境为 CentOS 7、Slurm 25.05.4，服务监听 loopback 地址，并通过固定的 `sinfo`、`squeue`、`sacctmgr` JSON 命令读取集群快照。

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
  --Node --json

runuser -u openhpc-web -- /usr/local/bin/squeue \
  --json

runuser -u openhpc-web -- /usr/local/bin/sacctmgr \
  --json show account WithAssoc
runuser -u openhpc-web -- /usr/local/bin/sacctmgr \
  --json show user WithAssoc
runuser -u openhpc-web -- /usr/local/bin/sacctmgr \
  --json show qos
```

五个 Slurm 命令都必须成功。Web 进程不需要 root、sudo 或 MariaDB 直连权限；账户、用户和 QoS 页面只读取 SlurmDBD 已授权返回的数据。

“节点与分区”页面中的分区容量由 `sinfo --Node --json` 节点记录聚合，不会额外执行 Slurm 命令。可将页面中的分区节点数、CPU 总量与 `sinfo` 输出交叉核对；同一节点属于多个分区时，会分别计入各自分区。

## 3. 构建与上传

在开发机仓库根目录构建：

```bash
export GOPROXY=https://goproxy.cn,direct
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' \
  -o dist/openhpc-web-linux-amd64 ./cmd/server

shasum -a 256 dist/openhpc-web-linux-amd64
```

### 在线开发同步

开发源码目录为 `/opt/ohpc/openhpc-web`。从开发机增量同步时不删除远端额外文件，也不携带本机 UID/GID：

```bash
rsync -a --no-owner --no-group \
  --exclude=.git/ \
  --exclude=state/ \
  --exclude=dist/ \
  --exclude=coverage.out \
  --exclude=output/ \
  --exclude=.playwright-cli/ \
  ./ root@10.80.4.30:/opt/ohpc/openhpc-web/
```

远端非交互 SSH 的 PATH 可能不包含 Go，使用绝对路径并显式配置代理：

```bash
export GOPROXY=https://goproxy.cn,direct
/usr/local/go/bin/go -C /opt/ohpc/openhpc-web test ./...
/usr/local/go/bin/go -C /opt/ohpc/openhpc-web build ./cmd/server
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
# 可选；留空时详情中的“查看内容”保持禁用
OPENHPC_JOB_OUTPUT_ROOTS=
```

必须把 `REPLACE_WITH_A_LONG_RANDOM_PASSWORD` 替换为真实密码。用户名长度为 1 到 64，密码长度至少为 12。不要在聊天记录、命令历史或仓库中保存真实密码。

如需查看 `/home` 下的作业输出，将 `OPENHPC_JOB_OUTPUT_ROOTS` 设置为经过评估的最小绝对目录集合，并用 systemd drop-in 将 `ProtectHome` 改为 `read-only`。还需通过 ACL 或受限用户组只授予 `openhpc-web` 服务账号所需的目录遍历和文件读取权限，不要授予写权限。接口会再次校验文件位于作业工作目录内、文件 UID 与 Slurm 作业用户一致，并只读取最新 256 KiB。未完成这些权限配置时应保持该变量为空。

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

以下示例使用自签发证书和本机 Nginx，适合无法接入内部 CA 的受控内网。浏览器不会自动信任自签发证书；正式生产环境应优先使用组织内部 CA 或受信任 CA 签发的证书。

示例使用域名 `openhpc.example.internal` 和服务器地址 `10.80.4.30`。执行前必须将它们替换为实际 DNS 名称和地址，并确保客户端能够解析该域名。如果客户端只通过 IP 访问，还要将后面的 HTTP 跳转目标改为该 IP。

### 8.1 安装 Nginx 和 OpenSSL

```bash
yum install -y epel-release
yum install -y nginx openssl
```

### 8.2 生成带 SAN 的自签发证书

CentOS 7 的 OpenSSL 不支持较新的 `-addext` 参数，因此先创建扩展配置。SAN 必须包含用户实际访问时使用的域名或 IP，否则浏览器仍会报告证书名称不匹配。

```bash
install -d -o root -g root -m 0755 /etc/pki/openhpc-web
install -d -o root -g root -m 0700 /etc/pki/openhpc-web/private

cat >/root/openhpc-web-openssl.cnf <<'EOF'
[req]
distinguished_name = dn
x509_extensions = v3_server
prompt = no

[dn]
CN = openhpc.example.internal

[v3_server]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = openhpc.example.internal
IP.1 = 10.80.4.30
EOF

umask 077
openssl req -x509 -nodes -newkey rsa:3072 -sha256 -days 365 \
  -config /root/openhpc-web-openssl.cnf \
  -keyout /etc/pki/openhpc-web/private/openhpc-web.key \
  -out /etc/pki/openhpc-web/openhpc-web.crt

chown root:root \
  /etc/pki/openhpc-web/private/openhpc-web.key \
  /etc/pki/openhpc-web/openhpc-web.crt
chmod 0600 /etc/pki/openhpc-web/private/openhpc-web.key
chmod 0644 /etc/pki/openhpc-web/openhpc-web.crt
rm -f /root/openhpc-web-openssl.cnf

openssl x509 -in /etc/pki/openhpc-web/openhpc-web.crt \
  -noout -subject -dates -fingerprint -sha256
```

私钥不得复制到客户端或提交到仓库。证书到期前应重新签发并执行 `systemctl reload nginx`。

### 8.3 配置 OpenHPC Web

编辑 `/etc/openhpc-web/openhpc-web.env`，保持应用仅监听 loopback，并只信任本机代理：

```text
OPENHPC_ADDRESS=127.0.0.1:18080
OPENHPC_SECURE_COOKIES=true
OPENHPC_TRUSTED_PROXY_CIDRS=127.0.0.1/32
```

修改后重启并确认应用仍未监听外部地址：

```bash
systemctl restart openhpc-web
systemctl status openhpc-web --no-pager
ss -lntp | grep 18080
```

### 8.4 配置 Nginx

创建 `/etc/nginx/conf.d/openhpc-web.conf`：

```nginx
limit_req_zone $binary_remote_addr zone=openhpc_web:10m rate=10r/s;

server {
    listen 80;
    server_name openhpc.example.internal 10.80.4.30;

    return 301 https://$server_name$request_uri;
}

server {
    listen 443 ssl;
    server_name openhpc.example.internal 10.80.4.30;

    ssl_certificate     /etc/pki/openhpc-web/openhpc-web.crt;
    ssl_certificate_key /etc/pki/openhpc-web/private/openhpc-web.key;
    ssl_protocols TLSv1.2;
    ssl_ciphers 'ECDHE-RSA-AES256-GCM-SHA384:ECDHE-RSA-AES128-GCM-SHA256';
    ssl_prefer_server_ciphers on;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 10m;

    client_max_body_size 16k;
    limit_req_status 429;

    location / {
        limit_req zone=openhpc_web burst=20 nodelay;

        proxy_pass http://127.0.0.1:18080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto https;
        proxy_connect_timeout 5s;
        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
    }
}
```

检查配置后启动 Nginx：

```bash
nginx -t
systemctl enable --now nginx
systemctl status nginx --no-pager
```

如果 SELinux 为 enforcing 且 Nginx 日志显示连接 `127.0.0.1:18080` 被拒绝，允许 Web 服务器发起代理连接后重试：

```bash
setsebool -P httpd_can_network_connect 1
systemctl restart nginx
```

如果启用了 `firewalld`，只开放 Nginx 的 HTTP/HTTPS 服务，不要开放应用端口 `18080`：

```bash
firewall-cmd --permanent --add-service=http
firewall-cmd --permanent --add-service=https
firewall-cmd --reload
```

### 8.5 验证 HTTPS

先在服务器上验证反向代理和证书名称：

```bash
curl --resolve openhpc.example.internal:443:127.0.0.1 \
  --cacert /etc/pki/openhpc-web/openhpc-web.crt \
  https://openhpc.example.internal/
```

再从客户端访问 `https://openhpc.example.internal/`。需要消除浏览器警告时，只把 `/etc/pki/openhpc-web/openhpc-web.crt` 导入客户端的受信任根证书存储；不要导入或传输私钥。导入前应通过独立安全渠道核对上面输出的 SHA-256 指纹。

检查端口时，预期 Nginx 监听 `80` 和 `443`，OpenHPC Web 仍只监听 `127.0.0.1:18080`：

```bash
ss -lntp | grep -E ':(80|443|18080)[[:space:]]'
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
  --Node --json
runuser -u openhpc-web -- /usr/local/bin/squeue \
  --json
runuser -u openhpc-web -- /usr/local/bin/sacctmgr \
  --json show account WithAssoc
runuser -u openhpc-web -- /usr/local/bin/sacctmgr \
  --json show user WithAssoc
runuser -u openhpc-web -- /usr/local/bin/sacctmgr \
  --json show qos
journalctl -u openhpc-web -n 50 --no-pager
```

确认 `/usr/local/bin/sinfo`、`/usr/local/bin/squeue`、`/usr/local/bin/sacctmgr` 及其父目录均由 root 持有，并且 group/other 不可写。应用会拒绝符号链接或可被普通用户替换的命令路径。

### 服务启动但无法访问

```bash
systemctl status openhpc-web --no-pager
ss -lntp | grep 18080
```

确认 SSH 隧道仍在运行，并从开发机访问 `127.0.0.1:18080`，不是服务器的公网地址。
