# MCPBridge 设备配对本地验收记录

验收日期：2026-10-02。对应 [设备授权设计](mcpbridge-device-authorization-design.zh-CN.md)。本文记录 v2.3.0 发布前的本地实现及候选包验收；GitHub 发行包复核见 [实现审计记录](implementation-audit.zh-CN.md)。

## 环境和实际覆盖

- Linux 服务、MCPBridge、MCP stdio Agent 驱动、SQLite 和 PostgreSQL：本机 OrbStack Ubuntu，Linux arm64。
- 浏览器：本机 Chrome。使用隔离的测试用户、用户组、LDAP、OIDC 身份服务及两个带独立凭证的测试 MCP 后端。
- Agent 到 MCPHub 使用 HTTPS，并校验测试 CA。Chrome 访问一次性 HTTP 预览代理；代理只为本次测试转换回调地址和测试 Cookie，未绕过 Chrome 的证书警告。因此浏览器验收覆盖页面和登录回调，不代表生产 HTTPS 证书部署已验收。
- MCP stdio 驱动使用 `2025-11-25` 协议版本；集成测试还使用仓库现有 MCP SDK。没有修改用户的 Codex、Claude Code 或 VS Code 配置。

| 行为 | 结果和证据范围 |
| --- | --- |
| 首次配对 | 无现成用户凭证时可创建申请；登录前只返回公开链接、配对码、状态和请求标识 |
| 登录与确认 | 集成测试覆盖内建账号；Chrome 实际覆盖 LDAP、OIDC 登录，登录后返回同一配对申请，支持中英文 |
| 范围选择 | Chrome 验证服务选择、工具默认不勾选、空选择不能确认、时长及高级数据范围输入；写权限默认关闭 |
| 单次交付 | SQLite、真实 PostgreSQL 的两个独立 Store 并发确认和领取；只建立一个 Grant、一个 SSO 家族，只交付一次 |
| 持久化 | SQLite 重新打开和实际服务重启后，未到期申请可继续确认和领取 |
| 权限变化 | 确认、领取、兑换阶段的组权限变更阻止交付；账号停用、服务工具策略变更和身份源停用阻止领取；Agent 已连接后，即使 scope 不变，工具权限撤回也移除业务工具 |
| 失效和清理 | 到期、限流、丢失交付后的取消及清理测试通过；SQLite、PostgreSQL 并发查码仍只接受每来源每分钟 20 次，避免跨进程计数覆盖；已有生效的 Grant 和共享会话不被取消逻辑清理 |
| Broker 绑定 | 两个客户端可在同一用户的 Broker 会话下分别授权；已结束的会话可通过明确的新配对创建新会话，旧 Grant 不转移 |
| 保留可用配置 | 拒绝新的配对不会覆盖已有可用 profile；实际制造客户端凭证文件写入失败，验证原 profile 不变且新 Grant 被撤销；申请与本机用户私有存储绑定 |
| Agent 首次连接 | 初始化立即成功，先只提供 `mcpbridge_auth_start` 和 `mcpbridge_auth_status`；重复 start 返回同一有效申请 |
| Agent 授权后 | 实际收到 `notifications/tools/list_changed`，刷新目录后成功调用获准的读取工具；预授权业务调用被拒绝，没有延后重放 |
| Agent 撤销后 | 业务工具从目录移除，状态说明需要重新认证；不自动发起新的授权 |
| 公开输出 | stdio 驱动检查工具结果不含 access token、refresh token、device code、Grant 兑换凭证或 Broker 私有凭证 |
| 候选包安装 | 实际解压的 Linux arm64 服务端、MCPBridge 压缩包通过命令检查和安装检查；六个 YAML 校验，四个默认模板完成启动、重启及管理员登录 |
| 配置和文档 | 未设置两个 PRIMARY 变量，内建认证无需外部 issuer；源文件、包内及文档站的四个默认模板逐字一致；双语文档站构建及本地链接检查通过 |

## 自动检查

以下检查均在上述环境通过；这里只记录有实际执行结果的检查。

```sh
go test ./...
go test -race ./internal/configstore ./internal/client ./internal/app ./internal/sso
go test ./internal/configstore -run TestPostgresConfigurationLifecycle -count=1
go test ./internal/app ./internal/client
bash scripts/verify-client.sh <解压的 mcpbridge> 2.3.0
bash scripts/verify-installation.sh <解压的 mcphub> <服务端解压目录>
node docs-site/build.mjs
git diff --check
```

PostgreSQL 检查使用独立测试 schema 和临时 DSN；没有在记录或公开输出中打印密码。文档站生成 84 个双语页面，检查 3422 个本地链接和资源。

## 未覆盖的部分

本轮后续性能与严重缺陷复核、修复及新版候选包验收见 [实现审计记录](implementation-audit.zh-CN.md)。其中补充了已完成配对的会话保护、授权/Broker 到期拒绝领取、并发 MFA 防重放、工具调用撤权与额外请求计数。

- 未在真实 Codex、Claude Code、VS Code 的客户端 UI 中安装并运行；已验证真实 MCP stdio 协议交互和工具调用，不能据此声称所有 Agent 产品均已验收。
- Windows amd64、macOS arm64 的 MCPBridge 已交叉编译，未运行验收；实际发行包安装验收限定为 Linux arm64。
- 未做生产身份服务、生产 TLS 证书、长时间运行或压力验收；文件失败覆盖客户端入口写入失败，未穷举所有磁盘故障。
- 本阶段只通过设备配对授权工具能力；提示词、资源 URI 和订阅继续使用现有 `setup` / `client add` 流程。
- 仅面向新部署；没有新增旧配置、变量或数据库的迁移流程。
