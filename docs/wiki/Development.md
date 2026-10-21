# 开发与测试

## 环境

- Go 1.22+
- 依赖：仅 `github.com/hashicorp/yamux`

## 构建

```bash
make build                  # dist/queqiaod + dist/queqiao
make linux-server           # linux/amd64 服务端
make agent                  # 本机客户端
```

## 测试

```bash
go test ./...
go test -race ./...
go vet ./...
bash scripts/e2e.sh         # 起服务端 + 客户端，跑完整审批/映射/公网访问
```

端到端脚本覆盖：首次连接待审批、网页 API 批准、上线、创建映射、真实 HTTP 隧道、禁用后端口关闭、封禁下线、安全负例（未登录 401 / 缺 CSRF 403 / 未知 API 404）。

## 代码结构

```text
cmd/queqiaod           服务端入口
cmd/queqiao            客户端入口
internal/protocol      线路协议
internal/auth          Ed25519 / 密钥码 / 挑战应答
internal/machineid     机器指纹
internal/store         设备/规则/管理员/设置 + 审计
internal/server        控制面、会话、公网监听、Web 后端
internal/web           API + 内嵌 SPA
internal/agent         客户端连接/转发
internal/agentsvc      launchd / systemd 安装
internal/agentstate    客户端状态与身份
deploy/                systemd、配置示例、nginx
docs/                  DESIGN / PROTOCOL / API / DEPLOY / AGENT_TASKS
scripts/               e2e.sh / check-ui.sh
```

## 贡献

1. 先开 issue 描述问题/需求；
2. 改动协议需同步更新 `docs/PROTOCOL.md` 并升版本；
3. 涉及隧道/认证的改动请补单测或 e2e；
4. 提交前跑 `go build ./... && go vet ./... && go test ./... && bash scripts/e2e.sh`。

## 关于本项目

- 开发工具：DeepSeek Harness（DSH），Agent Teams（Lead + Web UI + Reviewer）
- 模型：deepseek-flash
- 耗时：从需求到上线约 2.5 小时（含独立复审与安全修复）
