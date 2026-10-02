# Contributing / 贡献指南

[English](#english) | [中文](#中文)

## English

Thank you for contributing to MCPHub. Please keep changes small, observable, and aligned with the current HTTP aggregation boundary.

This public repository is [SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub), documenting v2.2.0 with module path `github.com/SamuelSupe/mcphub/v2`. The project is released under the [Apache License 2.0](LICENSE); see the [v2.2.0 release notes](RELEASE_NOTES_v2.2.0.md) for the shipped scope. Earlier releases remain available as historical references.

### Before you start

1. Start with the [documentation index](docs/README.md), then read the relevant [user manual](docs/user-guide.md), [administrator manual](docs/admin-guide.md) and [configuration reference](docs/configuration.md).
2. For a vulnerability or a suspected secret leak, do not open a public issue; follow [SECURITY.md](SECURITY.md).
3. Check the explicit exclusions before proposing a feature. The current release does not provide stdio backends, a standalone legacy SSE endpoint, native TLS, dynamic tenants, opaque-token introspection, Tasks, MCP Apps, or custom MCP extensions. The release includes local or OIDC-authenticated remote administration, encrypted SQLite/PostgreSQL configuration storage for one instance, optional endpoint limits, Vault-backed shared/personal accounts, and a separate local stdio-to-HTTP connector; see the administrator and user manuals.

### Development setup

Use Go 1.26. The repository's module declares `go 1.26.0`.

```bash
go version
go mod download
go build ./cmd/mcphub
go build ./cmd/mcpbridge
```

For configuration-driven work, copy `config.example.yaml`, set the required environment variables, and run:

```bash
go run ./cmd/mcphub validate --config ./config.yaml
```

The loader rejects unknown YAML fields, multiple documents, missing `${NAME}` environment variables, non-HTTPS public/issuer URLs, and unsafe remote HTTP backends. Local HTTP is accepted only for loopback hosts with an explicit `allow_insecure_http: true`.

### Code and test expectations

- Keep production changes focused on the requested behavior; avoid speculative abstractions and unrelated rewrites.
- Preserve the MCP wire contract and the backend namespace/URI mapping. Do not silently broaden a public capability.
- Prefer tests at observable behavior boundaries: authentication claims, scope all-of filtering, readiness, reload invariants, transport/header safety, and routing/rewrite behavior.
- Keep credentials, bearer tokens, and real backend URLs out of source, fixtures, logs, and pull requests.
- Format Go code with `gofmt`. Before requesting review, run the relevant package tests; for a full local check, use `go test ./...` and `go vet ./...` with the repository's Go 1.26 toolchain.

The `internal/client` integration tests run a disposable HTTPS identity service, the real gateway and an MCP backend. Run `go test -race ./internal/client` for login, credential rotation, concurrent connectors and transport checks. On macOS, the following opt-in check opens the system browser for authorization, denial and cancellation. Follow the terminal prompts; it uses temporary profiles and a loopback consent page, with HTTPS retained for discovery, token exchange and gateway calls.

```bash
CGO_ENABLED=0 MCPHUB_BROWSER_QA=1 go test ./internal/client -run '^TestNativeBrowserLogin$' -v -timeout=8m
```

For admin UI changes, validate the served pages in local Chrome: navigation, search/filter, editors, error/empty states, language switching, and a narrow viewport. The UI is embedded by Go and has no frontend build step; rebuilding the server is required to serve changed assets. Check every `internal/app/adminui/*.js` file with `node --input-type=module --check < "$script"`.

Windows CLI checks run on native x64 and ARM64 GitHub runners through `.github/workflows/windows-cli.yml`, reused by CI and release gates. They exercise login/refresh/connector flows, cross-process locks, DACL rejection, file replacement, and executable commands. Cross-compiling alone does not validate Windows filesystem behavior.

PostgreSQL persistence checks require a disposable database with `citext` installation rights. Set `MCPHUB_TEST_POSTGRES_DSN` before `go test -race ./...`; the test creates and removes its own schema. Without this variable the PostgreSQL test is skipped. CI and release workflows provide PostgreSQL automatically. The full suite covers admin audience/scope and CSRF checks, database revisions and encrypted secrets, endpoint rate budgets and cancellation, and connector 429 handling without replay.

### Pull requests

- Use a focused title that states the user-visible or operational change.
- Explain the problem, the chosen behavior, configuration/API impact, and known limitations.
- Include validation commands and their results. If a check was not run, say why.
- Update both languages of the affected manual or reference when behavior, configuration, security semantics or supported boundaries change; update the README when the project overview or entry points change.
- Update the issue/PR templates or release notes only when the requested change actually affects them; do not add a release promise for an excluded capability.

### Documentation changes

Documentation must reflect the current code, not a planned design. Keep both languages of each manual, reference and README semantically equivalent, with working language links. Put employee workflows in `docs/user-guide*.md`, administration in `docs/admin-guide*.md`, and detailed fields/protocols in `docs/configuration*.md`; the README remains an overview and navigation entry.

Use placeholders only in fields that [support environment expansion](docs/configuration.md#environment-variables-and-secrets); use variable names for `*_env` fields and example literals elsewhere. Never include live credentials. Keep runnable examples focused on one deployment scenario, distinguish YAML fragments from complete files, and label historical designs by version. Validate changed complete examples with the real loader and check Markdown links; configuration validation is not external integration acceptance.

## 中文

感谢你为 MCPHub 贡献代码。请让改动保持聚焦、可观察，并符合当前的 HTTP 聚合边界。

本公开仓库是 [SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub)，当前文档对应 v2.2.0，module path 为 `github.com/SamuelSupe/mcphub/v2`。项目采用 [Apache License 2.0](LICENSE)；已发布范围见 [v2.2.0 发行说明](RELEASE_NOTES_v2.2.0.md)。更早版本仍作为历史参考保留。

### 开始前

1. 从[文档导航](docs/README.zh-CN.md)开始，按改动阅读[用户手册](docs/user-guide.zh-CN.md)、[管理员手册](docs/admin-guide.zh-CN.md)和[配置参考](docs/configuration.zh-CN.md)。
2. 漏洞或疑似 secret 泄露不要提交公开 Issue，请遵循 [SECURITY.md](SECURITY.md) 的私下报告流程。
3. 提议新功能前先检查明确排除项。本版本不提供 stdio 后端接入、独立旧 SSE 端点、原生 TLS、动态租户、opaque token introspection、Tasks、MCP Apps 或自定义 MCP 扩展。本版本包含本地或 OIDC 远程管理、单实例的加密 SQLite/PostgreSQL 配置存储、可选 endpoint 限流、Vault 共享/个人账号，以及独立的本地 stdio 到 HTTP 连接器，参见管理员手册和用户手册。

### 开发环境

使用 Go 1.26；仓库 module 声明为 `go 1.26.0`。

```bash
go version
go mod download
go build ./cmd/mcphub
go build ./cmd/mcpbridge
```

涉及配置时，复制 `config.example.yaml`，设置所需环境变量，然后运行：

```bash
go run ./cmd/mcphub validate --config ./config.yaml
```

配置加载器会拒绝未知 YAML 字段、多文档、缺失 `${NAME}` 环境变量、非 HTTPS 的 public/issuer URL，以及不安全的远端 HTTP 后端。本地 HTTP 只有在 loopback 主机并显式设置 `allow_insecure_http: true` 时才接受。

### 代码与测试要求

- 生产代码改动应聚焦于当前需求，避免推测性抽象和无关重写。
- 保留 MCP wire contract 以及后端命名空间/URI 映射，不要静默扩大公开能力。
- 优先在可观察的行为边界添加测试：认证 claim、scope all-of 过滤、就绪、重载不变量、传输/请求头安全和路由/改写行为。
- 不要把凭证、Bearer token 或真实后端 URL 放入源码、fixture、日志或 Pull Request。
- Go 代码使用 `gofmt`。提交评审前运行相关包测试；完整本地检查可使用 Go 1.26 工具链执行 `go test ./...` 和 `go vet ./...`。

`internal/client` 集成测试启动临时 HTTPS 身份服务、真实网关和 MCP 后端。使用 `go test -race ./internal/client` 验证登录、凭证轮换、并发连接器与传输行为。macOS 上可显式运行以下检查，按终端提示在系统浏览器中完成授权、拒绝和取消；它使用临时 profile 和回环 consent 页面，discovery、换码及网关调用仍使用 HTTPS。

```bash
CGO_ENABLED=0 MCPHUB_BROWSER_QA=1 go test ./internal/client -run '^TestNativeBrowserLogin$' -v -timeout=8m
```

管理 UI 变更应在本机 Chrome 验证实际页面：导航、搜索/筛选、编辑器、错误/空状态、语言切换及窄屏布局。UI 由 Go 内嵌，无需前端构建步骤；修改静态资源后需要重新构建服务端。对每个 `internal/app/adminui/*.js` 文件使用 `node --input-type=module --check < "$script"` 做语法检查。

Windows CLI 通过 `.github/workflows/windows-cli.yml` 在原生 x64、ARM64 GitHub runner 上验证，CI 与发布共用此流程。检查覆盖登录/刷新/连接器、进程间锁、DACL 拒绝策略、文件替换与可执行程序命令。仅交叉编译不能验证 Windows 文件系统行为。

PostgreSQL 持久化检查需要允许安装 `citext` 的临时数据库。运行 `go test -race ./...` 前设置 `MCPHUB_TEST_POSTGRES_DSN`；测试会创建并清理自己的 schema，未设置该变量时会跳过 PostgreSQL 检查。CI 与发布流程自动提供 PostgreSQL。完整测试覆盖管理员 audience/scope 与 CSRF、数据库版本冲突和凭证加密、endpoint 限流及取消，以及连接器遇到 429 时不自动重放。

### Pull Request

- 标题应说明用户可见或运维可见的变化，保持聚焦。
- 说明问题、选择的行为、配置/API 影响和已知限制。
- 写明验证命令和结果；未运行的检查要说明原因。
- 行为、配置、安全语义或支持边界变化时，同时更新对应手册或参考页的中英文版本；项目概览和入口变化时再更新 README。
- 只有在请求确实影响时才更新 Issue/PR 模板或 release notes；不要为明确排除能力增加发布承诺。

### 文档变更

文档必须反映当前代码，而不是计划设计。保持各手册、参考页和 README 的中英文语义等价并确保语言链接可用。员工操作放入 `docs/user-guide*.md`，管理流程放入 `docs/admin-guide*.md`，详细字段和协议放入 `docs/configuration*.md`；README 保持项目概览和导航职责。

只在[支持环境展开的字段](docs/configuration.zh-CN.md#环境变量与-secret)使用占位符；`*_env` 填变量名，其余字段填示例字面值，不得包含真实凭证。完整示例聚焦单个部署场景，明确区分配置片段与完整文件，并为历史设计标注适用版本。修改完整示例后用实际加载器校验并检查 Markdown 链接；配置校验不代表外部集成验收。
