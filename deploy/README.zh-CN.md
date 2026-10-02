# 远程管理与数据库部署

> 本指南用于 v2.3.0 全新部署。内建账号与 Agent 设备授权需要 v2.3.0，v2.2.2 不包含这些功能。

[English](README.md) · [管理员手册](../docs/admin-guide.zh-CN.md) · [文档导航](../docs/README.zh-CN.md)

本目录对应 MCPHub v2.3.0 的远程管理与数据库部署能力。支持 **一个 MCPHub 实例 + SQLite 或 PostgreSQL**。PostgreSQL 提供独立数据库的备份、持久化和运维能力；本版不支持多个网关共享数据库后自动同步运行时配置。

本指南面向 v2.3.0 全新部署。默认配置启用本地管理台与 SQLite；团队远程管理使用本目录模板，增加 HTTPS 管理地址和管理员身份配置。启动管理台后逐个添加后端、分别配置凭证、测试连接，再发布工具。参见 [Vault](../docs/vault-accounts.zh-CN.md)和 [SSO](../docs/sso-and-user-management.zh-CN.md)可选配置。

已完成的 MCP POST 请求默认保留 30 天；可将 `admin.request_retention` 设置为 `24h`–`8760h`。管理员请求诊断页支持时间筛选和 NDJSON 导出，详见[请求历史与隐私边界](../docs/admin-guide.zh-CN.md#授权与请求诊断)。请保护数据库备份和导出文件：记录不含参数、结果和 Token，但查询索引仍含可读的身份与路由元数据。

| 方式 | 用途 | 配置 |
| --- | --- | --- |
| 本地管理 + SQLite | 本机开发、单机运维；需要内建账号登录 | [config.local.yaml](config.local.yaml)、[启动步骤](../docs/admin-guide.zh-CN.md#本地管理-ui) |
| 远程管理 + SQLite | 单机网关，经 HTTPS 访问管理 UI/API | [config.remote-sqlite.yaml](config.remote-sqlite.yaml) |
| 远程管理 + PostgreSQL | 企业单实例部署，数据库独立运维 | [config.remote-postgres.yaml](config.remote-postgres.yaml)、[Compose](compose.postgres.yaml) |
| 飞书 SSO + Vault + 个人 MCP 账号 | 联调起点，须完成真实租户验收 | [配置示例](config.feishu-vault.example.yaml) |

v2.3.0 服务端包包含这些模板，下载链接提供完全相同的文件。只运行二进制时，把选中的 YAML 保存为 `config.yaml`，后续使用 `--config config.yaml`，不要求完整源码。下面的 `deploy/...` 路径以完整仓库根目录为工作目录。

## 身份服务配置

默认使用 MCPHub 内建账号；不需要部署外部 OIDC。启动后，在服务器本机加载同一份部署环境并运行 `mcphub init-admin --config config.yaml`。首次管理员没有默认密码，网页不提供匿名初始化。企业统一登录可在管理台“身份服务”中同时配置 LDAP 与 OIDC，见[身份服务指南](../docs/enterprise-login.zh-CN.md)。本地账号与企业身份分别按组授权；高级 YAML 配置见 [SSO 专题](../docs/sso-and-user-management.zh-CN.md)。密码、MFA 与本机恢复见[内建账号](../docs/builtin-accounts.zh-CN.md)。

## 环境变量清单

按选择的模板准备以下变量。内建模式不设置 `MCPHUB_AUTH_ISSUER` 或管理员身份服务密钥。

| 模板 | 变量 | 用途 |
| --- | --- | --- |
| 默认、本地和远程 | `MCPHUB_PUBLIC_URL` | 完整 MCP HTTPS URL；签发者自动推导为同 origin 的 `/sso` |
| 全部托管模板 | `MCPHUB_CONFIG_KEY` | 固定 Base64 32 字节配置密钥；重启和备份均保管相同值 |
| 远程模板 | `MCPHUB_ADMIN_PUBLIC_URL` | 管理台 HTTPS origin，不包含路径 |
| PostgreSQL 主机部署 | `MCPHUB_DATABASE_URL` | 数据库 DSN；需要生产 TLS 与独立数据库凭证 |
| Compose PostgreSQL | `MCPHUB_POSTGRES_PASSWORD` | URL-safe 数据库密码，Compose 组成容器内 DSN |

企业身份、Vault 或纯 YAML 模板的变量只在选择这些高级选项时准备。用户与后端凭证在 UI 中分别配置，不设置固定全局后端密钥。

## 启动方式

设置公共参数（示例地址需替换）：

```bash
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
export MCPHUB_ADMIN_PUBLIC_URL=https://admin.example.com
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

然后在此目录设置所选模板需要的变量和数据库密码，运行：

```bash
# 为本示例使用 URL-safe 密码，例如 openssl rand -hex 24 的结果。
export MCPHUB_POSTGRES_PASSWORD=REPLACE_WITH_GENERATED_HEX_PASSWORD
docker compose -f deploy/compose.postgres.yaml config --quiet
docker compose -f deploy/compose.postgres.yaml up -d --build
```

Compose 将数据保留在 `postgres-data` 卷中，不发布数据库端口，只将网关的 HTTP 端口绑定到宿主机回环。示例数据库连接在私有容器网络中使用 `sslmode=disable`；独立/托管数据库部署应使用 `verify-full`。不要使用 `down -v` 删除需要保留的数据。

若构建下载依赖时报 `x509: certificate signed by unknown authority`，检查企业 HTTPS 代理的证书链。宿主机信任私有 CA，不代表构建镜像和运行镜像也信任；由运维将组织 CA 安装到对应镜像的信任库后重试。也可按上面的二进制方式部署，身份服务、Vault 和数据库的证书信任仍需分别检查。

### 员工接入的前置条件

远程模板已经启用个人授权中心，并自动注册门户和 MCPBridge 客户端。先在本机初始化管理员，在管理台创建用户、添加服务、显式发布只读工具并配置相应 Scope 和服务授权。需要严格客户端授权时，再按[客户端授权步骤](../docs/admin-guide.zh-CN.md#启用客户端授权)设置 Grant 要求。门户位于 MCP 域名的 `/client-auth/`，HTTPS 代理必须转发整个 MCP 域名，不能只转发 `/mcp`。完成这些步骤后，再向员工交付 `setup` 命令。

在服务器本机的另一个终端加载同一份环境，运行 `mcphub init-admin --config config.yaml` 后登录。Compose 部署在容器中执行 `docker compose -f deploy/compose.postgres.yaml exec mcphub mcphub init-admin --config /etc/mcphub/config.yaml`；命令交互式读取密码。

## HTTPS 入口

在可信反向代理上终止 TLS。将管理域名的**全部路径**转发到管理员监听端口，保留原始 `Host`；将 MCP 域名转发到 MCP 端口。管理 origin 必须与 `admin.public_url` 完全一致，不带路径和尾部 `/`。浏览器来源限制及 CSRF 检查不信任代理传入的用户身份头。

提供了宿主机 [Caddyfile](Caddyfile) 示例；设置 `MCPHUB_HUB_HOST=hub.example.com`、`MCPHUB_ADMIN_HOST=admin.example.com`，配置 DNS 和证书后运行已有 Caddy。也可使用已有的 Nginx/Ingress。HTTP 上游端口必须受回环或私有网络保护，外部客户端只使用 HTTPS。按需要在代理层限制探针、登录速率与来源网络。

## 启动后验收

1. 在实际服务环境运行 `mcphub validate --config ...`。它校验配置和适用的数据库内容，不连接业务后端或完成身份登录，也不替代 SSO/Vault 的运行时凭证检查。
2. 启动后从可信网络检查 MCP 监听器的 `/healthz` 和 `/readyz`。就绪包含身份验证器和 required 后端；optional 后端故障仍可能返回 ready，应在管理页逐个探测。
3. 通过 HTTPS 管理入口登录，新增一个服务并发布已审核的只读工具；用普通用户完成登录/客户端授权，再实际调用该工具。用户权限和管理员权限分别验证。
4. 如启用 SSO/Vault，验证真实身份的登录、个人账号连接与撤销；需要写工具时再验证审批。`configuration valid`、健康检查或模板示例本身不能证明这些集成已验收。

## 浏览器与 CLI 用法

打开管理员 HTTPS 地址，使用本机初始化的管理员账号登录。进入「用户与组」创建用户和组，在组上分配角色和工具权限，再把用户加入组；添加后端后逐个配置地址与认证凭证，测试连接，再发布工具。

用户连接器使用默认已登记的 `mcpbridge`；远程管理 CLI 使用默认已登记的 `mcpbridge-admin`：

```bash
mcpbridge login --admin --server https://admin.example.com --client-id mcpbridge-admin --profile ops
mcpbridge admin --profile ops get /overview
```

管理 API 的 Bearer Token 必须具有管理 audience 和管理员 scope；普通 MCP Token 不提供管理权限。浏览器写请求还要求正确 Origin 和会话 CSRF。密码重置、停用和 MFA 绑定会撤销相关会话。企业账号在企业身份服务管理密码；默认本地管理员保留用于恢复。

## 数据和权限边界

两种数据库都以事务保存配置和审计，Header/OAuth secret 使用相同 AES-256-GCM 加密；SQLite 文件保持 0600。审计 actor 为已认证用户的内部 subject；无身份的高级本地管理记录为 `local`，后台刷新为 `system`。审计是配置变更历史，不是不可篡改的合规日志或所有 HTTP 请求日志。

备份数据库并单独保管 `MCPHUB_CONFIG_KEY`。新建部署时选择数据库驱动；工具组和 OpenAPI 通过 UI/API 管理。网关保持单活。远程 MCP endpoint 支持[个人上游账号](../docs/vault-accounts.zh-CN.md)。外部身份服务签发的 JWT 撤销依赖供应商；Hub 自管 SSO 还会校验当前本地会话与用户权限。退出 Hub 不等于撤销上游业务账号或身份服务的全局会话。

MCP backend 和 HTTP 工具组可在管理 UI 的“限流策略”中设置 `requests_per_second`、`burst`、`max_concurrent`，也可通过管理 API 的 `rate_limit` 对象保存。默认全为 0、不限流；所有用户共享 endpoint 额度。策略保存在所选数据库，实时计数在当前进程内。详见[限流语义](../docs/configuration.zh-CN.md#endpoint-限流)。

## Agent 链接授权

内建签发者部署可将用户登录与客户端同意合并在一个网页中。本机、SSH 或容器上的 Agent 都可以使用，不需要浏览器回调到 Agent 机器。使用同一系统用户和同一 `MCPHUB_HOME` 私有目录执行：

```bash
mcpbridge pair start --server https://hub.example.com/mcp --profile work --name "项目助手" --json
mcpbridge pair finish --request pr_example --wait --json
mcpbridge connect --profile work --client ci_example
```

替换实际返回的请求与客户端 ID。向用户展示 `verification_uri_complete` 和 `user_code`；用户核对配对码后，以本地账号、LDAP 或 OIDC 登录，选择服务、工具、资源限制和期限，再确认。工具默认不勾选，写能力默认关闭。无 `--wait` 时只检查一次；`pending_user` 仍需用户确认，`ready` 表示私有凭证保存与 MCP 连接检查完成。默认申请最长 1 小时，受网关上限约束；申请 5 分钟到期，轮询初始间隔 5 秒。`--ttl` 接受秒数（至少 60），`--endpoint`、重复 `--tool` / `--scope` 可收窄请求。

Agent 没有命令执行能力时，将 stdio 连接参数设为：

```json
["connect", "--server", "https://hub.example.com/mcp", "--profile", "work", "--name", "项目助手", "--interactive-auth"]
```

会话立即初始化，仅开放 `mcpbridge_auth_start` 与 `mcpbridge_auth_status`。前者复用同一个未过期申请；后者可能领取、保存凭证并检查连接，按结果的 `interval` 调用。ready 后刷新工具列表；不支持 `notifications/tools/list_changed` 时改用返回的 `connect --profile … --client …` 重新连接。失败的业务调用不会排队或自动重试，撤销或到期后需明确重新授权。

每次配对只授权一个服务及工具能力；提示词、资源 URI 或订阅使用原有向导。权限受当前组和已确认范围共同约束，新工具不会自动扩权。所有私有凭证留在 MCPBridge，禁止复制 Token 给 Agent。失败不会覆盖原有可用 profile；换用户或服务器需另建 profile。纯外部签发者继续使用 PKCE 登录与 setup。
