# MCPHub v2.4.0

This release targets fresh, single-instance deployments with SQLite or PostgreSQL. Install `mcphub` on the gateway and `mcpbridge` on Agent computers. No migration workflow for older configurations, variables or databases is included.

## Permissions and enterprise identity

- Permissions belong to independent permission groups. Assign local users directly and map LDAP/OIDC organization groups explicitly. Identities remain isolated by source and stable ID; matching names never grant access. Effective access shows its group and mapping origins.
- Enterprise memberships expire unless reverified. The default templates use a 24-hour maximum age; current account state, membership, publication and service grants continue to restrict each request.
- LDAP and OIDC configuration can be tested before saving. Tests use the draft settings without granting permissions, expose no credentials, and expire after five minutes.

## Agent connections

- `mcpbridge login --native` provides standard OAuth authorization code + PKCE consent for multiple services in one connection. Each service has its own tools, capability choices, resource restrictions and duration, and can be revoked independently. Native clients use standard Bearer authentication without a custom grant header.
- Register OAuth clients and exact redirect URIs in Operations. Changing or removing a registration revokes that client's sessions and service grants.
- Device pairing remains available for remote and headless Agents. Generated commands include the required Agent name. Chinese/English consent retains selections when switching languages, allows a replacement expired code, and reports invalid resource restrictions without consuming the pending request.
- The personal portal groups grants by their actual connection and supports service or whole-connection revocation. Writes remain unchecked initially and retain existing operation approval requirements.

## Configuration, recovery and usability

- The service center combines MCP backends and HTTP tool groups. Console edits create encrypted drafts with validation, redacted differences, impact preview, approval/application and rollback drafts. Concurrent changes cannot be overwritten; rollback does not resurrect revoked credentials.
- Operations shows runtime/configuration, identity/audit and backup/recovery status. `mcphub backup`, `verify-backup` and `restore` provide consistent logical backups, isolated database checks and recovery into an empty target using the matching encryption key and database engine. Restore revokes old security state; users must sign in and consent again.
- Shutdown now waits for retired runtimes and backend connection loops to finish before releasing shared storage. A regression test protects draining requests during configuration replacement.
- Account security separates password changes from authenticator enrollment. Failed enrollment keeps its setup information visible; expired setup can restart. Approvals retain their confirmation and MFA gates, and advanced IDs no longer obscure the requested action.
- Chinese and English manuals, installation examples and GitHub Pages downloads use the same v2.4.0 package names and templates. Default startup requires only `MCPHUB_PUBLIC_URL` and `MCPHUB_CONFIG_KEY`, then local administrator initialization. Add each backend with its own credentials through the console.

See the [administrator manual](docs/admin-guide.md), [user manual](docs/user-guide.md), [security policy](SECURITY.md) and [implementation acceptance record](docs/product-architecture-acceptance.zh-CN.md). Release automation checks race tests, vet, reachable vulnerabilities, Windows clients, archive manifests and extracted-package installation before preparing the draft release. Real enterprise tenants, production Vault, capacity and every third-party MCP client remain deployment-specific acceptance work.

## 中文

本版本仅面向 SQLite / PostgreSQL 的单实例全新部署，不包含旧配置、旧变量或旧数据库迁移流程。网关安装 `mcphub`，用户与 Agent 电脑安装 `mcpbridge`。

- **权限组**：权限统一配置在独立权限组上；本地用户直接入组，LDAP/OIDC 组织组显式映射。来源与稳定身份 ID 保持隔离，有效权限显示来源；企业成员关系超过验证期限后失效。默认期限为 24 小时。
- **Agent 接入**：`mcpbridge login --native` 使用标准 OAuth + PKCE，一次连接可选择多个服务，各服务分别授权、撤销。原有远程/无浏览器设备配对继续可用。运维中心可注册 OAuth 客户端；修改注册会撤销相关会话与授权。
- **配置与恢复**：服务中心统一 MCP 与 HTTP API。配置先保存为加密草稿，再校验、查看影响、审批与应用；可从历史创建回退草稿。备份、隔离恢复检查与空目标恢复保留配置和账号，撤销旧会话、授权及未完成审批。
- **运行可靠性**：关闭时等待旧运行实例及后台连接循环退出，再关闭共享存储；增加配置替换后仍有旧请求的退出回归。
- **UI/UX 修复**：配对命令补齐 Agent 名称；中英文切换保留选择，失效授权码可原页替换，错误资源条件不消耗申请；账号密码与验证器配置分开，失败保留 MFA 信息；连接授权按实际连接归类，审批继续要求确认与加强认证。
- **文档与安装**：双语用户指南、管理员手册、发行包和在线模板统一为 v2.4.0。默认仅要求公开网关地址与配置密钥：启动网关 → 本机初始化管理员 → 登录管理台 → 创建权限组与用户、添加服务 → 测试连接 → 发布工具。

部署与验收边界见[管理员手册](docs/admin-guide.zh-CN.md)及[实现验收记录](docs/product-architecture-acceptance.zh-CN.md)。发布检查覆盖竞态、静态分析、可达漏洞、Windows 客户端及实际解压包安装；真实企业租户、生产 Vault、容量与全部第三方客户端需要在目标环境另行验收。
