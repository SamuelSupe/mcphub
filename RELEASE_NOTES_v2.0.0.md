# MCPHub v2.0.0 — access governance and enterprise console

MCPHub v2.0.0 brings centralized tool publication, write approval, per-client grants, enterprise SSO permissions and a redesigned management console. This is a major upgrade: tool publication defaults and write execution rules change, managed databases migrate to schema 7, and Go module/install paths add `/v2`.

MCPHub v2.0.0 将工具发布、写审批、客户端授权、企业 SSO 权限及新版管理控制台统一交付。本次为主版本升级：工具默认发布行为与写执行规则变化，托管数据库迁移到 schema 7，Go module/安装路径新增 `/v2`。

## Highlights / 主要变化

- Reorganized management console with grouped navigation, operational overview, expandable identity/request records, consistent forms and status styling, bilingual copy and narrow-screen navigation. / 管理控制台按功能分组，新增运行概览，统一用户/请求列表、表单和状态样式，支持中英文与窄屏导航。

- Generic confidential OIDC/OAuth2 SSO bridge, local user/organization permissions, claim/directory synchronization and immediate permission checks. / 通用机密客户端 OIDC/OAuth2 桥接、本地用户组织权限、声明/目录同步及逐次权限校验。See [SSO guide](docs/sso-and-user-management.md) / [中文说明](docs/sso-and-user-management.zh-CN.md).

- Explicit tool publication, business-resource restrictions, per-operation write approval, reviewer separation, step-up authentication and approval audit. / 工具显式发布、业务资源限制、单次写审批、独立审批人、加强认证和审批审计。
- Client authorization and the local Broker, with scopes intersected at the gateway, grant expiry and revocation. / 客户端授权与本地 Broker，由网关再次求取 Scope 交集并校验过期和撤销。
- Tool permissions workbench, side-effect-free access checks and `mcphub-cli doctor`. / 工具权限工作台、无副作用权限检查和本机诊断。
- `mcphub-cli setup`, an administrator client authorization center, and bounded request diagnostics. / 接入向导、管理员客户端授权中心和有容量上限的请求诊断。

See [English usage](README.md#guided-setup-and-administration) / [中文使用说明](README.zh-CN.md#接入向导与授权运维).

## Upgrade and rollback / 升级与回滚

The database schema is **7** for both SQLite and PostgreSQL. SSO adds users, organization memberships and credential sessions; supported older managed stores, including v1.4.0, migrate on startup. PostgreSQL deployment remains **single instance**. Changing `database.driver` does not migrate data.

SQLite/PostgreSQL 均使用 **schema 7**；SSO 新增用户、组织成员关系及凭证会话，v1.4.0 等受支持旧版托管数据库在启动时迁移。PostgreSQL 仍为**单实例**，切换 `database.driver` 不会迁移数据。

1. Before upgrading, record the old binary/configuration and stop the gateway. Back up the database and separately retain the matching `MCPHUB_CONFIG_KEY`. For SQLite, use SQLite's `.backup` command or a consistent filesystem backup including WAL state; do not copy only an active `.db` file. For PostgreSQL, use `pg_dump` to a protected backup and verify restoration into a separate database. Protect backups as secrets.
2. Review every backend's `published_tools`. Empty or omitted lists publish nothing; existing scope wildcards do not publish tools. Manually created HTTP tools default to disabled. Existing HTTP tools keep their stored state. Classify tools deliberately: unknown/write operations require the remote approval service and cannot execute in local unauthenticated or YAML-only mode.
3. Install the new `mcphub` and `mcphub-cli` binaries separately. Go installs use `github.com/SamuelSupe/mcphub/v2/cmd/mcphub@v2.0.0` and `github.com/SamuelSupe/mcphub/v2/cmd/mcphub-cli@v2.0.0`; binary names stay unchanged. Preserve the configuration path, database and encryption key. Run `mcphub validate --config /path/to/config.yaml`, then start one gateway with `mcphub serve --config /path/to/config.yaml`; startup performs any required schema migration. `validate` does not migrate the database.
4. Configure the portal's OIDC callback, matching issuer/subject and resource audience before enabling client authorization. SSO and strict client grants are opt-in; new SSO users need local enablement and permissions in MCPHub. Directory synchronization does not assign roles or grants. Use a test user to run setup and doctor, check a read operation, a denied scope, a write approval and grant revocation. Refresh admin browser assets and restart local MCP connections after changing identity/profile.
5. To roll back a schema upgrade, stop the new gateway and restore the **pre-upgrade database, matching key/configuration and old binary together**. Older binaries must not write the upgraded database. Restoring a backup loses subsequent configuration, grants and approval records; reconcile downstream writes before retrying operations with uncertain outcomes.

1. 升级前记录旧二进制和配置并停止网关，备份数据库，单独保存匹配的 `MCPHUB_CONFIG_KEY`。SQLite 使用 `.backup` 或包含 WAL 状态的一致性备份，不要只复制正在使用的 `.db`；PostgreSQL 使用 `pg_dump`，并在独立数据库验证恢复。备份按敏感数据保护。
2. 逐项核对后端 `published_tools`；省略或空名单不发布任何工具，已有 Scope 通配规则不会代替发布。新建手工 HTTP 工具默认关闭，已有工具保留启停状态。明确读写分类；未分类／写工具要求远程审批服务，不能在本地免登录或仅 YAML 模式执行。
3. 分别安装新版 `mcphub` 和 `mcphub-cli`；Go 安装路径为 `github.com/SamuelSupe/mcphub/v2/cmd/mcphub@v2.0.0` 和 `github.com/SamuelSupe/mcphub/v2/cmd/mcphub-cli@v2.0.0`，二进制名称不变。保留配置路径、数据库和密钥。先运行 `mcphub validate --config /path/to/config.yaml`，再用 `mcphub serve --config /path/to/config.yaml` 启动一个网关；启动时执行必要的 schema 迁移，`validate` 不迁移。
4. 启用客户端授权前配置门户 OIDC 回调、一致的 issuer／subject 和资源 audience。SSO 与严格客户端授权按需启用；SSO 新用户需在 MCPHub 启用并配置权限，目录同步不自动分配角色或授权。用测试用户验证 setup、doctor、只读调用、Scope 拒绝、写审批和撤销。刷新管理页面；切换身份或 profile 后重建本地 MCP 连接。
5. 回滚 schema 升级时停止新版网关，将**升级前数据库、匹配密钥／配置和旧二进制一同恢复**，不能让旧二进制写升级后的库。恢复会丢失备份后的配置、授权和审批记录；对结果不确定的操作，重试前先核查下游实际写入。

## Boundaries / 边界

The wizard grants tools on one endpoint per entry; use the existing explicit client commands for prompts/resources/subscriptions. Generated configuration is credential-free but machine-specific. Request diagnostics are an in-memory recent-request view, not durable audit or full monitoring. Existing in-flight upstream side effects cannot be undone by grant revocation. PostgreSQL remains single-instance. No multi-instance coordination, tenant isolation, cross-database migration tool, built-in accounts, Feishu tenant connection, Feishu directory adapter or SCIM implementation is included. Real enterprise identity providers still require deployment-specific acceptance testing. Broker IPC limits other OS users; it does not authenticate applications or isolate malicious processes under the same OS account.

向导每个条目授权一个 endpoint 的工具；prompts／resources／subscriptions 使用现有显式客户端命令。生成的配置不含凭证，但路径与本机绑定。请求诊断是内存中的近期视图，不是持久审计或完整监控。撤销无法回滚已经接纳的上游副作用。PostgreSQL 仍为单实例。本版不包含多实例协调、租户隔离、跨数据库迁移工具、内置账号、飞书租户连接、飞书目录适配器或 SCIM。真实企业身份服务仍需按部署配置验收。Broker IPC 限制其他系统用户接入，但不证明应用身份，也不能隔离同一系统账户下的恶意进程。

## Downloads and release checks / 下载与发布检查

Separate server and CLI archives are provided for macOS/Linux amd64 and arm64; the Windows CLI is provided for x64 and ARM64. All archives include bilingual README files, security policy, these release notes and the `docs/` guides. Server archives also include configuration and deployment examples. Check each download against `SHA256SUMS` before use.

提供 macOS/Linux amd64、arm64 的独立服务端与 CLI 包，以及 Windows x64、ARM64 CLI。全部归档包含中英文 README、安全策略、发行说明及 `docs/` 指南；服务端另含配置和部署示例。使用前与 `SHA256SUMS` 核对下载文件。

The tag workflow requires native Windows CLI tests on both architectures, the Go race suite with real PostgreSQL, `go vet`, JavaScript syntax checks, a container build, all archive builds and checksum/content verification before publishing assets. Native Chrome validated the console's desktop/narrow layouts, navigation, filters, permission-save feedback, role visibility and bilingual UI; this is not certification of an external enterprise identity provider.

Tag 工作流在上传附件前要求 Windows 两种架构的原生 CLI 测试、含真实 PostgreSQL 的 Go race 测试、`go vet`、JavaScript 语法检查、容器构建、全平台归档构建及校验和/内容检查。本机 Chrome 已验证控制台桌面/窄屏布局、导航、筛选、权限保存反馈、角色可见性和中英文界面；这些检查不代表外部企业身份服务的验收。
