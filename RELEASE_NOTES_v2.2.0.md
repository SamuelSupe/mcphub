# MCPHub v2.2.0

MCPHub v2.2.0 makes request investigations durable and improves administrator safety, login recovery and personal-account diagnostics. It also fixes runtime authorization continuity, endpoint identity and MCP view-cache exhaustion. Managed SQLite and single-instance PostgreSQL migrate from schema 8 to **schema 9**.

MCPHub v2.2.0 新增持久化请求历史，完善管理员保护、登录恢复和个人账号诊断，并修复运行时授权连续性、endpoint 身份与 MCP 视图缓存耗尽问题。托管 SQLite / 单实例 PostgreSQL 从 schema 8 迁移到 **schema 9**。

## Changes / 主要变化

1. **Last-administrator protection:** local permission edits cannot disable or remove the final effective SSO administrator. The check includes group/department inheritance and concurrent edits. Authoritative directory revocations still apply; the SSO guide documents local recovery.
2. **Authorization portal recovery:** invalid, expired or revoked request links no longer prevent users from seeing their existing client grants. Temporary failures retain the request and offer retry.
3. **Pending SSO login:** users awaiting authorization receive a validated OAuth error callback. The CLI exits with contact-administrator guidance and preserves any previous login, while the browser explains the pending/disabled state.
4. **Personal-account diagnostics:** `setup`, `doctor --client` and administrator access checks report missing, expired or unavailable personal upstream credentials. These checks read account status without executing tools or refreshing upstream credentials.
5. **Persistent request history:** completed MCP POST requests survive restart, with time filters, paginated investigation, whole-window statistics and NDJSON export. `admin.request_retention` defaults to `720h` (30 days), accepts `24h`–`8760h`, and prunes expired records automatically.
6. **Runtime updates:** backend/configuration changes keep approval, client-grant and identity services attached to the replacement runtime.
7. **Endpoint identity:** newly created backends use the same endpoint UID in the runtime and database. Delete/recreate continues to receive a fresh UID, invalidating old authorization.
8. **Portal login scopes:** authorization requests use the current endpoint scopes after configuration changes.
9. **MCP view cache:** a 512-view capacity evicts idle, unreferenced views while retaining active requests, sessions, subscriptions and Grant-owned resource history. When all views are retained, new views return HTTP 503 with `Retry-After` instead of growing without limit.

1. **最后管理员保护：** 本地权限编辑不能停用最后一位有效 SSO 管理员或移除其管理权；检查部门/组继承及并发修改。权威目录的离职停用仍生效，SSO 文档提供本地恢复步骤。
2. **授权门户恢复：** 无效、过期或已撤销的申请链接不再阻断已有客户端授权列表；临时失败保留申请并提供重试入口。
3. **待授权登录：** SSO 用户待授权时返回经过校验的 OAuth 错误回调；CLI 明确提示联系管理员并保留原登录，浏览器展示待授权或停用提示。
4. **个人账号诊断：** `setup`、`doctor --client` 与管理员权限检查报告个人上游凭证缺失、过期或不可用；只读检查账号状态，不调用工具或刷新上游凭证。
5. **持久化请求历史：** 已完成的 MCP POST 请求重启后保留，支持时间筛选、分页、完整筛选窗口统计及 NDJSON 导出。`admin.request_retention` 默认 `720h`（30 天），允许 `24h`–`8760h`，自动清理过期记录。
6. **运行时更新：** 修改后端或配置后，新运行时保留审批、客户端 Grant 与身份服务，避免授权能力丢失。
7. **Endpoint 身份一致：** 创建后端时，运行时与数据库使用同一 UID；删除后同名重建仍生成新 UID，使旧授权失效。
8. **门户登录 Scope：** 修改配置后，授权请求使用当前 endpoint 所需的 Scope。
9. **MCP 视图缓存：** 容量限制为 512，回收无人使用的空闲视图，保留活跃请求、会话、订阅及 Grant 所拥有的资源历史。全部视图均需保留时，新视图返回带 `Retry-After` 的 HTTP 503，避免无限增长。

## Upgrade and rollback / 升级与回滚

1. Stop the old gateway. Back up the database, matching `MCPHUB_CONFIG_KEY`, configuration and relevant Vault data; preserve the old binary. Database and Vault backups must be reconciled together, because older backups can restore previously revoked grants or account bindings.
2. Install `mcphub` and `mcphub-cli` v2.2.0 separately. Run `mcphub validate --config /path/to/config.yaml` for a read-only configuration check, then start **one** gateway with `mcphub serve --config /path/to/config.yaml` to apply schema 9. Changing database drivers does not migrate existing data.
3. Confirm login, a read-only call, request-history persistence and the relevant personal-account diagnostic. Existing MCP client configuration can stay unchanged. Review the retention setting and protect exported files.
4. To roll back, stop v2.2.0 and restore the pre-upgrade database, matching key/configuration and old binary together. Reconcile Vault and downstream operations where applicable. An older binary must not write schema 9; restoring only the binary is not a rollback.

1. 停止旧网关，备份数据库、匹配的 `MCPHUB_CONFIG_KEY`、配置及相关 Vault 数据，保留旧二进制。恢复时协调数据库与 Vault；旧备份可能恢复已经撤销的 Grant 或账号关联。
2. 分别安装 v2.2.0 服务端与 CLI。先运行 `mcphub validate --config /path/to/config.yaml` 只读检查配置，再启动**一个** `serve` 实例完成 schema 9 迁移。更换数据库驱动不会自动迁移数据。
3. 验证登录、一次只读调用、请求历史持久化和相关个人账号诊断；现有 MCP 客户端配置可以继续使用。检查保留期并保护导出文件。
4. 回滚时停止 v2.2.0，同时恢复升级前数据库、匹配的密钥/配置和旧二进制，并协调 Vault 与上游业务状态。旧二进制不能写 schema 9，仅替换二进制不构成回滚。

Upgrades from v1.x also require the tool-publication, write-approval and `/v2` module-path changes in the [v2.0.0 migration guide](RELEASE_NOTES_v2.0.0.md#upgrade-and-rollback--升级与回滚). Vault requirements introduced in [v2.1.0](RELEASE_NOTES_v2.1.0.md) remain applicable.

从 v1.x 升级还须遵循 [v2.0.0 迁移说明](RELEASE_NOTES_v2.0.0.md#upgrade-and-rollback--升级与回滚)中的工具发布、写审批及 `/v2` 模块路径变化；[v2.1.0](RELEASE_NOTES_v2.1.0.md) 引入的 Vault 部署要求继续适用。

## Validation / 验证

The implementation passed `go test -race -count=1 -timeout=5m ./...`, `go vet ./...` and `go build ./...` in OrbStack with PostgreSQL 17.11 and Vault 1.21. Focused regressions cover final-administrator protection, failed login callbacks, credential readiness, runtime changes, SQLite migration/reopen, PostgreSQL persistence, history filtering/export and cache lifecycle. After the final administrator callback refinement, its focused race test, database migration tests, vet and build were rerun successfully.

实现已在 OrbStack、PostgreSQL 17.11 与 Vault 1.21 环境通过全量 `go test -race -count=1 -timeout=5m ./...`、`go vet ./...` 和 `go build ./...`。回归验证覆盖最后管理员保护、登录失败回调、账号就绪状态、运行时更新、SQLite 迁移/重开、PostgreSQL 持久化、历史筛选/导出和缓存生命周期。最后一次管理员回调调整后，对应 race 回归、数据库迁移测试、vet 与构建再次通过。

Release preparation also rebuilt both binaries in OrbStack and passed example-configuration validation, a CLI status smoke check, syntax checks for 16 JavaScript modules, and validation of 159 local documentation links and anchors.

发布准备另在 OrbStack 重新构建两项二进制，通过示例配置校验与 CLI 状态检查；16 个 JavaScript 模块语法及 159 个本地文档链接/锚点检查通过。

Native Chrome checks covered invalid-link recovery and retry using the current portal assets with a synthetic API, and time filtering plus an actual NDJSON download against a running SQLite-backed administrator fixture. Portal warning/error logs were empty during those checks. Further browser inspection was stopped by the browser tool's URL policy; the final small portal list adjustment was not rerun in Chrome. Real enterprise SSO tenants and Codex/Claude Code end-to-end login were not exercised.

本机 Chrome 使用当前门户资源和模拟 API 验证失效链接恢复、重试及待授权提示；运行真实 SQLite 管理测试实例验证时间筛选和实际 NDJSON 下载。检查期间门户无警告/错误日志。后续浏览器检查因工具 URL 策略停止，最后一处门户列表的小调整未重新运行 Chrome；未验证真实企业 SSO 租户或 Codex/Claude Code 端到端登录。

GitHub independently gates release assets on native Windows x64/ARM64 CLI tests, Linux race/vet, JavaScript syntax, container and cross-platform builds. Archives, the architecture PDF and SHA-256 checksums become available after the release workflow succeeds; local validation does not establish the remote result.

GitHub 独立执行 Windows x64/ARM64 原生 CLI 测试、Linux race/vet、JavaScript 语法检查、容器和跨平台构建。发布工作流成功后才提供归档、架构 PDF 及 SHA-256；本机验证不代表远端结果。

## Boundaries / 使用边界

- Request history excludes arguments, results, tokens and raw error bodies. Detailed records are encrypted, while query indexes expose identity/routing metadata to database operators. Exports require administrator authorization.
- Persistence runs after request processing. A write failure never replays or changes a business operation; it is logged and surfaced as a recording gap. Crashes before persistence, in-flight requests and GET streams are not covered. This history is not an independent tamper-evident audit archive; unmanaged deployments retain only in-memory diagnostics.
- Account readiness does not prove downstream business permissions. Directory revocation is authoritative even if it removes the final administrator. Recovery requires trusted operator access as documented in the SSO guide.
- One active MCPHub process remains the supported topology. Existing Vault, approval, Broker and enterprise-integration boundaries are unchanged. The bundled screenshots and 27-page PDF remain explicitly dated v2.1.0 snapshots; online guides describe v2.2.0.

- 请求历史不含参数、结果、Token 或原始错误正文；详细记录加密，查询索引仍向数据库运维人员暴露身份/路由元数据，导出要求管理员授权。
- 请求处理完成后才持久化；保存失败不会重放或改变业务结果，会记录日志并提示缺口。保存前崩溃、执行中请求及 GET 流不在范围内；请求历史不替代独立防篡改审计归档，非托管部署仍只有内存诊断。
- 账号就绪检查不证明后端业务权限；权威目录停用始终生效，即使该用户是最后管理员。恢复须按 SSO 指南由可信运维人员操作。
- 仍支持一个活跃 MCPHub 进程；Vault、审批、Broker 和企业集成的既有边界不变。随附截图与 27 页 PDF 明确保留为 v2.1.0 快照，在线指南对应 v2.2.0。

See [English documentation](docs/README.md), [中文文档](docs/README.zh-CN.md), [SSO recovery](docs/sso-and-user-management.md) and the [architecture PDF](docs/feishu-vault-agent-architecture.zh-CN.pdf).
