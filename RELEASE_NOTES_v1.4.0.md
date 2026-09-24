# MCPHub v1.4.0

Remote administration, SQLite/PostgreSQL configuration storage, and optional endpoint rate limits.

[English](#english) | [中文](#中文) · [Changes since v1.3.1](https://github.com/SamuelSupe/mcphub/compare/v1.3.1...v1.4.0)

## English

- Adds remote browser administration through an external OIDC issuer. Management uses its own JWT audience and required scopes, PKCE login, server-side token refresh, secure session cookies and CSRF protection. Local administration remains the default and stays loopback-only.
- Adds `mcphub-cli login --admin` and `mcphub-cli admin` for authenticated JSON management API access. Use a separate administrator profile; ordinary MCP profiles do not grant administrator access. Configuration history records the verified administrator subject.
- Supports SQLite for local deployments and PostgreSQL for a separately operated configuration database. Both retain encrypted Header/OAuth secrets, revision conflict checks and transactional audit history. This release supports **one gateway instance**, including PostgreSQL deployments.
- Adds per-endpoint `requests_per_second`, `burst` and `max_concurrent` settings in the UI/API and backend YAML. Limits are **off by default**. Callers share a backend's allowance; HTTP tools share their group's allowance. Rejections return HTTP 429 and a retry hint before upstream execution. Cancellation and unsubscribe remain available, and the CLI does not automatically replay a rejected call.
- Preserves active request counts and unchanged rate budgets across configuration changes. Policies persist in the selected database; counters stay in memory and reset on process restart.
- Updates both README languages, the security policy, contribution checks and deployment guides. Server archives include SQLite/PostgreSQL configurations, a PostgreSQL Compose example and a Caddy proxy example. All archives include security policy and release notes.

### Getting started

```bash
mcphub-cli login --admin --server https://admin.example.com \
  --client-id mcphub-admin-cli --profile ops
mcphub-cli admin --profile ops get /backends
```

Register the administrator OAuth clients and configure `admin.mode: remote`, its HTTPS origin and scopes first. See the [deployment guide](https://github.com/SamuelSupe/mcphub/blob/v1.4.0/deploy/README.md). User MCP clients continue to run `mcphub-cli connect --profile work`.

For a backend, add this optional policy; configure HTTP tool groups through the management UI/API:

```yaml
rate_limit:
  requests_per_second: 20
  burst: 40
  max_concurrent: 8
```

Omit the object or set all fields to zero for unlimited access. With a positive rate, a zero/omitted burst uses capacity 1. See [rate-limit semantics](https://github.com/SamuelSupe/mcphub/blob/v1.4.0/README.md#endpoint-rate-limits).

### Upgrade and boundaries

Keep the existing SQLite file, encryption key and CLI profiles when replacing binaries. Upgrades from v1.2.0–v1.3.1 need no manual SQLite schema conversion; existing local administration and unlimited endpoint defaults remain. Restart the server and refresh the UI. Restarting clears browser sessions and in-memory rate counters.

Changing `database_driver` does not migrate data. PostgreSQL requires a dedicated database/schema and the `citext` extension; retain the same encryption key for the data it protects. Configure an HTTPS reverse proxy and keep the upstream HTTP listeners private for remote management. Multi-instance coordination, per-user/IP quotas, detailed management RBAC and cross-database migration tooling are outside this release.

Release gates run the race suite with a real PostgreSQL database, vet, JavaScript syntax checks, container builds, and native Windows x64/ARM64 CLI checks. Interactive authorization with each deployment's identity provider still requires its own validation.

## 中文

- 新增外部 OIDC 身份服务支持的远程浏览器管理。管理入口使用独立 JWT audience 与管理员 scope，提供 PKCE 登录、服务端 Token 刷新、安全会话 Cookie 和 CSRF 校验。本地管理仍为默认模式，且仅允许回环访问。
- 新增 `mcphub-cli login --admin` 与 `mcphub-cli admin`，通过独立管理员 profile 访问 JSON 管理 API。普通 MCP profile 不授予管理权限；配置历史记录已验证的管理员 subject。
- 支持本机 SQLite 与独立运维的 PostgreSQL 配置数据库。两种后端都保留 Header/OAuth 凭证加密、版本冲突检查和事务审计。本版包括 PostgreSQL 部署在内均支持 **一个网关实例**。
- UI/API 与后端 YAML 新增 `requests_per_second`、`burst`、`max_concurrent`，**默认不限流**。调用者共享 backend 额度，HTTP tools 共享所属工具组额度。超限在上游执行前返回 HTTP 429 和重试提示；取消及退订保持可用，CLI 不自动重放被拒绝的调用。
- 配置变更保留活动请求计数及未变策略的额度。策略保存在所选数据库；计数位于内存中，进程重启后重置。
- 更新中英文 README、安全策略、贡献检查与部署指南。服务端归档附带 SQLite/PostgreSQL 配置、PostgreSQL Compose 与 Caddy 代理示例；所有归档附带安全策略及发行说明。

### 使用方式

```bash
mcphub-cli login --admin --server https://admin.example.com \
  --client-id mcphub-admin-cli --profile ops
mcphub-cli admin --profile ops get /backends
```

先注册管理员 OAuth 客户端，配置 `admin.mode: remote`、HTTPS 管理地址及权限，详见[部署指南](https://github.com/SamuelSupe/mcphub/blob/v1.4.0/deploy/README.zh-CN.md)。普通 MCP 客户端继续启动 `mcphub-cli connect --profile work`。

后端可添加上述 `rate_limit` YAML，HTTP 工具组通过管理 UI/API 设置。省略该对象或将所有字段设为 0 即不限流；启用正速率时，`burst` 为 0 或省略表示容量为 1。详见[限流语义](https://github.com/SamuelSupe/mcphub/blob/v1.4.0/README.zh-CN.md#endpoint-限流)。

### 升级与边界

替换二进制时保留现有 SQLite 文件、加密密钥与 CLI profile。从 v1.2.0–v1.3.1 升级无需手动转换 SQLite schema；默认仍是本地管理和不限流。重启服务并刷新 UI；重启会清除浏览器会话和内存限流计数。

切换 `database_driver` 不会迁移数据。PostgreSQL 需要独立数据库/schema 和 `citext` 扩展，已有加密数据必须保留对应密钥。远程管理需配置 HTTPS 反向代理，内部 HTTP 监听器保持私有。本版不包含多实例协调、按用户/IP 配额、细粒度管理 RBAC 或跨数据库迁移工具。

发布检查包含真实 PostgreSQL 下的 race 测试、vet、JavaScript 语法、容器构建及原生 Windows x64/ARM64 CLI 检查；各部署实际使用的身份服务仍需验证交互授权配置。

## Downloads / 下载

[All platform archives / 全平台归档](https://github.com/SamuelSupe/mcphub/releases/tag/v1.4.0) · [SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v1.4.0/SHA256SUMS)

Server: macOS/Linux amd64 and arm64. CLI: macOS/Linux amd64 and arm64, plus Windows x64 and ARM64. Verify the checksum before extracting. The Compose example builds from source; use a checkout of tag `v1.4.0` for that workflow.

服务端提供 macOS/Linux amd64、arm64；CLI 另提供 Windows x64、ARM64。解压前校验 SHA-256。Compose 示例从源码构建，请检出 `v1.4.0` tag 后使用。
