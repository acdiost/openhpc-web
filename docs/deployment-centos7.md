# CentOS 7 部署指南

本文档用于将 OpenHPC Web 部署到 Slurm 管理节点。当前验证环境为 CentOS 7、Slurm 25.05.4，服务监听 loopback 地址，并通过固定的 `sinfo`、`squeue`、`sacct`、`sstat`、`sacctmgr` 命令读取集群和作业资源数据。

## 1. 部署原则

- Web 服务只能以 root 身份运行；非 root 启动会在读取配置或访问本地资源前失败。
- Web 服务不通过 SSH root 调用 Slurm，也不直接修改 Slurm 数据库。
- `/etc/openhpc-web/openhpc-web.env` 建议为 `root:root 0600`，这是 systemd 环境文件保护建议，不是应用启动校验。
- 部署时应将 `/var/lib/openhpc-web` 设为 `root:root 0700`。属主或模式不一致时应用只会输出 WARNING；实际读写权限仍受操作系统和 systemd sandbox 限制。
- `/slurm/config` 使用 `OPENHPC_SLURM_CONFIG_ROOT`（默认 `/usr/local/etc`）提供只读配置浏览，不执行重载、不展开 Include。
- 平台用户保存在 `OPENHPC_DATABASE_PATH` 的 SQLite 数据库中。管理员可在“平台用户”页面创建和停用账号；普通用户仅可访问总览、自己的作业、文件管理和终端，权限由服务端路由和作业用户名双重校验。
- 首次访问使用 SSH 端口转发；公网或局域网发布前必须增加 TLS 反向代理。

## 2. 验证服务账户

在管理节点上执行：

```bash
test "$(id -u)" -eq 0

/usr/local/bin/sinfo \
  --Node --json

/usr/local/bin/squeue \
  --json

/usr/local/bin/sacct \
  --json --allocations --allusers --starttime=today --endtime=now

# 将 32943 替换为运行中作业 ID。
/usr/local/bin/sstat \
  --jobs=32943 --allsteps --noheader --parsable2 \
  --format=JobID,AveCPU,AveRSS,MaxRSS,AveVMSize,MaxVMSize,TRESUsageInTot

/usr/local/bin/sacctmgr \
  --json show account WithAssoc
/usr/local/bin/sacctmgr \
  --json show user WithAssoc
/usr/local/bin/sacctmgr \
  --json show qos
```

除 `sstat` 外的六个 Slurm 命令都必须成功。Web 进程以 root 运行，但账户、用户、QoS 和核时页面仍只读取 SlurmDBD 已授权返回的数据。

`sstat` 的 step RPC 通常校验调用者 UID。以 root 运行可提供跨用户查询；程序不额外限制 root 的文件或 Slurm 权限，具体访问范围仍受 systemd sandbox 限制。

核时页面的统计口径为 allocation 分配 CPU 数乘以所选窗口内墙钟占用时间，仅支持过去 24 小时、7 天和 30 天。该值不是 CPU 实际利用时间，也不包含 GPU/TRES 计费。

“节点与分区”页面中的分区容量由 `sinfo --Node --json` 节点记录聚合，不会额外执行 Slurm 命令。可将页面中的分区节点数、CPU 总量与 `sinfo` 输出交叉核对；同一节点属于多个分区时，会分别计入各自分区。

### 2.1 验证 LDAP 只读账户与 TLS

LDAP 功能只支持 LDAPS，并强制验证证书链和 URL 主机名。不能沿用 `TLS_REQCERT allow`、`ldap_tls_reqcert = never` 或跳过验证。先将签发 LDAP 服务证书的 CA 安装为 root 持有、不可组/全局写的普通文件：

```bash
install -o root -g root -m 0644 openhpc-ldap-ca.pem \
  /etc/pki/ca-trust/source/anchors/openhpc-ldap-ca.pem
update-ca-trust

LDAPTLS_CACERT=/etc/pki/ca-trust/source/anchors/openhpc-ldap-ca.pem \
  ldapwhoami -x -H ldaps://ldap.example.com:636 \
  -D 'cn=openhpc-reader,dc=example,dc=com' -W
```

证书 SAN 必须包含 `OPENHPC_LDAP_URL` 使用的主机名。只读 Bind 账户的 ACL 仅需允许在配置的 Base DN 下搜索和读取：`uid`、`cn`、`mail`、`uidNumber`、`gidNumber`、`homeDirectory`、`loginShell`、`description`、`memberUid` 和 `objectClass`。不要授权 `userPassword`、`authPassword`、SSH key 或写权限。

## 3. 构建与上传

在安装 Go 1.25.12 或更高安全修订版本的开发机仓库根目录构建：

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
install -d -o root -g root -m 0700 /var/lib/openhpc-web
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

服务模板已以 root 运行，并保留 systemd sandbox。应用仍保留 loopback、认证、固定命令参数、禁止 shell、允许目录、普通文件、超时和输出上限。除非确有文件预览需求，否则保持 `OPENHPC_JOB_OUTPUT_ROOTS` 为空。

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
OPENHPC_SETTINGS_KEY=REPLACE_WITH_BASE64_32_BYTE_KEY
OPENHPC_ADDRESS=127.0.0.1:18080
OPENHPC_SECURE_COOKIES=false
OPENHPC_TRUSTED_PROXY_CIDRS=

OPENHPC_SLURM_ENABLED=true
OPENHPC_SLURM_BIN_DIR=/usr/local/bin
OPENHPC_SLURM_TIMEOUT=3s
OPENHPC_SLURM_MAX_OUTPUT=2097152
OPENHPC_SLURM_CACHE_TTL=10s
OPENHPC_SLURM_CONFIG_ROOT=/usr/local/etc
# 可选；留空时详情中的“查看内容”保持禁用
OPENHPC_JOB_OUTPUT_ROOTS=

OPENHPC_LDAP_ENABLED=false
OPENHPC_LDAP_URL=ldaps://ldap.example.com:636
OPENHPC_LDAP_BASE_DN=dc=example,dc=com
OPENHPC_LDAP_USER_BASE_DN=ou=People,dc=example,dc=com
OPENHPC_LDAP_GROUP_BASE_DN=ou=Group,dc=example,dc=com
OPENHPC_LDAP_BIND_DN=cn=openhpc-reader,dc=example,dc=com
OPENHPC_LDAP_BIND_PASSWORD=REPLACE_WITH_A_READ_ONLY_BIND_PASSWORD
OPENHPC_LDAP_CA_FILE=/etc/pki/ca-trust/source/anchors/openhpc-ldap-ca.pem
OPENHPC_LDAP_TIMEOUT=3s
OPENHPC_LDAP_MAX_RESULTS=200
```

登录后可从左下角“系统设置”编辑允许的 Slurm/LDAP 配置。SQLite 中的设置覆盖同名环境变量，保存后需重启服务生效。`OPENHPC_SETTINGS_KEY` 必须是受保护环境或密钥管理器提供的 base64 编码 32 字节密钥，用于加密 LDAP Bind 密码；不要把密钥写入 SQLite 或提交到仓库。

必须把 `REPLACE_WITH_A_LONG_RANDOM_PASSWORD` 替换为真实密码。用户名长度为 1 到 64，密码长度至少为 12。不要在聊天记录、命令历史或仓库中保存真实密码。

如需查看 `/home` 下的作业输出，将 `OPENHPC_JOB_OUTPUT_ROOTS` 设置为经过评估的最小绝对目录集合，并用 systemd drop-in 将 `ProtectHome` 改为 `read-only`。接口仍校验文件位于作业工作目录内且为非符号链接普通文件；实际读取能力受 systemd sandbox 和操作系统限制，并只读取最新 256 KiB。

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
/usr/local/bin/sinfo \
  --Node --json
/usr/local/bin/squeue \
  --json
/usr/local/bin/sacct \
  --json --allocations --allusers --starttime=today --endtime=now
/usr/local/bin/sstat \
  --jobs=32943 --allsteps --noheader --parsable2 \
  --format=JobID,AveCPU,AveRSS,MaxRSS,AveVMSize,MaxVMSize,TRESUsageInTot
/usr/local/bin/sacctmgr \
  --json show account WithAssoc
/usr/local/bin/sacctmgr \
  --json show user WithAssoc
/usr/local/bin/sacctmgr \
  --json show qos
journalctl -u openhpc-web -n 50 --no-pager
```

建议 `/usr/local/bin/sinfo`、`/usr/local/bin/squeue`、`/usr/local/bin/sacct`、`/usr/local/bin/sstat`、`/usr/local/bin/sacctmgr` 及其父目录由 root 持有且 group/other 不可写。属主或可写位异常时应用只输出 WARNING；缺失、符号链接、非普通文件或无执行位仍会拒绝初始化。

### 服务启动但无法访问

```bash
systemctl status openhpc-web --no-pager
ss -lntp | grep 18080
```

### LDAP 目录显示暂不可用

```bash
LDAPTLS_CACERT=/etc/pki/ca-trust/source/anchors/openhpc-ldap-ca.pem \
  ldapsearch -x -LLL -o nettimeout=3 \
  -H ldaps://ldap.example.com:636 \
  -D 'cn=openhpc-reader,dc=example,dc=com' -W \
  -b 'dc=example,dc=com' -z 2 \
  '(|(objectClass=posixAccount)(objectClass=posixGroup))' \
  uid cn uidNumber gidNumber

journalctl -u openhpc-web -n 50 --no-pager
```

确认环境文件为 `root:root 0600`，CA 文件及所有父目录由 root 持有且 group/other 不可写，并检查证书 SAN 与 `OPENHPC_LDAP_URL` 主机名一致。应用日志不会输出 Bind DN、密码、搜索词或 LDAP 底层错误。

确认 SSH 隧道仍在运行，并从开发机访问 `127.0.0.1:18080`，不是服务器的公网地址。
