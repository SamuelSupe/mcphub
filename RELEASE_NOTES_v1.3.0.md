# MCPHub v1.3.0

MCPHub v1.3.0 reorganizes local administration around connecting services, configuring access, and managing tools. It also ships a separate `mcphub-cli` for browser login and local stdio clients.

[English](#english) | [中文](#中文) · [Repository](https://github.com/SamuelSupe/mcphub) · [Changes since v1.2.0](https://github.com/SamuelSupe/mcphub/compare/v1.2.0...v1.3.0)

## English

### Management UI

- Four dedicated pages: overview, MCP backends, HTTP tool groups, and change history. The overview shows readiness and configuration totals, recent changes, and the two service connection paths.
- Search backend IDs/URLs and tool groups; filter backends by status. Empty states explain the next action, and refresh failures expose a retry action and the last successful update time.
- Configure connections in three steps: connection details, upstream credentials, then client access. Tool policies and advanced connection options are collapsed until needed.
- Edit group headers as individual fields. Existing secret values remain hidden; leaving a value blank preserves it, while removing its Header row removes that credential.
- After creating a tool group, continue directly to adding HTTP interfaces or importing OpenAPI operations. Existing groups open with their tools first and shared connection settings below.
- A consistent light visual style, responsive navigation and editors, keyboard focus handling, and Chinese/English labels replace the previous single-page layout.

### Browser login and local connector

- Adds the separate `mcphub-cli` executable with `login`, `connect`, `status`, and `logout`. The gateway executable continues to provide `serve` and `validate`.
- Browser login uses an external OIDC issuer, a preregistered public OAuth client, authorization code with PKCE S256, and a temporary loopback callback. It verifies gateway access before saving credentials; failed or canceled login preserves the previous profile.
- The connector bridges local MCP stdio clients to the saved HTTPS gateway, preserving tool/resource names, pagination, progress, and subscriptions. It refreshes credentials before expiry, retries a rejected 401 once after refresh, and does not replay operations after network failure.
- Profiles use owner-only file permissions, atomic writes, and per-profile process locks for refresh-token rotation. Tokens are stored as **unencrypted local JSON** in `~/.mcphub/`; status never prints token values. Logout clears local tokens without revoking issuer tokens or signing out the browser. Restart existing connectors after logging in again.

See the [English README](https://github.com/SamuelSupe/mcphub/blob/v1.3.0/README.md) for installation, issuer registration, client configuration, and the management UI guide.

### Upgrade

1. Verify the downloaded archive against `SHA256SUMS`.
2. When upgrading from v1.2.0, retain the existing YAML, SQLite database, and the same `MCPHUB_CONFIG_KEY`. Back up the database and keep the key separately; this release adds no database schema or configuration migration.
3. Replace the server binary, restart the process, and reload the console to use the new embedded UI. YAML-only deployments continue to work with `admin.enabled: false`.
4. Install `mcphub-cli` separately on client machines. Register a public native OAuth client with your issuer and configure JWT access-token audience/scopes as described in the README.

### Boundaries and validation

- The admin console remains opt-in and loopback-only, without admin login or remote administration. CLI user login authenticates MCP access, not the console.
- Access policy still uses JWT `scope`/`scp` with all-of semantics. Several backends can require the same scope, but this release does not introduce endpoint groups, roles, or a user directory. HTTP tool groups share one API connection and policy.
- Local stdio is provided by the connector; gateway ingress and backend transport remain HTTP. The existing v1.2.0 aggregation, encryption, OpenAPI, and deployment boundaries continue to apply.
- Release checks include `go test -race ./...`, `go vet ./...`, formatting, both executable builds, and the server container build. Client integration tests use a disposable HTTPS issuer, real gateway, and MCP backend.
- JavaScript syntax and served assets were checked. Browser interaction and narrow-screen visual checks, and the opt-in native browser login test, were not rerun for this release; automated checks do not cover those manual checks.

## 中文

v1.3.0 围绕服务接入、访问权限和工具管理重新组织本地控制台，并发布独立的 `mcphub-cli`，用于浏览器登录和连接本地 stdio MCP 客户端。

### 管理 UI

- 拆分为概览、MCP 后端、HTTP 工具组、变更记录四个页面。概览集中展示就绪状态、配置数量、最近变更和两种服务接入入口。
- 支持按后端标识/地址或工具组搜索、按后端状态筛选；空状态给出下一步操作，刷新失败时提供重试入口与最近成功更新时间。
- 编辑器按连接信息、上游认证、客户端访问权限三个步骤排列；工具级规则和高级连接选项按需展开。
- 工具组 Header 改为逐项编辑。已有敏感值不回显，编辑时值留空保留原值，删除 Header 行则移除对应凭证。
- 创建工具组后可直接继续添加 HTTP 接口或导入 OpenAPI；编辑已有组时先展示工具，再展示共享连接配置。
- 统一浅色视觉、响应式导航与编辑器、键盘焦点处理和中英文文案，替换原来的单页布局。

### 浏览器登录与本地连接器

- 新增独立的 `mcphub-cli`，提供 `login`、`connect`、`status`、`logout`；网关程序继续提供 `serve` 和 `validate`。
- 浏览器登录使用外部 OIDC、预注册公开 OAuth 客户端、授权码与 PKCE S256，以及临时回环回调。确认网关接受认证后才保存凭证，失败或取消时保留旧 profile。
- 连接器将本地 MCP stdio 客户端桥接到保存的 HTTPS 网关，保留工具/资源名称、分页、进度和订阅。凭证到期前自动刷新；401 刷新后最多重试一次，网络失败不重放操作。
- Profile 使用仅限所有者的文件权限、原子写入和进程间锁保护 refresh token 轮换。Token 以**未加密本地 JSON** 保存在 `~/.mcphub/`；状态命令不输出 Token。退出登录清除本地凭证，不吊销身份服务 Token 或退出浏览器会话；重新登录后需重启已有连接器。

安装、身份服务注册、客户端配置与管理 UI 操作见[中文 README](https://github.com/SamuelSupe/mcphub/blob/v1.3.0/README.zh-CN.md)。

### 升级

1. 使用 `SHA256SUMS` 验证下载归档。
2. 从 v1.2.0 升级时保留现有 YAML、SQLite 数据库和相同的 `MCPHUB_CONFIG_KEY`。升级前备份数据库并单独安全保存密钥；本次无需迁移配置或数据库 schema。
3. 替换服务端二进制、重启进程并刷新控制台，加载新版内嵌 UI。`admin.enabled: false` 的 YAML-only 部署继续有效。
4. 在客户端机器单独安装 `mcphub-cli`，并按 README 在身份服务中注册公开原生 OAuth 客户端、配置 JWT access token 的 audience 和 Scope。

### 边界与验证

- 管理控制台仍需显式启用、仅限回环访问，不提供管理登录或远程管理。CLI 用户登录用于 MCP 访问，不用于登录控制台。
- 权限继续使用 JWT `scope`/`scp`，多个 Scope 必须全部满足。多个后端可要求相同 Scope，但本版本不新增 endpoint 分组、角色或用户目录。HTTP 工具组共享一个 API 连接和权限策略。
- 本地 stdio 由连接器提供，网关入口与后端传输仍为 HTTP；延续 v1.2.0 的聚合、加密、OpenAPI 和部署边界。
- 发布检查包含 `go test -race ./...`、`go vet ./...`、格式检查、两个程序构建及服务端容器构建。客户端集成测试使用临时 HTTPS 身份服务、真实网关和 MCP 后端。
- 已检查 JavaScript 语法和实际提供的静态资源。本次没有重新运行浏览器交互、窄屏视觉检查及可选的原生浏览器登录测试；自动检查不覆盖这些手动检查。

## Downloads / 下载

Install the server on the gateway host and the CLI on user computers. Each archive includes the executable, license, and bilingual READMEs; server archives also include `config.example.yaml`.

服务端安装在网关机器，CLI 安装在用户电脑。每个归档包含可执行文件、许可证和中英文 README，服务端归档另含 `config.example.yaml`。

| Platform / 平台 | Server / 服务端 | CLI / 客户端 |
| --- | --- | --- |
| macOS Intel | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.0/mcphub_v1.3.0_darwin_amd64.tar.gz) | [mcphub-cli](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.0/mcphub-cli_v1.3.0_darwin_amd64.tar.gz) |
| macOS Apple Silicon | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.0/mcphub_v1.3.0_darwin_arm64.tar.gz) | [mcphub-cli](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.0/mcphub-cli_v1.3.0_darwin_arm64.tar.gz) |
| Linux amd64 | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.0/mcphub_v1.3.0_linux_amd64.tar.gz) | [mcphub-cli](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.0/mcphub-cli_v1.3.0_linux_amd64.tar.gz) |
| Linux arm64 | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.0/mcphub_v1.3.0_linux_arm64.tar.gz) | [mcphub-cli](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.0/mcphub-cli_v1.3.0_linux_arm64.tar.gz) |

[SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.0/SHA256SUMS) · [Release workflow / 发布流程](https://github.com/SamuelSupe/mcphub/blob/v1.3.0/.github/workflows/release.yml)
