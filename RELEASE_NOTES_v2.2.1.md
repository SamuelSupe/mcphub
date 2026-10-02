# MCPHub v2.2.1

MCPHub v2.2.1 aligns release archives, configuration templates and the bilingual user/administrator documentation. New installations use the same files in the archive and on GitHub Pages. It retains the v2.2.0 runtime behavior and database **schema 9**.

MCPHub v2.2.1 统一发布包、配置模板及中英文用户/管理员文档。新部署从压缩包与 GitHub Pages 获取的是相同文件，沿用 v2.2.0 的运行行为与数据库 **schema 9**。

## Installation / 安装

- Download the server `mcphub` and employee `mcphub-cli` separately. Verify each archive against this release's `SHA256SUMS`.
- The bundled `config.example.yaml` contains one MCP backend and needs only `MCPHUB_PUBLIC_URL`, `MCPHUB_AUTH_ISSUER`, `MCPHUB_PRIMARY_BACKEND_URL` and `MCPHUB_PRIMARY_API_KEY`. It has no console-origin or CRM variables. Copy it to `config.yaml`, replace the environment values with real HTTPS endpoints/credentials, then run `mcphub validate --config config.yaml` and `mcphub serve --config config.yaml`.
- For a console, choose `deploy/config.local.yaml` (MCP URL, issuer, persistent encryption key) or the remote SQLite/PostgreSQL templates. Remote administration additionally needs its HTTPS origin and registered OAuth client ID; PostgreSQL also needs a reachable database/DSN. Save the chosen file as `config.yaml` when following the binary-only installation procedure.
- Tools are unpublished by default. Review and explicitly publish verified read tools. Employee `setup` additionally requires a user portal and registered clients. The Feishu/Vault file remains an advanced integration starting point that requires real credentials and tenant-specific settings.
- The bundled Compose file builds from source and requires a **complete source checkout**. A server binary archive alone cannot build it.

- 分别下载服务端 `mcphub` 与员工端 `mcphub-cli`，使用本版本的 `SHA256SUMS` 核对压缩包。
- 包内 `config.example.yaml` 只有一个 MCP 后端，只需 `MCPHUB_PUBLIC_URL`、`MCPHUB_AUTH_ISSUER`、`MCPHUB_PRIMARY_BACKEND_URL`、`MCPHUB_PRIMARY_API_KEY`，无需控制台来源或 CRM 变量。复制为 `config.yaml`，将环境变量替换为真实 HTTPS 地址/凭证，再执行 `mcphub validate --config config.yaml` 和 `mcphub serve --config config.yaml`。
- 控制台选择 `deploy/config.local.yaml`（MCP 地址、身份源、持久保存的加密密钥）或远程 SQLite/PostgreSQL 模板。远程管理还需 HTTPS 管理来源与已注册的 OAuth Client ID；PostgreSQL 还需可连接的数据库/DSN。按二进制安装步骤操作时，将选中的文件保存为 `config.yaml`。
- 默认不发布工具，先审核并显式发布确认只读的工具。员工 `setup` 还需要用户门户与客户端注册。飞书/Vault 文件仍是高级集成起点，需要真实凭证和租户配置。
- 包内 Compose 文件从源码构建，必须使用**完整源码 checkout**，只解压服务端二进制包无法构建。

The old v2.2.0 archive contains a comprehensive base example with console-origin and CRM variables; it is not the four-variable template. For a new deployment, use this v2.2.1 archive. Existing production configurations must be preserved and reviewed rather than replaced wholesale.

旧 v2.2.0 包的基础文件包含控制台来源和 CRM 变量，不是 4 变量模板。新部署使用 v2.2.1 压缩包；已有生产配置应保留并逐项审核合并，不要整体覆盖。

## Changes / 变化

- Ship the current base, local, remote SQLite, remote PostgreSQL and Feishu/Vault templates with server archives. Publish the same five files as documentation downloads.
- Update all current download/installation commands to v2.2.1 in both languages. Clarify key persistence, environment expansion, managed configuration import, readiness and source-only Compose prerequisites.
- CI validates all five templates and starts/restarts the four standard deployments. The release workflow also runs these checks with the **extracted Linux server binary and bundled files**, and compares every archive's documentation/configuration with the release checkout before publication.
- Stabilize an existing approval-lifecycle test when audit records share a millisecond timestamp; production ordering behavior is unchanged.

- 服务端包携带当前基础、本地、远程 SQLite、远程 PostgreSQL 与飞书/Vault 模板，文档站提供相同的 5 个文件下载。
- 中英文当前下载与安装命令统一为 v2.2.1，明确密钥保存、环境变量展开、托管配置导入、就绪条件及 Compose 源码要求。
- CI 校验 5 个模板并启动、重启 4 种普通部署。发布流程使用**实际解压的 Linux 程序与包内文件**再次验证，并逐包比对文档/配置与发布源码，全部通过后才发布。
- 修正已有审批生命周期测试在审计记录毫秒时间相同时的排序假设，不改变生产排序行为。

## Upgrade and rollback / 升级与回滚

1. Stop the old gateway; back up the database, matching `MCPHUB_CONFIG_KEY`, configuration and relevant Vault data. Preserve the old binary.
2. Install v2.2.1 server and CLI separately. Keep the existing database and matching encryption key. Run the read-only `validate` command, then start **one** `serve` instance. Upgrading from v2.2.0 adds no schema migration; v2.1.0 or earlier migrates to schema 9. Changing database drivers does not transfer data.
3. Confirm readiness, identity login, an authorized read-only call and your relevant portal/administration flows. An unreachable issuer or required backend keeps `/readyz` at 503 even when `/healthz` is 200; `validate` does not prove external connectivity.
4. To return to v2.2.0, retain schema 9 and the matching key/configuration. Returning to v2.1.0 or earlier requires the pre-migration database backup, matching key/configuration and old binary together. Reconcile Vault state and downstream operations where applicable.

1. 停止旧网关，备份数据库、匹配的 `MCPHUB_CONFIG_KEY`、配置及相关 Vault 数据，保留旧二进制。
2. 分别安装 v2.2.1 服务端与 CLI，保留现有数据库和匹配的加密密钥。先执行只读 `validate`，再启动**一个** `serve` 实例。从 v2.2.0 升级不新增 schema 迁移；v2.1.0 或更早版本迁移到 schema 9，更换数据库驱动不会自动搬迁数据。
3. 检查就绪状态、身份登录、一次授权的只读调用及所需门户/管理操作。身份源或必需后端不可用时，即使 `/healthz` 为 200，`/readyz` 仍为 503；`validate` 不证明外部连接正常。
4. 回到 v2.2.0 可沿用 schema 9 与匹配的密钥/配置；回到 v2.1.0 或更早版本必须同时恢复迁移前数据库、匹配密钥/配置及旧二进制，并协调 Vault 与上游业务状态。

For v1.x migrations, also follow the [v2.0.0 migration guide](RELEASE_NOTES_v2.0.0.md#upgrade-and-rollback--升级与回滚). [v2.2.0 notes](RELEASE_NOTES_v2.2.0.md) describe schema 9 and its runtime changes.

从 v1.x 升级还须遵循 [v2.0.0 迁移说明](RELEASE_NOTES_v2.0.0.md#upgrade-and-rollback--升级与回滚)；schema 9 与对应运行时变化见 [v2.2.0 说明](RELEASE_NOTES_v2.2.0.md)。

## Validation and boundaries / 验证与边界

Release gates include Linux race/vet, native Windows x64/ARM64 CLI tests, container/cross-platform builds, checksums, archive-content comparisons and packaged-server startup. Local installation checks run in OrbStack. Controlled identity/MCP fixtures establish configuration and connection behavior; they do not certify a real enterprise SSO or Feishu/Vault tenant. Deployment remains limited to one active gateway instance.

发布门槛包括 Linux race/vet、Windows x64/ARM64 原生 CLI 测试、容器/跨平台构建、校验和、包内文件比对与解压程序启动。本地安装检查在 OrbStack 运行；受控身份源/MCP 验证配置与连接行为，不代表真实企业 SSO 或飞书/Vault 租户验收。仍只支持一个活跃网关实例。

[中文安装指南](https://samuelsupe.github.io/mcphub/admin/install.html) · [English installation guide](https://samuelsupe.github.io/mcphub/en/admin/install.html) · [中文用户指南](https://samuelsupe.github.io/mcphub/) · [English user guide](https://samuelsupe.github.io/mcphub/en/)
