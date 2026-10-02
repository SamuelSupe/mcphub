# MCPHub v2.2.2 — Fresh deployments with MCPBridge / 新部署与 MCPBridge

MCPHub now starts with a management console, SQLite and an empty service list. Administrators add each backend with its own address, authentication, tool publication and access policy. The user connector is **MCPBridge** (`mcpbridge`); the gateway server is **`mcphub`**.

默认启动本地管理台、SQLite 和空服务列表。管理员为每个后端分别配置地址、认证凭证、工具发布及权限。用户连接器为 **MCPBridge**（`mcpbridge`），网关服务端为 **`mcphub`**。

| Role / 用途 | Executable / 程序 | Download prefix / 下载包前缀 | Commands / 命令 |
| --- | --- | --- | --- |
| Gateway server / 网关服务端 | `mcphub` | `mcphub_v2.2.2_*` | `serve`, `validate`, `verify-audit` |
| Agent connector / Agent 连接器 | `mcpbridge` / `mcpbridge.exe` | `mcpbridge_v2.2.2_*` | `setup`, `login`, `connect`, `doctor`, `client`, `broker`, `admin` |

## Fresh installation / 全新安装

1. Download the server package for your OS/CPU, compare its SHA-256 with `SHA256SUMS`, and extract into a new directory.
2. Copy `config.example.yaml` to `config.yaml`. Set **only** `MCPHUB_PUBLIC_URL`, `MCPHUB_AUTH_ISSUER` and `MCPHUB_CONFIG_KEY`. Generate a Base64-encoded 32-byte configuration key once and retain it with the database. The default has no preconfigured backend.
3. Run `mcphub --version`, `mcphub validate --config config.yaml`, then `mcphub serve --config config.yaml`. Open `http://127.0.0.1:8081/` on the gateway machine.
4. **Start the console → add backends → test connections → publish tools.** Configure each service's own URL and Header/OAuth credentials. Publish reviewed `read` tools and assign scopes, then make a real authenticated MCP call. Restart with the same key to retain the configuration.
5. For team access, select `deploy/config.remote-sqlite.yaml` or `deploy/config.remote-postgres.yaml` and register the administrator's HTTPS origin, identity client and scopes. The local console is loopback-only. Console startup does not establish OIDC readiness or authorize MCP clients.
6. End users install the MCPBridge package and run `mcpbridge setup` after the administrator enables the user portal and publishes services. Source installation: `go install github.com/SamuelSupe/mcphub/v2/cmd/mcpbridge@v2.2.2`.

1. 下载对应系统和架构的服务端包，与 `SHA256SUMS` 核对后解压到新目录。
2. 将 `config.example.yaml` 复制为 `config.yaml`，只设置 **`MCPHUB_PUBLIC_URL`、`MCPHUB_AUTH_ISSUER`、`MCPHUB_CONFIG_KEY`**。配置密钥为 Base64 编码的 32 字节值，只生成一次并与数据库配套保管。默认不预设后端。
3. 执行 `mcphub --version`、`mcphub validate --config config.yaml`、`mcphub serve --config config.yaml`，在网关所在机器打开 `http://127.0.0.1:8081/`。
4. 按 **启动管理台 → 添加后端 → 测试连接 → 发布工具** 操作。为每个服务分别填写 URL、Header/OAuth 凭证，发布已审核的 `read` 工具并配置 scope，再实际执行一次已认证 MCP 调用。重启时保留同一密钥，恢复已保存配置。
5. 团队远程管理使用 `deploy/config.remote-sqlite.yaml` 或 `deploy/config.remote-postgres.yaml`，配置管理员 HTTPS origin、身份客户端与 scope。本地管理台只允许本机访问；能打开管理台不等于 OIDC 已就绪或用户已获工具权限。
6. 管理员启用用户门户并发布服务后，最终用户安装 MCPBridge 包并运行 `mcpbridge setup`。源码安装使用 `go install github.com/SamuelSupe/mcphub/v2/cmd/mcpbridge@v2.2.2`。

The independent advanced `deploy/config.yaml-only.example.yaml` covers deployments without a console. Fill each backend's actual fields directly in that file; no global backend credential parameter is introduced. New-deployment documentation does not include legacy configuration, variable or database migration procedures.

独立高级示例 `deploy/config.yaml-only.example.yaml` 用于无控制台部署，在文件中逐个填写后端实际字段，不引入全局后端密钥参数。当前指南只面向新部署，不包含旧配置、变量或数据库迁移流程。

## Release acceptance / 发行验收

Archives are prepared as a draft release. Before publication, acceptance uses the actual extracted archives: fresh startup with the three default variables, two backends with different credentials, connection probes, authenticated read calls and persistence after restart. Backend execution uses OrbStack; console interaction uses native Chrome. Archive contents, checksums, bilingual guides and GitHub Pages templates are checked against the same release source. CI additionally checks Linux race/vet, native Windows x64/ARM64 clients, JavaScript syntax and container builds.

发行包先保存为草稿。公开前从实际解压包验收：仅三个默认变量的新部署启动、两个不同凭证后端的连接测试、已认证只读调用及重启持久化。后端使用 OrbStack，管理台交互使用本机 Chrome。包内配置、校验和、中英文指南及 GitHub Pages 模板均与同一发行源码核对；CI 另检查 Linux race/vet、Windows x64/ARM64 原生客户端、浏览器脚本语法和容器构建。

Real enterprise identity/Feishu/Vault tenants require deployment-specific acceptance.

真实企业身份源、飞书与 Vault 租户仍需在部署环境验收。

[中文服务端安装](https://samuelsupe.github.io/mcphub/admin/install.html) · [English server installation](https://samuelsupe.github.io/mcphub/en/admin/install.html) · [中文客户端安装](https://samuelsupe.github.io/mcphub/install.html) · [English client installation](https://samuelsupe.github.io/mcphub/en/install.html)
