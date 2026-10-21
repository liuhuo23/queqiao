# 客户端安装

客户端二进制名：`queqiao`。所有数据都在当前用户目录下的 **`/.queqiao`**：

```text
`/.queqiao/
├── identity.json      # Ed25519 身份密钥（0600）
├── agent.json         # 服务端地址、设备 ID、证书指纹固定
├── bin/queqiao        # 二进制（推荐）
├── queqiao.log
└── queqiao.err.log
```

## macOS

```bash
make agent
mkdir -p `/.queqiao/bin
install -m 0755 dist/queqiao `/.queqiao/bin/queqiao

# 首次运行（会停留在"待审批"）
`/.queqiao/bin/queqiao -s YOUR_SERVER_IP:7600

# 在管理台批准后，安装为开机自启（launchd）
`/.queqiao/bin/queqiao install-service -s YOUR_SERVER_IP:7600
`/.queqiao/bin/queqiao status
tail -f `/.queqiao/queqiao.log
```

launchd plist 固定在 ``/Library/LaunchAgents/io.queqiao.agent.plist`，程序与日志都指向 `/.queqiao`。

## Linux

```bash
make linux-agent
sudo install -m 0755 dist/queqiao-linux-amd64 /usr/local/bin/queqiao
queqiao -s YOUR_SERVER_IP:7600           # 首次运行
sudo queqiao install-service -s YOUR_SERVER_IP:7600
systemctl status queqiao --no-pager
journalctl -u queqiao -f
```

## Windows

```powershell
# 首次运行
queqiao.exe -s YOUR_SERVER_IP:7600
```

批准后用「任务计划程序」或服务管理器设置开机启动；状态目录为 `%USERPROFILE%\.queqiao`。

## 常用参数

| 参数 | 说明 |
|------|------|
| `-s host:port` | 服务端地址（首次必填，之后记住） |
| `-state-dir` | 状态目录（默认 `/.queqiao） |
| `-allow-lan` | 允许映射到私网地址（默认只允许 loopback） |
| `-ca file` | 用指定 CA 校验证书（默认 TOFU 指纹固定） |
| `-insecure` | 跳过证书校验（仅调试） |
| `-log-level` / `-log-format` | 日志 |

## 常用命令

```bash
`/.queqiao/bin/queqiao status          # 本地状态
`/.queqiao/bin/queqiao identity        # 密钥码与公钥
`/.queqiao/bin/queqiao reset-identity -yes   # 重置身份（会变成新设备，需重新审批）
launchctl kickstart -k gui/$(id -u)/io.queqiao.agent   # macOS 重启服务
`/.queqiao/bin/queqiao uninstall-service               # 卸载开机自启
```

## 安全说明

- 私钥只在本机；服务端只存公钥。
- 首次连接绑定机器指纹，换机会被拒绝，需要管理员在网页「重绑定机器」。
- 默认只允许映射 `127.0.0.1`/`localhost`；映射局域网地址需显式加 `-allow-lan`。
