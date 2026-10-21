# 常见问题

## 客户端一直显示"待审批"

到管理台「设备」页批准。核对密钥码是否一致；如果设备列表为空，检查：
- 客户端是否能连到控制端口（`nc -zv YOUR_SERVER_IP 7600`）；
- 防火墙/安全组是否放行控制端口；
- 客户端日志 `/.queqiao/queqiao.log`。

## 提示 fingerprint_mismatch（机器指纹不一致）

换机器或重装系统导致。管理台「设备」→「重绑定机器」，设备回到待审批后重新批准；客户端会自动重连并登记新指纹。

## 提示"证书指纹与首次连接不一致"

服务端重装/更换了 TLS 证书。确认是本人操作后，删除客户端 `/.queqiao/agent.json` 里 `trust_pins` 对应条目（或直接删掉 `agent.json` 中的 trust_pins 字段），重启客户端即可重新固定。

## 公网端口连不上

按顺序检查：

1. 规则 state 是否 `active`、设备是否 online；
2. 云安全组 + firewalld 是否放行该端口；
3. 端口是否在 `port_range` 内、是否被系统进程占用（网页「映射」里有原因）；
4. 本机服务是否只监听 127.0.0.1（正常，客户端会本地 dial）。

## 规则 state=error

通常是公网端口被占用或不在端口段内。网页会显示具体原因；换一个端口或调整端口段后重试。

## Agent 日志说"本地地址被安全策略拒绝"

默认只允许映射 `127.0.0.1`/`localhost`。要映射局域网地址（如 `192.168.1.10:22`），启动客户端时加 `-allow-lan`。

## HTTP 域名映射怎么用

1. 设置里把「HTTP 监听」设为 `:39080`（80/443 被占用时）；
2. 把域名解析到服务器；
3. 新建映射选 http、填域名和本地地址；
4. 访问 `http://你的域名:39080`。

## 管理台 401 / CSRF 校验失败

会话过期或浏览器页面过旧。刷新页面重新登录；升级服务端后请硬刷新一次（Cmd+Shift+R / Ctrl+F5）。

## 忘记管理员密码

目前没有重置命令，可这样恢复：

1. 停止服务：`systemctl stop queqiaod`；
2. 备份 `/var/lib/queqiao/state.json`；
3. 删除其中 `"admins": [...]` 数组（保留其它字段）；
4. 启动服务：`systemctl start queqiaod`；
5. 重新查看 `/var/lib/queqiao/initial-admin.txt` 登录并改密。

设备、规则、审计不受影响。

## 客户端开机自启

```bash
# macOS / Linux
`/.queqiao/bin/queqiao install-service -s YOUR_SERVER_IP:7600
# 卸载
`/.queqiao/bin/queqiao uninstall-service
```

Windows 用任务计划程序或服务管理器。

## 如何彻底卸载

```bash
# 停止并删除服务
`/.queqiao/bin/queqiao uninstall-service
rm -rf `/.queqiao
```

服务端删除 `/etc/queqiao`、`/var/lib/queqiao` 与 `queqiaod.service` 即可。
