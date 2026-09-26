# MCPHub v2.1.0

MCPHub v2.1.0 connects Hub user authorization with Vault-backed upstream accounts. Administrators choose shared or personal credentials for remote MCP endpoints; users connect accounts in the personal portal while keeping their Agent configuration free of upstream tokens. Managed SQLite and single-instance PostgreSQL migrate from schema 7 to **schema 8**.

MCPHub v2.1.0 将用户授权与 Vault 上游账号关联起来。管理员为远程 MCP endpoint 选择共享或个人凭证，用户在个人门户连接账号，Agent 配置无需填写上游 Token。SQLite / 单实例 PostgreSQL 从 schema 7 升级到 **schema 8**。

## Changes / 主要变化

- **Vault KV v2:** AppRole or environment-supplied token authentication, TLS/custom CA, exact shared paths and server-assigned personal paths. Personal OIDC connections use authorization code + PKCE; PAT/API-key connections are also supported.
- **Credential lifecycle:** refresh-token rotation with single-instance serialization and Vault CAS; replacing/disconnecting an account invalidates its owner's endpoint grants and cancels admitted requests/subscriptions. Failed secret deletion uses a persistent cleanup queue.
- **Authorization remains enforced:** current user policy, ClientGrant, scopes, tool publication/state, business-resource rules and per-operation write approvals still apply. Personal requests never fall back to shared discovery credentials.
- **Documentation:** bilingual Vault and deployment guides, documentation indexes, a Feishu SSO + Vault + Codex / Claude Code architecture guide, SVG/PNG diagrams, a 27-page PDF and a deployment configuration example. The PDF preserves the 2026-09-26 architecture review snapshot; online documentation records current release status.

- **Vault KV v2：** 支持 AppRole 或环境变量 Token、TLS/专用 CA、精确共享路径，以及由服务器分配的个人路径。个人浏览器授权使用 OIDC 授权码 + PKCE，也支持 PAT/API key。
- **凭证生命周期：** 单实例串行刷新与 Vault CAS 保存轮换凭证；更换/断开账号会撤销该用户对应 endpoint 的旧 Grant，并取消已接纳的调用和订阅。删除失败由持久化清理队列重试。
- **权限持续生效：** 用户策略、ClientGrant、scope、工具发布/启停、业务资源规则和逐次写审批继续检查。个人调用不会退回使用共享发现凭证。
- **完整文档：** 中英文 Vault/部署指南与文档索引，飞书 SSO + Vault + Codex / Claude Code 架构与最佳实践、SVG/PNG、27 页 PDF 及配置示例。PDF 保留同日架构评审快照，在线文档维护当前版本状态。

## Upgrade and rollback / 升级与回滚

1. Stop the old gateway and back up the database, matching `MCPHUB_CONFIG_KEY`, configuration and relevant Vault data. Preserve the previous binary. Coordinate database and Vault restoration; an old backup can restore old grants or account bindings.
2. Install `mcphub` and `mcphub-cli` v2.1.0 separately. Run `mcphub validate --config /path/to/config.yaml`; validation is read-only. Start **one** gateway with `mcphub serve --config /path/to/config.yaml` to apply schema 8. Changing the database driver does not migrate data.
3. Vault remains opt-in. Configure its access policy before enabling a backend's credential mode. Personal mode requires the authorization portal and ClientGrant. Review tool publication and write policies, then complete a read-only acceptance call.
4. To roll back, stop v2.1.0 and restore the pre-upgrade database, matching key/configuration and old binary together. Restore/reconcile Vault data when applicable. An older binary must not write schema 8. Reconcile any downstream writes whose outcome is uncertain.

1. 停止旧网关，备份数据库、匹配的 `MCPHUB_CONFIG_KEY`、配置和相关 Vault 数据，保留旧二进制。恢复时协调数据库与 Vault；旧备份可能恢复旧 Grant 或账号关联。
2. 分别安装 v2.1.0 的服务端与 CLI。先运行 `validate` 做只读检查，再启动**一个** `serve` 实例迁移到 schema 8。更换数据库驱动不会自动迁移数据。
3. Vault 按需启用，先配置访问策略再修改后端凭证模式。个人模式要求用户授权门户和 ClientGrant；审核工具发布与写策略后，先完成只读验收。
4. 回滚需同时恢复升级前数据库、匹配密钥/配置与旧二进制，并协调 Vault 状态。旧版不能写 schema 8。对结果未知的业务写入，先核查后端状态再决定后续操作。

Upgrading from v1.x also requires the publication/default-write-policy and `/v2` module changes described in the [v2.0.0 migration guide](RELEASE_NOTES_v2.0.0.md#upgrade-and-rollback--升级与回滚).

从 v1.x 升级还须遵循 [v2.0.0 迁移说明](RELEASE_NOTES_v2.0.0.md#upgrade-and-rollback--升级与回滚)中的默认发布、写审批和 `/v2` 安装路径变化。

## Validation / 验证

Local OrbStack verification passed `go test -race ./...`, `go vet ./...` and both binary builds, using PostgreSQL 17 and Vault 1.21. Credential/Vault and database suites were also run without test caching. The example configuration, 16 browser JavaScript modules, workflow shell syntax and 159 local documentation links passed validation; the published PDF matches the verified 27-page artifact.

OrbStack 本机验证通过全量 `go test -race ./...`、`go vet ./...` 和两项二进制构建，使用 PostgreSQL 17 与 Vault 1.21；凭证/Vault 与数据库测试另以禁用缓存方式运行。示例配置、16 个浏览器 JavaScript 模块、工作流 shell 语法及 159 个本地文档链接校验通过，发布 PDF 与已检查的 27 页文件一致。

GitHub separately runs its Windows CLI, race/vet, container and platform build jobs, verifies archive contents, and publishes SHA-256 checksums including the PDF. Binary release assets become available only after those jobs pass; local Linux checks do not establish the remote matrix result.

GitHub 另行执行 Windows CLI、race/vet、容器与各平台构建，校验归档清单并发布含 PDF 的 SHA-256。远端任务通过后才生成二进制发行附件，本机 Linux 验证不代表远端矩阵已完成。

## Boundaries / 使用边界

- This release does not certify a real Feishu tenant or real Codex/Claude Code SSO deployment. Feishu endpoint pairing/PKCE requires tenant acceptance; the directory pull adapter is not included. Ordinary OAuth2 login does not prove OIDC step-up/MFA.
- Vault holds credentials and applies path ACLs; Hub policies decide whether a tool call is allowed. Tokens are not exported to Agents. Downstream business systems must enforce their own permissions.
- Vault authorization covers remote MCP endpoints. HTTP tool groups, dynamic cloud/database credentials, provider-specific SaaS OAuth adapters, provider-side token revocation and multi-instance Hub coordination remain outside this version.
- OAuth refresh and Vault persistence are not one distributed transaction; a crash after issuance but before persistence may require reconnecting. Revocation cannot undo a downstream write already committed.

- 本版不宣称已完成真实飞书租户或 Codex/Claude Code SSO 联调；飞书端点配对与 PKCE 仍需验收，目录拉取适配器未包含。普通 OAuth2 登录不代表满足 OIDC 加强认证/MFA。
- Vault 管理凭证及路径 ACL，Hub 策略决定工具是否可调用；Token 不导出给 Agent，后端仍需自行鉴权。
- 首版覆盖远程 MCP endpoint；HTTP 工具组、动态云/数据库凭证、供应商专用 SaaS OAuth、上游 revoke 和 Hub 多实例协调不在本版范围内。
- OAuth 刷新与 Vault 持久化不是分布式事务，换码成功而保存前崩溃可能需要重连；撤权不能撤销已提交的业务写入。

See [English documentation](docs/README.md), [中文文档](docs/README.zh-CN.md) and the [architecture PDF](docs/feishu-vault-agent-architecture.zh-CN.pdf).
