# 快速开始

目标：10 分钟内把本机一个 HTTP 服务通过公网服务器暴露出去。

## 0. 准备

- 一台有公网 IP 的 Linux 服务器（root / systemd）
- 本机：macOS / Linux / Windows
- 放行端口：控制端口 `7600/tcp`、映射段 `39000-40000/tcp`（防火墙 + 云安全组）

> 如果云安全组只放行 39000-40000，可把控制端口改成 `39999`、映射段改成 `39000-39998`。

## 1. 构建 / 安装

```bash
git clone https://github.com/liuhuo23/queqiao.git
cd queqiao
make build
# 产出：dist/queqiaod（服务端）、dist/queqiao（客户端）
```

也可以直接：

```bash
go install github.com/liuhuo23/queqiao/cmd/queqiaod@latest
go install github.com/liuhuo23/queqiao/cmd/queqiao@latest
```

## 2. 服务端

```bash
scp dist/queqiaod-linux-amd64 root@YOUR_SERVER_IP:/usr/local/bin/queqiaod
ssh root@YOUR_SERVER_IP '
  chmod +x /usr/local/bin/queqiaod
  mkdir -p /etc/queqiao /var/lib/queqiao
  chmod 700 /var/lib/queqiao
'
```

` /etc/queqiao/queqiaod.json`：

```json
{
  "control_addr": ":7600",
  "web_addr": "127.0.0.1:7610",
  "data_dir": "/var/lib/queqiao",
  "public_host": "YOUR_SERVER_IP",
  "public_bind_addr": "0.0.0.0",
  "http_addr": "",
  "port_range": [39000, 40000],
  "log_level": "info",
  "log_format": "text"
}
```

```bash
scp deploy/queqiaod.service root@YOUR_SERVER_IP:/etc/systemd/system/
ssh root@YOUR_SERVER_IP 'systemctl daemon-reload && systemctl enable --now queqiaod'
ssh root@YOUR_SERVER_IP 'curl -s http://127.0.0.1:7610/api/v1/health'
```

防火墙放行（云安全组也要放）：

```bash
firewall-cmd --permanent --add-port=7600/tcp
firewall-cmd --permanent --add-port=39000-40000/tcp
firewall-cmd --reload
```

初始管理员密码：

```bash
ssh root@YOUR_SERVER_IP 'cat /var/lib/queqiao/initial-admin.txt'
```

登录后请立刻修改密码（改密成功后该文件会自动删除）。

## 3. 客户端

```bash
# 本机
mkdir -p `/.queqiao/bin
install -m 0755 dist/queqiao `/.queqiao/bin/queqiao
`/.queqiao/bin/queqiao -s YOUR_SERVER_IP:7600
```

首次运行会：

1. 在 `/.queqiao/` 生成 Ed25519 身份密钥与机器指纹；
2. 连接服务端并进入「待审批」，终端打印密钥码（形如 `QIAO-XXXX-XXXX-XXXX`）。

在网页管理台「设备」页核对密钥码后点击「批准」。

批准后客户端会自动连上；安装为开机自启：

```bash
`/.queqiao/bin/queqiao install-service -s YOUR_SERVER_IP:7600
```

## 4. 添加映射并验证

本机起一个测试服务：

```bash
mkdir -p /tmp/demo && echo hello-from-queqiao > /tmp/demo/index.html
cd /tmp/demo && python3 -m http.server 8080 --bind 127.0.0.1
```

网页「映射」→ 新建：

| 字段 | 值 |
|------|-----|
| 设备 | 刚批准的机器 |
| 协议 | tcp |
| 本地地址 | 127.0.0.1:8080 |
| 公网端口 | 39001 |

保存后访问 `http://YOUR_SERVER_IP:39001/`，应看到 `hello-from-queqiao`。

## 5. 下一步

- [客户端安装（launchd/systemd/Windows）](Install-Client)
- [管理台完整使用说明](Usage)
- [常见问题](FAQ)
