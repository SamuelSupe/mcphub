# MCPHub 管理员手册

[English](admin-guide.md) · [文档导航](README.zh-CN.md) · [项目首页](../README.zh-CN.md)

[在线阅读：管理员文档站](https://samuelsupe.github.io/mcphub/admin/) · [在线用户指南](https://samuelsupe.github.io/mcphub/)

面向负责部署、服务接入、权限策略、审批及运行维护的管理员，对应 v2.2.2。员工电脑的安装与接入步骤见[用户手册](user-guide.zh-CN.md)。

推荐顺序：**启动管理台 → 添加后端 → 测试连接 → 发布工具 → 配置用户权限 → 验证实际调用**。

- [部署准备与安装](#部署准备与安装) · [本地管理 UI](#本地管理-ui) · [远程管理与数据库](#远程管理员与-postgresql)
- [登录客户端与上游凭证](#区分登录客户端与上游凭证)
- [服务接入与工具发布](#服务接入与工具发布) · [用户与组织](#用户与组织) · [客户端授权](#启用客户端授权) · [上游账号](#配置上游账号)
- [写审批与配置治理](#写审批与配置治理) · [控制台导航](#控制台导航) · [工具权限检查](#工具权限检查) · [授权与请求诊断](#授权与请求诊断)
- [备份与恢复](#备份与恢复) · [运行维护](#运行维护) · [安全说明](#安全说明) · [已知限制与排障](#已知限制与排障提示)

## 部署准备与安装

准备可用的身份服务、MCP 与管理端的 HTTPS 地址、一个持久化数据库，以及独立保存的配置加密密钥。网关保持**单活**；PostgreSQL 不提供 Hub 多实例运行时协调。

| 模式 | 适用场景 | 入口 |
| --- | --- | --- |
| 本地管理 + SQLite | 在网关所在机器开发或维护 | [下方最小配置](#本地管理-ui)，免登录且只绑定回环 |
| 远程管理 + SQLite / PostgreSQL | 团队使用、审批与集中管理 | [部署指南](../deploy/README.zh-CN.md)，独立管理员登录和 HTTPS |
| YAML 管理 | 不启用控制台、仅发布明确只读的工具 | [完整配置示例](configuration.zh-CN.md#从完整-yaml-示例启动) |

运行网关的机器安装 `mcphub`；用户电脑安装 `mcpbridge`。管理员使用管理 CLI 时也需安装后者。服务端下载：

| 平台 | 服务端下载 |
| --- | --- |
| macOS Intel | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.2/mcphub_v2.2.2_darwin_amd64.tar.gz) |
| macOS Apple Silicon | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.2/mcphub_v2.2.2_darwin_arm64.tar.gz) |
| Linux amd64 | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.2/mcphub_v2.2.2_linux_amd64.tar.gz) |
| Linux arm64 | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.2/mcphub_v2.2.2_linux_arm64.tar.gz) |

从 [v2.2.2 Release](https://github.com/SamuelSupe/mcphub/releases/tag/v2.2.2) 下载后核对 [SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.2/SHA256SUMS)。以下为 Linux arm64 的安装示例；按上表替换文件名，macOS 校验命令为 `shasum -a 256`：

```bash
sha256sum mcphub_v2.2.2_linux_arm64.tar.gz
# 与 SHA256SUMS 中同名条目逐字核对，一致后再解压。
mkdir -p mcphub-release "$HOME/.local/bin"
tar -xzf mcphub_v2.2.2_linux_arm64.tar.gz -C mcphub-release
install -m 755 mcphub-release/mcphub "$HOME/.local/bin/mcphub"
export PATH="$HOME/.local/bin:$PATH"
```

安装后运行 `mcphub --version`，应显示 `mcphub 2.2.2 (server)`。`validate`、`serve` 是服务端命令；`mcpbridge` 是用户连接器，供用户连接 Agent，不能启动或校验网关。源码更新不会自动替换目录里的旧二进制；要使用刚安装的程序路径。

此 PATH 设置用于当前终端；后续可直接运行 `"$HOME/.local/bin/mcphub"`，或将工具目录加入服务环境。已安装 Go 1.26 时也可执行：

```bash
go install github.com/SamuelSupe/mcphub/v2/cmd/mcphub@v2.2.2
```

Go 安装路径是 `go env GOBIN`，为空时是 `$(go env GOPATH)/bin`；该目录也需要加入 PATH。

**v2.2.2 服务端包与在线指南包含相同模板。** 默认 `config.example.yaml` 启用本地管理台、SQLite 和 `backends: []`，只需要网关地址、OIDC issuer 和配置加密密钥。启动管理台后，逐个添加服务，分别填写凭证、测试连接，再发布已审核的工具并配置权限。团队远程管理使用远程模板；不启用控制台时选择独立的[高级纯 YAML 示例](../deploy/config.yaml-only.example.yaml)。

`validate --config PATH` 校验配置，不创建 SQLite 数据库；`serve --config PATH` 初始化新数据库并启动服务，JSON 日志写到 stderr。

部署前先查[示例选择与配置生效方式](configuration.zh-CN.md#选择配置与生效方式)、[环境变量/Secret 写法](configuration.zh-CN.md#环境变量与-secret)和[部署变量清单](../deploy/README.zh-CN.md#环境变量清单)。不要把所有可选模块同时塞进基础配置。

## 本地管理 UI

默认模板适用于全新部署，管理台只监听网关所在机器的 `127.0.0.1:8081`，无需管理员登录。远程访问按[远程部署指南](../deploy/README.zh-CN.md)配置 HTTPS 和管理员身份。

解压服务端包后，把包内 `config.example.yaml` 复制为 `config.yaml`；也可下载[相同的在线模板](../config.example.yaml)。在新部署目录中执行，替换两个 HTTPS 地址：

```bash
cp mcphub-release/config.example.yaml config.yaml
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
export MCPHUB_AUTH_ISSUER=https://idp.example.com
umask 077
mkdir -p secrets
test -f secrets/config.key || openssl rand -base64 32 > secrets/config.key
export MCPHUB_CONFIG_KEY="$(cat secrets/config.key)"
mcphub validate --config config.yaml
mcphub serve --config config.yaml
```

默认模板只要求以上三个变量。`MCPHUB_CONFIG_KEY` 是 Base64 编码的 32 字节密钥，只生成一次，重启时从同一个私有文件读取。服务管理器也需要注入这三个变量；MCPHub 不自动加载 `.env`。数据库位于配置文件旁的 `data/mcphub.db`，需要可写目录。

打开[本地管理台](http://127.0.0.1:8081/)，初始服务列表为空。进入 **MCP 后端 → 添加 MCP 后端**，逐个填写地址与独立凭证，**测试连接**后保存，再到 **工具权限**设置明确的 `read` 分类、发布名单和所需 scope。按[服务接入流程](#服务接入与工具发布)实际调用一个只读工具。管理台启动不代表身份源可用；网关 `/readyz` 返回 200 还需要可用的 OIDC issuer。

## 远程管理员与 PostgreSQL

支持独立管理员登录、远程 UI/API、`mcpbridge admin` 与按管理员身份记录的配置审计。管理员 JWT 使用 `admin.public_url` 作为 audience，且必须具有 `admin.required_scopes`（默认 `mcphub:admin`）；普通 MCP 用户的登录凭证不会自动获得管理权限。

```bash
mcpbridge login --admin --server https://admin.example.com --client-id mcphub-admin-cli --profile ops
mcpbridge admin --profile ops get /overview
mcpbridge admin --profile ops get /backends
mcpbridge admin --profile ops get /tool-groups
mcpbridge admin --profile ops get /events
```

数据库可选 SQLite（默认）或 PostgreSQL（`database_driver: postgres` 与 `database_dsn_env`）。本版支持单实例网关；PostgreSQL 不代表已支持多实例运行时同步。完整的 OIDC 注册、浏览器登录、API 写入、数据库与 HTTPS 代理部署见 [部署指南](../deploy/README.zh-CN.md)。

## 服务接入与工具发布

1. 在「MCP 后端」新增 Streamable HTTP 服务，或在「HTTP 工具组」组织 REST API / OpenAPI 工具。
2. 设置连接和上游凭证，检查连接或导入结果。工具组的启用状态本身不证明上游连通。
3. 显式发布审核过的工具，使用后端原始工具名填写 `published_tools`；新发现的工具不会自动开放。HTTP 工具通过工具/导入编辑器管理发布状态。
4. 在「工具权限」中确定 `read` / `write`、所需 Scope 和业务资源范围。多个 Scope 要全部满足；参数资源限制需要配合后端自身授权。
5. 先用一个已授权用户和只读工具验证，再配置写审批和更大的范围。

上游 Header/OAuth 是 Hub 调用业务服务的凭证；客户端 Scope 是用户访问 Hub 的权限，两者分别配置。修改配置采用版本检查；遇到 `409 revision_conflict` 时重新读取并核对，不要覆盖他人的修改。启用配置审批后，提交成功可能是待审批，需获批才会应用。

字段和 API 见[后端配置](configuration.zh-CN.md#backends)、[HTTP 工具组](configuration.zh-CN.md#工具组与托管-http-api-tool)、[发布与资源规则](configuration.zh-CN.md#显式发布与资源范围)、[限流](configuration.zh-CN.md#endpoint-限流)。

## 用户与组织

选择身份模式后再分配权限：直接使用外部 OIDC 时由身份服务签发 Scope；启用 `auth.sso` 桥接时，由 MCPHub 管理用户、部门/组的本地授权。上游企业应用密钥只保留在服务器。

在「用户与组织」中启用用户，分配 endpoint、精确工具、Scope、资源范围和必要角色。新 SSO 用户默认待授权。部门/组同步只更新身份和成员关系，不授予本地权限；离职处理需要可靠的目录同步。管理员与审批人角色分别授予。

完整配置、目录快照契约及最后一位管理员恢复见 [SSO 与用户管理](sso-and-user-management.zh-CN.md)。向员工交付 MCP URL、CLI client ID、可用服务和必要回调端口，并提供[用户接入步骤](user-guide.zh-CN.md#接入-mcp-客户端)。

## 区分登录客户端与上游凭证

同名的 `client_id` 属于不同认证流程，应分别注册和填写；以下域名均需替换。Hub 用户 Token 的 audience 是完整 `server.public_url`，管理员 Token 的 audience 是 `admin.public_url`，二者不能互换。

| 用途 | 配置位置 | 注册回调示例 |
| --- | --- | --- |
| 员工 CLI 登录 Hub | CLI 的 `--client-id` | `http://127.0.0.1:PORT/oauth/callback` |
| 普通用户门户 | `client_authorization.client_id` | `https://hub.example.com/client-auth/auth/callback` |
| 管理员浏览器登录 | `admin.client_id` | `https://admin.example.com/auth/callback` |
| Hub 向企业 SSO 验证身份 | `auth.sso.upstream.client_id` | `https://hub.example.com/sso/callback` |
| 用户连接上游业务账号 | `backends[].credentials.oauth.client_id` | `https://hub.example.com/client-auth/api/accounts/callback` |

`backends[].oauth` 是 Hub 自己的服务账号 OAuth `client_credentials`，没有浏览器回调；`credentials.oauth` 是用户个人授权，两者互斥。Hub 的 `required_scopes` 和上游的 `oauth.scopes` 分别由各自身份服务解释，不会自动映射或相互授予。

## 启用客户端授权

启用 `client_authorization.enabled`、配置用户门户 `client_id`，并启用托管数据库。支持 SQLite 和 **单实例 MCPHub + PostgreSQL**，新数据库使用 schema 9，重启时保留数据库与匹配密钥。

在身份服务注册门户回调 `https://hub.example.com/client-auth/auth/callback`。门户位于 MCP 服务的域名下，与管理端口分开。CLI 与门户必须取得 audience 为完整 MCP resource URL、`issuer + sub` 一致的 JWT access token。若身份服务对不同客户端返回不同的 pairwise subject，应先调整身份服务的主体策略；不会通过 email 拼接身份。门户可选的 client secret 通过服务端 `client_secret_env` 配置。

以下是**追加到现有完整 YAML 根级别的片段**，不要替换整个配置文件；`client_id` 改为已注册的门户客户端 ID。

```yaml
client_authorization:
  enabled: true
  client_id: mcphub-user-portal
  require_client_grant: false
  max_grant_ttl: 8h
```

直接运行二进制时重启服务。Compose 部署将片段加入挂载的 `deploy/config.remote-postgres.yaml`，在原来的部署环境执行 `docker compose -f deploy/compose.postgres.yaml up -d --force-recreate mcphub`。还需单独注册员工 CLI 的公开客户端，按[外部 OIDC 注册](configuration.zh-CN.md#用户-cli-的外部-oidc-注册)配置 PKCE、回调和 MCP audience。先发布一个明确 `read` 的工具并授予用户相应 Scope，再让员工运行 `setup`；空的 `backends: []` 或 `published_tools: []` 不会自动产生可选工具。

在指定 backend 或 HTTP 工具组设置 `require_client_grant: true`，可逐个设置；全局开启则全部强制。两级条件取 OR。不带 `--client` 的连接只能访问未强制客户端授权的 endpoint，登录握手不会暴露严格 endpoint。门户全局设置属于静态进程配置，修改后重启；endpoint 设置沿用现有配置治理流程。

普通用户在门户确认自己的客户端范围；管理员可以查询和撤销，不能代替用户同意。外部 issuer 下，CLI 还需要[公开 OAuth 客户端注册](configuration.zh-CN.md#用户-cli-的外部-oidc-注册)；启用 SSO 桥接时按 SSO 指南配置本地公开客户端。

## 配置上游账号

在「MCP 后端 → 上游认证 → Vault」中按服务选择共享服务账号或每用户个人账号。共享账号由管理员提供固定 Vault 路径；个人模式由服务器分配路径，并要求启用用户门户与客户端授权。

管理员负责 Vault 的 AppRole、路径权限、发现凭证、上游 OAuth 回调与备份；用户只在门户连接自己的账号。配置细节见 [Vault 配置与运维](vault-accounts.zh-CN.md)，员工步骤见[连接个人账号](user-guide.zh-CN.md#连接个人账号)。当前覆盖远程 MCP endpoint；HTTP 工具组不支持个人 Vault 账号。

## 写审批与配置治理

明确只读的已发布工具在权限通过后执行；写工具和未分类工具需逐次审批。本地免登录和仅 YAML 模式只能执行明确只读的已发布工具，写审批需要远程管理。

| 角色 | 职责 |
| --- | --- |
| 配置管理员 | 接入服务、发布工具、设置权限与查询诊断 |
| 写操作审批人 | 审核具体操作、目标、参数和预览；按配置完成独立复核与加强认证 |
| 安全审批人 | 启用配置审批时，审核配置提案并应用；不能直接编辑配置 |

按需显式授予兼任角色；管理权限本身不等于写审批权限。生产写工具还应由后端落实原子版本检查与业务幂等。通知链接不能代替浏览器审批，审批完成也不会跳过执行时的权限检查。

配置步骤与完整示例见[单次写审批](configuration.zh-CN.md#单次写入审批)、[配置治理与双人审批](configuration.zh-CN.md#配置治理双人审批与业务幂等)、[审批通知和独立归档](configuration.zh-CN.md#审批通知与独立审计归档)。用户与审批人的实际操作见[写操作审批](user-guide.zh-CN.md#写操作审批)。

## 控制台导航

控制台按日常管理工作分为四组，功能入口如下。导航只显示当前账号有权使用的页面；仅审批角色直接进入审批中心。

| 分组 | 页面 | 主要用途 |
| --- | --- | --- |
| 总览 | 概览 | 查看真实后端连接状态、工具组与 HTTP 工具数量、最近变更；优先展示连接异常的后端，并直接进入配置。 |
| 服务接入 | MCP 后端 | 接入 Streamable HTTP MCP 服务；搜索、按状态筛选、测试连接、启停，以及配置发布范围、上游凭证和限流。 |
| 服务接入 | HTTP 工具组 | 将 REST API 转成 MCP 工具；手工添加接口或从 OpenAPI 导入，共享连接、凭证和访问策略。 |
| 访问控制 | 工具权限 | 查看显式发布状态、读写分类、最终 Scope、业务资源规则和审批要求；编辑规则或执行无副作用的权限检查。 |
| 访问控制 | 用户与组织 | 管理 SSO 用户、部门和用户组的本地启停、角色、Scope、工具与资源权限；展开单条记录后编辑。 |
| 访问控制 | 客户端授权 | 按用户、客户端、endpoint 与状态筛选授权；查看完整范围、关联请求，或撤销授权。 |
| 治理与审计 | 审批中心 | 按权限审核写操作与配置变更，核对预览、审批进度、执行结果与审计记录。 |
| 治理与审计 | 请求诊断 | 查看近期调用结果、拒绝原因和耗时；展开请求详情，直接定位对应的工具权限。 |
| 治理与审计 | 变更记录 | 查看最近 50 条管理变更及操作者；敏感凭证不会展示。 |

推荐工作顺序：**接入服务 → 显式发布与读写分类 → 配置用户/组织权限 → 客户端登录与授权 → 审批和诊断**。用户使用 `mcpbridge setup` / `connect` 接入，在个人授权门户确认自己的客户端范围；管理员在管理控制台维护整体策略。SSO 身份源、管理员登录、数据库和审计投递等部署设置继续通过 YAML 和环境变量配置，详细步骤见 [SSO 与用户管理](sso-and-user-management.zh-CN.md)。

两类编辑器都按 **连接信息 → 上游认证 → 客户端访问权限** 配置。上游 Header/OAuth 是 MCPHub 调用服务的凭证；所需 Scope 决定客户端能否使用后端或工具组：留空允许所有已认证客户端，填写多个时必须**全部满足**。在**已发布工具**中填写审核通过的原始工具名；发现新工具不会自动发布。工具级规则可为选定操作追加 Scope 和资源参数限制；高级连接配置按需展开。编辑已有凭证时，值留空会保留原值；删除 Header 行会移除对应凭证。

多个 MCP endpoint 可复用相同的所需 Scope。未启用 SSO 桥接时，由外部身份服务签发对应 Scope；启用 `auth.sso` 后，可在用户与组织中维护用户和部门/用户组的本地授权，成员关系由已验证的登录声明或目录快照同步。HTTP 工具组用于同一 REST API 内共享连接与权限，不用于组合多个 MCP 后端。工具发布、Scope、业务资源、客户端授权和写审批共同约束调用。

语言控件可切换中文与 English。窄屏使用抽屉导航，支持键盘与 Escape 关闭。刷新会重新读取当前配置并显示最近成功更新时间；失败时保留错误提示与重试入口。概览数据来自实际配置和连接状态；请求历史按数据库保留期查询，不代表完整监控或不可篡改的审计归档。

## 工具权限检查

配置管理员可在管理界面进入 **工具权限**，选择 MCP 后端或 HTTP 工具组，统一查看已发布和未发布的工具、最终读写类型、所需 Scope、参数资源限制与审批要求。可搜索工具，筛选未分类或未发布的项目。读取目录不会执行工具；HTTP 工具组状态仅表示是否启用，不代表已探测上游连通性。

**编辑工具规则** 修改当前工具的精确名称规则；MCP 后端的发布状态在同一版本中保存。其他匹配规则继续生效，精确 `read` 不能覆盖通配 `write` 或审批规则。表单支持 Scope、JSON Pointer 资源允许值、审批人 Subject、审批人数和加强认证，并保留已有高级审批字段。HTTP 工具发布仍通过现有工具／工具组或 OpenAPI 导入编辑器管理。保存沿用现有版本冲突检查；启用配置审批时，仍须经过审批才生效。

**权限检查** 逐项解释发布、就绪状态、Scope、资源、客户端 Grant 和审批条件，不调用工具、不运行预览、不创建审批、不占用执行额度。输入的 Scope 是管理员的假设条件。检查已保存 Grant 时，还需填写 Grant ID 和对应用户的精确 Subject，有效 Scope 取其授权交集。检查通过不授予执行权：参数 Schema、实时限流、资源版本、预览和上游权限仍在实际执行时校验。仅配置管理员可访问 `GET /api/v1/tool-policies?endpoint=<id>` 与 `POST /api/v1/access-check`，后者请求示例：

```json
{"endpoint":"projects","tool":"get_project","scopes":["projects:read"],"arguments":{"project":"work"}}
```

## 授权与请求诊断

管理 UI 新增**客户端授权**和**请求诊断**。配置管理员可按精确用户 Subject、客户端 ID、endpoint 和状态查询并翻页，查看 Scope、工具、资源范围和协议能力，带入权限检查或撤销授权。授权 Scope 只是批准上限，不代表用户当前 Token 的实际权限。撤销立即阻止后续接纳并取消活动流，无法回滚上游副作用。个人门户仍只能访问登录用户自己的授权。

请求诊断将已完成的 MCP POST 保存到托管 SQLite／PostgreSQL，重启后保留。`admin.request_retention` 默认 `720h`（30 天），可设为 `24h`–`8760h`；到期记录自动清理。默认查询最近 24 小时，支持完成时间筛选、分页和 NDJSON 导出。仅记录请求 ID、已验证身份、已知目标、耗时和固定结果／原因代码，不记录参数、结果、Token 或原始错误正文。详细记录加密保存，查询索引保留身份与路由等元数据，应保护数据库和导出文件。统计覆盖整个筛选窗口，包括 P95 和已恢复操作的平均审批等待时间。

请求记录在处理完成后写入；写入失败不会重放或改变业务操作，服务器记录错误，页面提示本次运行的记录缺口。进程在写入前崩溃、进行中的请求和 GET 流不在此记录中；它不能替代审批审计的独立归档。未启用托管数据库的部署只保留原有短期内存诊断。

`GET /api/v1/requests` 新增 `since`、`until`（RFC3339，按完成时间筛选），实际窗口在 `window_start`／`window_end` 中返回。`format=ndjson` 导出最多 10,000 条，仍受管理员鉴权和相同筛选限制；响应头 `X-MCPHub-Next-Cursor` 非零时可作为 `cursor` 继续导出，或在 UI 缩小时间范围。请求历史持久化在新部署的 **schema 9** 数据库中，备份时保存数据库及匹配密钥。

以下 API 仅位于**管理监听器**，个人门户不开放跨用户查询：

```bash
mcpbridge admin --profile ops get '/client-grants?subject=alice&status=active&limit=25'
mcpbridge admin --profile ops get '/requests?endpoint=database-prod&outcome=scope_denied&limit=25'
```

两者都返回 `next_cursor`，保持筛选条件并作为 `cursor` 传回；请求历史翻页时同时保持返回的时间窗口。授权还可筛选 `client`、`endpoint`；诊断还可筛选 `request_id`、`subject`、`client` 和原始 `tool`。普通分页 `limit` 范围为 1–100。撤销接口为 `POST /api/v1/client-grants/{grant_id}/revoke`，正文 `{"subject":"alice"}`。

## 备份与恢复

全新部署初始化 SQLite 或 PostgreSQL 后，分别保管数据库备份和匹配的 `MCPHUB_CONFIG_KEY`。SQLite 使用一致性备份方式，PostgreSQL 使用数据库原生备份工具；启用 Vault 时协调其数据备份。

恢复演练在隔离环境使用同版本程序、备份数据库及匹配密钥。检查后端地址、凭证、工具发布、用户权限和撤销记录，再验证实际调用。不要删除数据库来重新导入 YAML；管理台中的服务配置以数据库为准。

## 运行维护

- 以 `/healthz` 检查进程存活，以 `/readyz` 检查身份验证器和 required 后端就绪；两者含义不同。详见[HTTP 端点](configuration.zh-CN.md#http-端点与-rfc-9728)。
- YAML 可热改字段使用 SIGHUP；监听器、身份地址、管理配置以及 Vault/门户等静态设置变化需重启。已初始化的托管数据库是后端配置来源，之后修改 YAML backends 不生效。详见[热重载与关停](configuration.zh-CN.md#sighup-热重载与关停)。
- 备份数据库及匹配的 `MCPHUB_CONFIG_KEY`，启用 Vault 时协调备份 Vault 数据。恢复后核对授权、账号和配置状态；创建部署时选择数据库驱动。
- 查看请求记录缺口、审批投递失败与归档积压；请求历史和最近配置变更不能替代独立审批审计归档。

### Docker 部署

Dockerfile 使用 `golang:1.26-bookworm` 构建静态二进制，再放入 `gcr.io/distroless/static-debian12:nonroot`；最终容器没有 shell，进程以 nonroot 用户运行。

```bash
docker build -t mcphub:local .
docker run --rm \
  --name mcphub \
  -p 127.0.0.1:8080:8080 \
  --env-file .env \
  -v "$PWD/config.yaml:/etc/mcphub/config.yaml:ro" \
  mcphub:local serve --config /etc/mcphub/config.yaml
```

容器内监听地址应与端口映射匹配（示例为 `:8080`）。`--env-file` 只为进程注入环境变量，配置以只读方式挂载；请限制宿主机 `config.yaml` 和 `.env` 的权限。镜像入口点已经是 `/usr/local/bin/mcphub`，因此 `serve`/`validate` 直接作为参数传入。

SQLite 管理模式需要可写数据库卷，两种存储都需要 `MCPHUB_CONFIG_KEY`。本地模式绑定容器回环，普通端口映射无法访问它。远程容器部署使用 `mode: remote`、OIDC 管理员权限与 HTTPS 代理，参见[PostgreSQL Compose 示例](../deploy/README.zh-CN.md)。

## 安全说明

- 生产环境对外的 `public_url`、`auth.issuer` 和远端 backend URL 使用 HTTPS。`allow_insecure_http` 只允许 loopback 后端，不能把远端明文连接变成合法配置。
- `public_url` 必须出现在 token 的 `aud` 中：`aud` 为字符串时等于 `public_url`，为数组时包含 `public_url`；TLS 终止或反向代理后仍要保留公开的 host 和路径，并正确转发 RFC 9728 的两个 metadata 地址。
- 使用精确的 `allowed_origins`，不要把不受信任的控制台加入列表；Origin 和预检头均有严格 allowlist，预检只允许 `POST`（由 `OPTIONS` 返回响应）。
- 把 client secret、API key、静态 Authorization 等放在环境变量或外部 secret store，不要提交 YAML。静态 header 会发送到后端数据面，请避免把敏感值写入日志或能力名。
- 配置校验会拒绝换行头、重复头和 transport 管理头（包括 `Proxy-Authorization`、`Proxy-Authenticate`）；OAuth 模式会拒绝静态 `Authorization`，避免认证来源互相覆盖。
- 超长或无效的 request ID 会重新生成；日志中的 request/trace 值会限长或哈希，能力名和资源 URI 字段会校验并脱敏；请求失败只记录外部错误类型，不记录外部错误原文。
- `/healthz`、`/readyz` 和 metadata 不要求 Bearer；应在网络层限制管理面可见范围。MCP 入口只接受 Authorization header 的 Bearer JWT。
- MCP 监听器的所有 HTTP 路由在 request body 被消费或关闭前都保持 `request_timeout` 读取 deadline，因此未认证或被拒绝请求的慢 body 也有界。`subscriptions/listen` POST 只有在 body 读完后才不受普通 response 写入和 request context timeout 限制。
- 后端 SSE 响应保持 streaming passthrough。progress 检查每个 event 最多缓存 1 MiB；超大 event 原样转发但跳过 progress 检查。
- 已确认的 2026 resource subscription ID 会把更新映射回原订阅 URI，包括 update event URI 不同的情况；timeout、取消订阅和 session/重连清理会删除映射。
- 本地管理模式无登录，只允许数字回环地址；远程管理模式要求独立 audience 和管理员 scope，使用 HTTPS、精确 Host/Origin、Cookie CSRF 校验和严格 CSP。
- `MCPHUB_CONFIG_KEY` 应放在 YAML 和数据库备份之外安全保存。SQLite 文件权限为 `0600`，但恢复其中 Secret 必须保留完全相同的 32 字节密钥。

## 已知限制与排障提示

- 管理平台持久化 backend、工具组配置以及写入审批和审计记录；当前仍没有指标、持久化 MCP 目录、跨实例共享订阅或高可用协调，每个进程维护自己的后端连接、目录和 token view。
- 当前版本不提供内置账号、stdio 后端接入、独立旧式 GET SSE 端点、原生 TLS、动态租户、opaque token introspection、Tasks、MCP Apps 或自定义 MCP 扩展。本地 `connect` 命令提供到 HTTP 网关的 stdio 连接；TLS 和外部限流由反向代理负责。
- 个人凭证当前覆盖远程 MCP endpoint；HTTP 工具组、动态云/数据库凭证及供应商专用 SaaS OAuth 适配不在本版范围内。
- 聚合器只声明并实现 tools、prompts、resources（含订阅）和 completions 能力；后端声明的其他能力不会自动变成网关能力。非法名称、非法 URI 模板和 SDK 拒绝的元数据会被省略。
- 后端断线时保留 last-known-good 目录，但调用需要实时连接；required 后端会使 `/readyz` 变为 503，optional 后端不会阻塞整体就绪。
- `server.refresh_interval` 是最大刷新周期，后端返回更短 TTL 时会更快刷新；不会提供强制即时刷新 API。
- SIGHUP 不会重新读取 Docker 编排层的 `--env-file`；占位符使用的是当前进程环境，secret 或不可热改字段变化应重启进程。

常见现象对应关系：

1. `validate` 报 `environment variable ... is not set`：按所选示例的变量清单注入实际服务进程，见[基础示例](configuration.zh-CN.md#从完整-yaml-示例启动)或[远程部署](../deploy/README.zh-CN.md#环境变量清单)；`required: false` 不会跳过该后端的变量校验。
2. `/readyz` 返回 503：查看 JSON 中 `auth_verifier_ready` 和 `required_ready`，再检查 OIDC discovery/JWKS 与 required 后端 URL。
3. MCP 返回 401 且 challenge 带 `resource_metadata`：检查 Bearer 是否存在、签名/issuer/audience 是否正确。
4. MCP 返回 403 `insufficient_scope`：按 challenge 中的 `scope` 补齐该后端的全部 `required_scopes`。
5. 访问 `/mcp` 404：检查请求路径是否与 `public_url` 的路径完全一致；`public_url` 不能只写域名根路径。

配置相关常见误用：

| 现象 | 检查与处理 |
| --- | --- |
| 修改 YAML 后服务列表未变 | 托管数据库初始化后，改用控制台/API 编辑；不要删除数据库。 |
| 配了变量但 SSO/Vault 仍使用 `${...}` | 这些字段不展开；填实际字面值，Secret 用 `*_env` 变量名。 |
| 只有管理页，没有用户授权门户 | 另行启用 `client_authorization` 并注册普通用户客户端。 |
| 发布后仍看不到/不能执行工具 | 同时检查原始工具名、`effect`、Token/Grant Scope 和用户策略；`validate` 不验证工具实际存在。 |
| SQLite 看似变成空库 | 核对 YAML 的位置及解析后的 `database_path`，不要意外连接到新路径。 |
