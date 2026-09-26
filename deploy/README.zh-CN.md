# 远程管理与数据库部署

本目录对应 MCPHub v2.0.0 的远程管理与数据库部署能力。支持 **一个 MCPHub 实例 + SQLite 或 PostgreSQL**。PostgreSQL 提供独立数据库的备份、持久化和运维能力；本版不支持多个网关共享数据库后自动同步运行时配置。

v2.0.0 包含显式发布、审批、Broker、接入向导、SSO 用户权限和新版控制台，使用 schema 7。部署仍限单实例；替换 v1.4.0 前请按[升级与回滚流程](../RELEASE_NOTES_v2.0.0.md)备份并检查兼容性。SSO/用户目录将 schema 6 升级到 7；参见 [SSO 部署与组织同步](../docs/sso-and-user-management.zh-CN.md)。

| 方式 | 用途 | 配置 |
| --- | --- | --- |
| 本地管理 + SQLite | 本机开发、单机运维；不需要管理员登录 | 主 README 的本地管理示例 |
| 远程管理 + SQLite | 单机网关，经 HTTPS 访问管理 UI/API | [config.remote-sqlite.yaml](config.remote-sqlite.yaml) |
| 远程管理 + PostgreSQL | 企业单实例部署，数据库独立运维 | [config.remote-postgres.yaml](config.remote-postgres.yaml)、[Compose](compose.postgres.yaml) |

## 身份服务配置

MCPHub 使用已有 `auth.issuer`。在该 OIDC 服务中配置：

1. 浏览器管理员客户端：授权码 + PKCE S256；回调为 `https://admin.example.com/auth/callback`。默认是公开客户端。如需机密客户端，设置 `admin.client_secret_env`，支持 `client_secret_basic` 和 `client_secret_post`。
2. CLI 管理员客户端：预先注册的公开客户端，无 client secret；回调为 `http://127.0.0.1:PORT/oauth/callback`。身份服务不接受随机端口时，注册固定端口并使用 `--callback-port`。
3. 为指定管理员授予 `mcphub:admin`（可通过 `admin.required_scopes` 改名；多个 scope 必须全部满足）。权限取自 JWT 的 `scope`/`scp`，不读取 `roles`/`groups`，普通 MCP 用户不会自动获得管理员权限。
4. 签发可验签的 JWT **access token**，`aud` 包含完整的 `admin.public_url`，如 `https://admin.example.com`，不能只配置 MCP 的 `/mcp` audience。还需有效的 `iss`、`sub`、`exp`。授权、换码和刷新请求均携带此 `resource`。
5. 如需续期，启用刷新授权并声明 `offline_access`。浏览器令牌仅在服务端内存中保存；浏览器只持有 Secure/HttpOnly/SameSite cookie。会话最长 8 小时，服务重启后需重新登录。

这是一种管理员权限，获得该权限后可以管理全部后端、工具组、接口和导入。尚不包含租户隔离、只读管理员、细粒度管理 RBAC、远程进程/主机管控或 IDP 用户管理。

## 启动方式

设置公共参数（示例地址需替换）：

```bash
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
export MCPHUB_ADMIN_PUBLIC_URL=https://admin.example.com
export MCPHUB_ADMIN_CLIENT_ID=mcphub-admin-web
export MCPHUB_AUTH_ISSUER=https://idp.example.com
# 从 secret store 注入 MCPHUB_CONFIG_KEY：Base64 编码的 32 字节密钥。
# 密钥只生成一次（openssl rand -base64 32），升级与重启必须保留。
```

SQLite：

```bash
mcphub validate --config deploy/config.remote-sqlite.yaml
mcphub serve --config deploy/config.remote-sqlite.yaml
```

相对数据库路径以 YAML 所在目录为准。保留原来的 `admin.database_path` 和密钥即可继续使用现有 SQLite 数据，无需转换表结构。要沿用本地管理模式，保留 `mode: local`（默认值）；该模式无登录，仍只允许数字回环地址，禁止通过代理对外暴露。

外部 PostgreSQL：

```bash
# 通过环境或 secret store 设置连接串，不要提交到 Git。
export MCPHUB_DATABASE_URL='postgres://mcphub:URL_ENCODED_PASSWORD@db.example.com:5432/mcphub?sslmode=verify-full&sslrootcert=/path/to/db-ca.pem'
mcphub validate --config deploy/config.remote-postgres.yaml
mcphub serve --config deploy/config.remote-postgres.yaml
```

为 MCPHub 使用独立数据库/专用 schema。数据库用户需要创建/升级表的权限；`citext` 扩展由数据库管理员预先执行 `CREATE EXTENSION IF NOT EXISTS citext`，或允许应用在首次启动时创建。连接串、CA 和加密密钥必须在服务环境中可用。`validate` 只读，不创建表；首次 `serve` 建表并导入 YAML backends。

也可使用仓库中的 PostgreSQL Compose 示例：

```bash
# 为本示例使用 URL-safe 密码，例如 openssl rand -hex 24 的结果。
export MCPHUB_POSTGRES_PASSWORD=REPLACE_WITH_GENERATED_HEX_PASSWORD
docker compose -f deploy/compose.postgres.yaml up -d --build
```

Compose 将数据保留在 `postgres-data` 卷中，不发布数据库端口，只将网关的 HTTP 端口绑定到宿主机回环。示例数据库连接在私有容器网络中使用 `sslmode=disable`；独立/托管数据库部署应使用 `verify-full`。不要使用 `down -v` 删除需要保留的数据。

## HTTPS 入口

在可信反向代理上终止 TLS。将管理域名的**全部路径**转发到管理员监听端口，保留原始 `Host`；将 MCP 域名转发到 MCP 端口。管理 origin 必须与 `admin.public_url` 完全一致，不带路径和尾部 `/`。浏览器来源限制及 CSRF 检查不信任代理传入的用户身份头。

提供了宿主机 [Caddyfile](Caddyfile) 示例；设置 `MCPHUB_HUB_HOST=hub.example.com`、`MCPHUB_ADMIN_HOST=admin.example.com`，配置 DNS 和证书后运行已有 Caddy。也可使用已有的 Nginx/Ingress。HTTP 上游端口必须受回环或私有网络保护，外部客户端只使用 HTTPS。按需要在代理层限制探针、登录速率与来源网络。

## 浏览器与 CLI 用法

浏览器打开 `https://admin.example.com`，点击“使用身份服务登录”。登录后可新增/编辑/启停后端、管理 HTTP 工具组和 OpenAPI，并查看包含 OIDC subject 的变更记录。“退出登录”结束当前 MCPHub 浏览器会话，不会退出身份服务的 SSO 会话。

CLI 使用独立管理员 profile，避免覆盖 MCP 客户端凭证：

```bash
mcphub-cli login --admin --server https://admin.example.com \
  --client-id mcphub-admin-cli --profile ops --callback-port 8400
mcphub-cli admin --profile ops get /overview
mcphub-cli admin --profile ops get /backends
mcphub-cli admin --profile ops get /tool-groups
mcphub-cli admin --profile ops get '/events?limit=50'
```

`admin` 是管理 API 的 JSON 客户端，路径相对于 `/api/v1`，支持 `get/post/put/delete`，全部 flags 放在方法之前；后端、HTTP tools、导入/刷新均沿用主 README 中的 API 路径。JSON body 使用 `--file FILE`（`-` 表示 stdin）；输出 JSON 到 stdout，ETag 到 stderr，不导出 Token。请求上限 6 MiB，与 OpenAPI 上传一致。

例如创建一个停用的后端，将以下内容保存为 `backend.json`：

```json
{"id":"crm","url":"https://crm.example.com/mcp","enabled":false,"required":false,"required_scopes":["mcp:crm.read"],"headers":[]}
```

```bash
mcphub-cli admin --profile ops --file backend.json post /backends
mcphub-cli admin --profile ops get /backends/crm
# 把 backend.json 的 enabled 改为 true，使用刚读取的 ETag（此处仅示例）
mcphub-cli admin --profile ops --file backend.json --if-match '"1"' put /backends/crm
mcphub-cli admin --profile ops post /backends/crm/probe
mcphub-cli status --profile ops
mcphub-cli logout --profile ops
```

PUT 使用完整输入对象；保留 Header 时提供名称并省略 `value`，保留 OAuth secret 时省略 `client_secret`。不要直接将包含 runtime/revision/脱敏标记的 GET 响应作为 PUT 输入。失效 ETag 返回 409，不覆盖他人的变更。网络失败不重放写入；401 最多刷新并重试一次。普通 MCP 客户端继续使用 `mcphub-cli connect --profile work`，不能使用管理员 profile。

## 数据和权限边界

两种数据库都以事务保存配置和审计，Header/OAuth secret 使用相同 AES-256-GCM 加密；SQLite 文件保持 0600。审计 actor 为远程管理员 JWT `sub`；本地操作为 `local`，后台刷新为 `system`。审计是配置变更历史，不是不可篡改的合规日志或所有 HTTP 请求日志。

备份数据库并单独保管 `MCPHUB_CONFIG_KEY`。更换 `database_driver` 不会自动迁移原数据，尤其工具组和 OpenAPI 不存在于 YAML 中。切换存储需要单独的数据迁移；不要把空 PostgreSQL 当作已有 SQLite 的副本。多实例协调、跨库迁移工具、集中会话撤销及第三方后端的交互授权不在本版范围内。退出不会撤销已签发的 JWT；权限撤销依赖短期 access token、刷新授权及身份服务策略。

MCP backend 和 HTTP 工具组可在管理 UI 的“限流策略”中设置 `requests_per_second`、`burst`、`max_concurrent`，也可通过管理 API 的 `rate_limit` 对象保存。默认全为 0、不限流；所有用户共享 endpoint 额度。策略保存在所选数据库，实时计数在当前进程内。详见[限流语义](../README.zh-CN.md#endpoint-限流)。
