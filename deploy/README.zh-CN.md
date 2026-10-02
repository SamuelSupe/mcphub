# 远程管理与数据库部署

[English](README.md) · [管理员手册](../docs/admin-guide.zh-CN.md) · [文档导航](../docs/README.zh-CN.md)

本目录对应 MCPHub v2.2.2 的远程管理与数据库部署能力。支持 **一个 MCPHub 实例 + SQLite 或 PostgreSQL**。PostgreSQL 提供独立数据库的备份、持久化和运维能力；本版不支持多个网关共享数据库后自动同步运行时配置。

本指南面向 v2.2.2 全新部署。默认配置启用本地管理台与 SQLite；团队远程管理使用本目录模板，增加 HTTPS 管理地址和管理员身份配置。启动管理台后逐个添加后端、分别配置凭证、测试连接，再发布工具。参见 [Vault](../docs/vault-accounts.zh-CN.md)和 [SSO](../docs/sso-and-user-management.zh-CN.md)可选配置。

已完成的 MCP POST 请求默认保留 30 天；可将 `admin.request_retention` 设置为 `24h`–`8760h`。管理员请求诊断页支持时间筛选和 NDJSON 导出，详见[请求历史与隐私边界](../docs/admin-guide.zh-CN.md#授权与请求诊断)。请保护数据库备份和导出文件：记录不含参数、结果和 Token，但查询索引仍含可读的身份与路由元数据。

| 方式 | 用途 | 配置 |
| --- | --- | --- |
| 本地管理 + SQLite | 本机开发、单机运维；不需要管理员登录 | [config.local.yaml](config.local.yaml)、[启动步骤](../docs/admin-guide.zh-CN.md#本地管理-ui) |
| 远程管理 + SQLite | 单机网关，经 HTTPS 访问管理 UI/API | [config.remote-sqlite.yaml](config.remote-sqlite.yaml) |
| 远程管理 + PostgreSQL | 企业单实例部署，数据库独立运维 | [config.remote-postgres.yaml](config.remote-postgres.yaml)、[Compose](compose.postgres.yaml) |
| 飞书 SSO + Vault + 个人 MCP 账号 | 联调起点，须完成真实租户验收 | [配置示例](config.feishu-vault.example.yaml) |

v2.2.2 服务端包已包含这些模板，下载链接提供完全相同的文件。只运行二进制时，把选中的 YAML 保存为 `config.yaml`，后续使用 `--config config.yaml`，不要求完整源码。下面的 `deploy/...` 路径以完整仓库根目录为工作目录。

## 身份服务配置

外部身份服务模式使用已有 `auth.issuer`；Hub 身份桥接及本地用户权限参见 [SSO 指南](../docs/sso-and-user-management.zh-CN.md)。外部 OIDC 服务需配置：

1. 浏览器管理员客户端：授权码 + PKCE S256；回调为 `https://admin.example.com/auth/callback`。默认是公开客户端。如需机密客户端，设置 `admin.client_secret_env`，支持 `client_secret_basic` 和 `client_secret_post`。
2. CLI 管理员客户端：预先注册的公开客户端，无 client secret；回调为 `http://127.0.0.1:PORT/oauth/callback`。身份服务不接受随机端口时，注册固定端口并使用 `--callback-port`。
3. 为指定管理员授予 `mcphub:admin`（可通过 `admin.required_scopes` 改名；多个 scope 必须全部满足）。权限取自 JWT 的 `scope`/`scp`，不读取 `roles`/`groups`，普通 MCP 用户不会自动获得管理员权限。
4. 签发可验签的 JWT **access token**，`aud` 包含完整的 `admin.public_url`，如 `https://admin.example.com`，不能只配置 MCP 的 `/mcp` audience。还需有效的 `iss`、`sub`、`exp`。授权、换码和刷新请求均携带此 `resource`。
5. 如需续期，启用刷新授权并声明 `offline_access`。浏览器令牌仅在服务端内存中保存；浏览器只持有 Secure/HttpOnly/SameSite cookie。会话最长 8 小时，服务重启后需重新登录。

这是一种管理员权限，获得该权限后可以管理全部后端、工具组、接口和导入。尚不包含租户隔离、只读管理员、细粒度管理 RBAC、远程进程/主机管控或 IDP 用户管理。

## 环境变量清单

先选一个示例，不需要把所有集成都配置一遍。远程 SQLite/PostgreSQL 示例仅打开管理控制台；员工的 `mcpbridge setup` 还需要按[管理员手册](../docs/admin-guide.zh-CN.md#启用客户端授权)启用 `client_authorization` 并注册门户/CLI 客户端。下表对应两个 `config.remote-*` 模板及其 Compose；飞书综合示例的所需变量见[文件头部](config.feishu-vault.example.yaml)。

| 适用范围 | 变量 | 填写内容 |
| --- | --- | --- |
| 两个远程模板 | `MCPHUB_PUBLIC_URL` | 完整 MCP HTTPS URL，例如 `https://hub.example.com/mcp` |
| 两个远程模板 | `MCPHUB_AUTH_ISSUER` | 外部 OIDC issuer；使用 Hub SSO 时按专题改为本网关 `/sso` |
| 两个远程模板 | `MCPHUB_ADMIN_PUBLIC_URL` | 管理 HTTPS origin，例如 `https://admin.example.com`，不带尾部 `/` |
| 两个远程模板 | `MCPHUB_ADMIN_CLIENT_ID` | 已注册的管理员浏览器客户端 ID，不是 CLI/用户门户 ID |
| 两个远程模板 | `MCPHUB_CONFIG_KEY` | 固定的 Base64 编码 32 字节密钥，重启保留原值 |
| 直接连接 PostgreSQL | `MCPHUB_DATABASE_URL` | 可连接的 DSN；外部数据库使用 TLS，密码需按 URL 编码 |
| Compose 内置 PostgreSQL | `MCPHUB_POSTGRES_PASSWORD` | URL-safe 密码；Compose 自动生成容器内 `MCPHUB_DATABASE_URL` |
| 宿主机 Caddy | `MCPHUB_HUB_HOST`、`MCPHUB_ADMIN_HOST` | 仅域名，分别注入 **Caddy 进程**；不要填写 URL 或 `/mcp` |

MCPHub YAML 的 `${NAME}`、Compose 的 `${NAME:?message}`、Caddy 的 `{$NAME}` 是三种不同语法。MCPHub 不加载 `.env`；直接运行二进制时由服务管理器/当前 shell 注入变量。Compose 可用 `--env-file /secure/mcphub.env` 显式选择变量文件，示例 `environment` 再将指定值传入容器；Docker 的 `--env-file` 则直接注入容器环境。环境文件中的敏感值按所用工具的语法填写，限制文件权限并排除出版本控制。字段展开范围及 `*_env` 名称写法见[配置参考](../docs/configuration.zh-CN.md#环境变量与-secret)。

## 启动方式

设置公共参数（示例地址需替换）：

```bash
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
export MCPHUB_ADMIN_PUBLIC_URL=https://admin.example.com
export MCPHUB_ADMIN_CLIENT_ID=mcphub-admin-web
export MCPHUB_AUTH_ISSUER=https://idp.example.com
umask 077
mkdir -p "$HOME/.config/mcphub"
test -f "$HOME/.config/mcphub/config.key" || openssl rand -base64 32 > "$HOME/.config/mcphub/config.key"
export MCPHUB_CONFIG_KEY="$(cat "$HOME/.config/mcphub/config.key")"
```

此密钥文件是新部署的本机示例；已有数据库必须注入原密钥。重启、服务管理器和容器都应使用同一值，并保留数据库与密钥备份。

SQLite：

```bash
mcphub validate --config deploy/config.remote-sqlite.yaml
mcphub serve --config deploy/config.remote-sqlite.yaml
```

相对数据库路径以 YAML 所在目录为准：直接使用仓库模板时是 `deploy/data/mcphub.db`，复制到其他目录后随文件位置变化。选择可写目录，重启保留相同加密密钥。

外部 PostgreSQL：示例监听 `:8080` / `:8081`，供容器使用。直接在宿主机运行前，复制为 `config.yaml`，将两个 `listen` 改为 `127.0.0.1:8080` / `127.0.0.1:8081` 或受保护的私有地址，然后执行：

```bash
# 通过环境或 secret store 设置连接串，不要提交到 Git。
export MCPHUB_DATABASE_URL='postgres://mcphub:URL_ENCODED_PASSWORD@db.example.com:5432/mcphub?sslmode=verify-full&sslrootcert=/path/to/db-ca.pem'
mcphub validate --config config.yaml
mcphub serve --config config.yaml
```

为 MCPHub 使用独立数据库/专用 schema。数据库用户需要创建表的权限；`citext` 扩展由数据库管理员预先执行 `CREATE EXTENSION IF NOT EXISTS citext`，或允许应用在首次启动时创建。连接串、CA 和加密密钥必须在服务环境中可用。`validate` 只读，不创建表；首次 `serve` 建表并导入 YAML backends。`validate` 仍会连接 PostgreSQL；数据库尚未创建、凭证错误或网络不通时不能通过。

### 使用 PostgreSQL Compose

Compose 示例会从源码构建 Hub，要求 Git、Docker 与 Compose v2，以及包含 `Dockerfile`、`go.mod`、`go.sum`、`cmd/`、`internal/` 的**完整源码目录**。二进制 Release 包中的 `deploy/` 不能单独构建，否则会报 `Dockerfile: no such file or directory`。先获取当前源码，或进入已有完整 checkout：

```bash
git clone https://github.com/SamuelSupe/mcphub.git
cd mcphub
```

然后在此目录设置上面的 5 个变量和数据库密码，运行：

```bash
# 为本示例使用 URL-safe 密码，例如 openssl rand -hex 24 的结果。
export MCPHUB_POSTGRES_PASSWORD=REPLACE_WITH_GENERATED_HEX_PASSWORD
docker compose -f deploy/compose.postgres.yaml config --quiet
docker compose -f deploy/compose.postgres.yaml up -d --build
```

Compose 将数据保留在 `postgres-data` 卷中，不发布数据库端口，只将网关的 HTTP 端口绑定到宿主机回环。示例数据库连接在私有容器网络中使用 `sslmode=disable`；独立/托管数据库部署应使用 `verify-full`。不要使用 `down -v` 删除需要保留的数据。

若构建下载依赖时报 `x509: certificate signed by unknown authority`，检查企业 HTTPS 代理的证书链。宿主机信任私有 CA，不代表构建镜像和运行镜像也信任；由运维将组织 CA 安装到对应镜像的信任库后重试。也可按上面的二进制方式部署，身份服务、Vault 和数据库的证书信任仍需分别检查。

### 员工接入的前置条件

远程模板的首次启动只提供管理平台，尚未启用用户接入向导。按[客户端授权步骤](../docs/admin-guide.zh-CN.md#启用客户端授权)将 `client_authorization` 片段加入完整配置并重启，注册门户和员工 CLI 客户端，再通过管理端添加服务、显式发布只读工具并给用户相应 Scope。门户位于 MCP 域名的 `/client-auth/`，HTTPS 代理必须转发整个 MCP 域名，不能只转发 `/mcp`。完成这些步骤后，再向员工交付 `setup` 命令。

## HTTPS 入口

在可信反向代理上终止 TLS。将管理域名的**全部路径**转发到管理员监听端口，保留原始 `Host`；将 MCP 域名转发到 MCP 端口。管理 origin 必须与 `admin.public_url` 完全一致，不带路径和尾部 `/`。浏览器来源限制及 CSRF 检查不信任代理传入的用户身份头。

提供了宿主机 [Caddyfile](Caddyfile) 示例；设置 `MCPHUB_HUB_HOST=hub.example.com`、`MCPHUB_ADMIN_HOST=admin.example.com`，配置 DNS 和证书后运行已有 Caddy。也可使用已有的 Nginx/Ingress。HTTP 上游端口必须受回环或私有网络保护，外部客户端只使用 HTTPS。按需要在代理层限制探针、登录速率与来源网络。

## 启动后验收

1. 在实际服务环境运行 `mcphub validate --config ...`。它校验配置和适用的数据库内容，不连接业务后端或完成身份登录，也不替代 SSO/Vault 的运行时凭证检查。
2. 启动后从可信网络检查 MCP 监听器的 `/healthz` 和 `/readyz`。就绪包含身份验证器和 required 后端；optional 后端故障仍可能返回 ready，应在管理页逐个探测。
3. 通过 HTTPS 管理入口登录，新增一个服务并发布已审核的只读工具；用普通用户完成登录/客户端授权，再实际调用该工具。用户权限和管理员权限分别验证。
4. 如启用 SSO/Vault，验证真实身份的登录、个人账号连接与撤销；需要写工具时再验证审批。`configuration valid`、健康检查或模板示例本身不能证明这些集成已验收。

## 浏览器与 CLI 用法

浏览器打开 `https://admin.example.com`，点击“使用身份服务登录”。登录后可新增/编辑/启停后端、管理 HTTP 工具组和 OpenAPI，并查看包含 OIDC subject 的变更记录。“退出登录”结束当前 MCPHub 浏览器会话，不会退出身份服务的 SSO 会话。

CLI 使用独立管理员 profile，避免覆盖 MCP 客户端凭证：

```bash
mcpbridge login --admin --server https://admin.example.com \
  --client-id mcphub-admin-cli --profile ops --callback-port 8400
mcpbridge admin --profile ops get /overview
mcpbridge admin --profile ops get /backends
mcpbridge admin --profile ops get /tool-groups
mcpbridge admin --profile ops get '/events?limit=50'
```

`admin` 是管理 API 的 JSON 客户端，路径相对于 `/api/v1`，支持 `get/post/put/delete`，全部 flags 放在方法之前；后端、HTTP tools、导入/刷新均使用[配置参考中的 API 路径](../docs/configuration.zh-CN.md#工具组与托管-http-api-tool)。JSON body 使用 `--file FILE`（`-` 表示 stdin）；输出 JSON 到 stdout，ETag 到 stderr，不导出 Token。请求上限 6 MiB，与 OpenAPI 上传一致。

例如创建一个停用的后端，将以下内容保存为 `backend.json`：

```json
{"id":"example","url":"https://mcp.example.com/mcp","enabled":false,"required":false,"required_scopes":["mcp:example.read"],"headers":[]}
```

```bash
mcpbridge admin --profile ops --file backend.json post /backends
mcpbridge admin --profile ops get /backends/example
# 把 backend.json 的 enabled 改为 true，使用刚读取的 ETag（此处仅示例）
mcpbridge admin --profile ops --file backend.json --if-match '"1"' put /backends/example
mcpbridge admin --profile ops post /backends/example/probe
mcpbridge status --profile ops
mcpbridge logout --profile ops
```

PUT 使用完整输入对象；保留 Header 时提供名称并省略 `value`，保留 OAuth secret 时省略 `client_secret`。不要直接将包含 runtime/revision/脱敏标记的 GET 响应作为 PUT 输入。失效 ETag 返回 409，不覆盖他人的变更。网络失败不重放写入；401 最多刷新并重试一次。普通 MCP 客户端使用独立用户 profile；按[用户手册](../docs/user-guide.zh-CN.md)生成配置，不能使用管理员 profile。

## 数据和权限边界

两种数据库都以事务保存配置和审计，Header/OAuth secret 使用相同 AES-256-GCM 加密；SQLite 文件保持 0600。审计 actor 为远程管理员 JWT `sub`；本地操作为 `local`，后台刷新为 `system`。审计是配置变更历史，不是不可篡改的合规日志或所有 HTTP 请求日志。

备份数据库并单独保管 `MCPHUB_CONFIG_KEY`。新建部署时选择数据库驱动；工具组和 OpenAPI 通过 UI/API 管理。网关保持单活。远程 MCP endpoint 支持[个人上游账号](../docs/vault-accounts.zh-CN.md)。外部身份服务签发的 JWT 撤销依赖供应商；Hub 自管 SSO 还会校验当前本地会话与用户权限。退出 Hub 不等于撤销上游业务账号或身份服务的全局会话。

MCP backend 和 HTTP 工具组可在管理 UI 的“限流策略”中设置 `requests_per_second`、`burst`、`max_concurrent`，也可通过管理 API 的 `rate_limit` 对象保存。默认全为 0、不限流；所有用户共享 endpoint 额度。策略保存在所选数据库，实时计数在当前进程内。详见[限流语义](../docs/configuration.zh-CN.md#endpoint-限流)。
