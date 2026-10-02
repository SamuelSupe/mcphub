# 外部 SSO 与 MCPHub 用户权限

[English](sso-and-user-management.md) · [管理员手册](admin-guide.zh-CN.md) · [文档导航](README.zh-CN.md)

本文是管理员配置与运维专题。员工接入请阅读[用户手册](user-guide.zh-CN.md)。

MCPHub 可以作为身份桥接服务接入 OIDC 或支持授权码、PKCE S256 和 Bearer UserInfo 的 OAuth2 身份源。客户端继续使用 `mcpbridge`，无需持有上游应用密钥。飞书配置示例仍是联调起点；兼容性需由实际应用的授权接口、UserInfo 字段、租户配置和目录适配器确认。

```text
用户浏览器 → MCPHub /sso → 企业身份服务
                         ↓ 验证身份
               MCPHub 用户、部门、用户组权限
                         ↓ 本地签发的 JWT
mcpbridge / 本地 Broker → MCPHub → 工具策略 → 写审批 → 后端
```

上游 Token 仅用于身份验证，不作为 MCPHub 调用凭证，也不传给工具后端。高级部署可选择 `auth.mode: external` 验证外部 JWT；默认内建模式会自动配置本地授权服务。

默认先使用[内建账号](builtin-accounts.zh-CN.md)，企业 SSO 是可选功能。此处只说明外部身份接入；本地账号与企业身份的权限不合并。

默认内建部署可直接在 [身份服务 UI](enterprise-login.zh-CN.md) 配置 LDAP 与 OIDC，保存立即生效。下面的 YAML 接入用于高级配置／初始 OIDC 连接；第一次 UI 保存后，以数据库设置为准。

## 配置身份源和公开客户端

下面是添加到现有配置的片段。MCPHub 对外和身份服务必须使用 HTTPS；网关内网监听器保持私有。`auth.issuer` 固定为 MCP 公网域名下的 `/sso`，反向代理必须转发 `/sso/*`、`/.well-known/oauth-authorization-server/sso`、`/.well-known/openid-configuration/sso` 和原有 MCP 元数据路径。

`auth.sso` 下所有字段均为字面值，不展开 `${...}`；`client_secret_env`、`directory_token_env` 填变量名。整个 `auth.sso` 的修改需要重启。`bootstrap_subjects` 匹配上游原始 subject（OIDC 的 `sub` 或配置的 OAuth2 主体字段）；审批策略中的 `subjects` 则使用 Hub 签发 JWT 的内部 `sub`，请从管理界面/API 核对，不能直接复制上游 open_id。

```yaml
server:
  public_url: https://hub.example.com/mcp

auth:
  mode: builtin
  issuer: https://hub.example.com/sso
  sso:
    upstream:
      protocol: oidc
      issuer: https://identity.example.com
      client_id: mcphub-server
      client_secret_env: MCPHUB_SSO_CLIENT_SECRET
      token_auth_method: client_secret_post # 也可 client_secret_basic
      scopes: [openid, profile]
      name_claim: name
      tenant_claim: tenant_id
      tenant_value: company-a
      groups_claim: groups
      departments_claim: departments
    clients:
      - id: mcpbridge
        redirect_uris: [http://127.0.0.1/oauth/callback]
        resources: [https://hub.example.com/mcp]
      - id: mcphub-admin
        redirect_uris: [https://admin.example.com/auth/callback]
        resources: [https://admin.example.com]
      - id: mcphub-portal
        redirect_uris: [https://hub.example.com/client-auth/auth/callback]
        resources: [https://hub.example.com/mcp]

admin:
  enabled: true
  mode: remote
  public_url: https://admin.example.com
  listen: 127.0.0.1:8081
  client_id: mcphub-admin
  required_scopes: [mcphub:admin]
  database_path: ./data/mcphub.db
  encryption_key_env: MCPHUB_CONFIG_KEY

client_authorization:
  enabled: true
  client_id: mcphub-portal
```

在上游注册**机密 Web 客户端**，固定回调为 `https://hub.example.com/sso/callback`。上游 secret 只放服务端环境变量。MCPHub 的下游客户端是预注册的公开客户端，管理页面和用户门户不要配置 `client_secret_env`。建议给每个应用分别注册 client ID 和资源范围。上面的管理员客户端只注册了浏览器回调；若需要 `mcpbridge login --admin`，另注册带 loopback 回调、admin resource 的客户端。

OIDC discovery 必须声明 S256；ID Token 校验签名、issuer、客户端 audience、有效期及 nonce。分组声明从经过验证的 ID Token 获取。加强认证只接受该 ID Token 中可验证的 `acr`、`auth_time`，由现有审批策略再次核对。普通 OAuth2 不能提供加强认证证明。

Loopback 注册地址不含端口时允许随机端口；填写端口时严格匹配。其他回调严格匹配完整地址，不支持通配符、fragment 或动态客户端注册。用户在浏览器的上游身份验证最长 5 分钟，授权码 1 分钟内有效且只能使用一次。

### OAuth2 / 企业应用 UserInfo 模式

对于不提供标准 OIDC discovery 的身份源，可把 `upstream` 替换为静态 HTTPS 端点配置：

```yaml
upstream:
  protocol: oauth2
  issuer: https://identity.example.com # 稳定的身份命名空间
  authorization_url: https://identity.example.com/authorize
  token_url: https://identity.example.com/token
  userinfo_url: https://identity.example.com/userinfo
  client_id: enterprise-app-id
  client_secret_env: MCPHUB_SSO_CLIENT_SECRET
  token_auth_method: client_secret_post
  scopes: [profile]
  subject_claim: data.open_id
  name_claim: data.name
  tenant_claim: data.tenant_key
  tenant_value: expected-tenant
  success_claim: code
  success_value: '0'
```

使用身份服务实际提供的端点及 scope，示例域名不是飞书配置。点分路径支持 `data.open_id` 这类响应封装；`success_claim` 可防止 HTTP 200 中的失败业务码被当成成功。主体字段必须是非空、稳定字符串，不能用可变姓名或未验证邮箱自动关联用户。用户按身份源连接（issuer、协议、客户端 ID、主体/租户映射）和 external subject 隔离；更换连接配置不会继承旧用户权限，旧连接的会话会被拒绝。更换应用导致 open_id 变化时是新用户，不自动继承旧权限。单租户部署应配置租户字段和值。

## 管理用户与组

进入管理页面的 **用户与组**。首次发现用户默认待授权，管理员需启用账号，在组／部门上配置权限并让用户继承；新组和新部门没有任何权限。默认使用本机初始化的内建管理员为企业用户分配权限。仅在独立外部认证部署需要时才使用 `bootstrap_subjects`，它只在首次创建企业用户时将其加入本地管理的 Administrators 组，不会开放业务工具。

首次登录待授权或账号被停用时，Hub 会向经过校验的客户端回调返回 `access_denied` 和固定原因 `account_access_required`，CLI 立即结束等待，门户说明应联系 MCPHub 管理员。授权后重新运行登录；失败不会覆盖原有本地凭证。

内建模式会拒绝停用或降权最后一位有效本地管理员；独立远程 SSO 模式会保护最后一位有效企业管理员，返回 `409 last_administrator`。判断只使用有效部门／组的继承权限，涵盖用户移出组及组的停用或降权；并发修改在同一事务边界内校验。先授权另一位有效管理员，再移除旧管理员。目录撤销仍必须生效，不能为保住管理入口而继续接受已离职用户。

保留至少一位不受企业目录撤销影响的内建本地管理员。密码或验证器丢失时，使用同一数据库、配置和密钥，在服务器本机执行 `mcphub init-admin --reset`，仅丢失验证器时额外使用 `--reset-mfa`，详见[本地账号恢复](builtin-accounts.zh-CN.md)。登录该本地账号后修复企业用户权限；目录撤销仍必须生效，需要时先修正身份源并推送完整目录快照。不要开放匿名维护台，也不要绕过审批策略。

- 本地启停与目录启停都必须允许；目录恢复不能重新启用被管理员停用的用户。
- 角色为 `admin`、`approver`、`security_reviewer`，分别映射现有管理员、写审批、配置审批 scope。原有审批人范围和禁止自审条件仍然适用。
- Scope 与 endpoint、精确原始工具名、业务资源条件分别配置。空工具列表不允许工具调用；不会隐式授予新发现工具。
- 提示词、资源读取和订阅需要单独授权。业务 `resource_rules` 只检查工具参数，不是 MCP resource URI 的所有权策略。
- 用户只继承同一身份来源中有效部门／组权限的并集，不能配置直接权限。每条服务授权的工具、写申请开关和资源条件作为一个整体匹配；不能从不同授权拼接出更宽的业务资源范围。一条无资源限制的授权会放开该工具的用户级资源限制，因此只应在确实需要时配置。
- 实际调用还需满足 Token scope、客户端 Grant、endpoint/tool scope、发布和启停状态、共享资源策略。允许申请写操作仍然需要逐次审批。

保存采用 `If-Match` 版本检查，并记入变更记录。登录同步、目录推送不修改本地角色、scope 或工具授权。Scope 撤销每次验票生效；新增 scope 需重新登录取得新的授权上限。组织权限变化会重新计算有效权限，旧 MCP 视图不能继续调用；失去权限的在途请求收到取消信号，不能撤回已经完成的写入。部分客户端遇到授权拒绝或流关闭后需重建连接。

管理员 API：`GET /api/v1/identities`；`PUT /api/v1/identities/{id}`（`If-Match: "revision"`）：

以下 `permissions` 示例只用于**组／部门 ID**。通过 `POST /api/v1/identities/groups`，正文 `{"name":"项目只读组","provider":"<已配置身份来源 ID>"}` 创建本地管理的组；身份来源 ID 从 `GET /api/v1/identities` 获取。更新用户使用 `{"enabled":true,"groups":["<组的 MCPHub ID>"]}` 及该用户自己的 revision。省略 `groups` 保留成员关系；没有身份源成员关系时，显式空数组可移出所有组。提交列表必须保留所有由身份源维护的组。用户提交非空 `permissions` 返回 `400 group_permissions_required`。本地策略组标记 `managed_locally: true`，登录声明或目录不能自行加入或覆盖其成员关系。详见[组与权限](builtin-accounts.zh-CN.md#组与权限)。

```json
{
  "enabled": true,
  "permissions": {
    "roles": [],
    "scopes": ["projects:read"],
    "access": [{
      "endpoint_id": "projects",
      "tools": ["read_project"],
      "allow_write_requests": false,
      "resource_rules": [{"argument":"/project", "allowed_values":["demo"]}]
    }]
  }
}
```

权限检查页面在 SSO 模式下需要填写此处的 MCPHub 用户 ID；上游 subject 仅用于身份映射。

## 部门 / 用户组自动同步

支持两种互斥来源：

1. **登录声明同步**：配置 `groups_claim` / `departments_claim`（点分路径），值为字符串数组。每次成功认证完整替换该用户由身份源维护的成员关系，保留管理员维护的策略组成员关系。未返回某项声明表示该项没有成员关系；不会追加保留旧组。组和部门的同名 ID 相互隔离。
2. **目录快照推送**：设置 `auth.sso.directory_token_env: MCPHUB_DIRECTORY_TOKEN`，环境变量保存至少 32 字符的随机密钥。目录适配器定期拉取企业目录，再 `PUT https://hub.example.com/sso/directory`，使用独立 `Authorization: Bearer ...`。启用后，成员关系以目录为准，忽略登录中的分组声明。

推送格式是 MCPHub 的 JSON 契约，**不是 SCIM 协议或飞书通讯录 API 适配器**。企业可用既有同步任务、iPaaS 或适配服务定时推送；本次不安装飞书应用、不抓取真实通讯录。

```json
{
  "version": 1,
  "groups": [
    {"kind":"group", "id":"engineering", "name":"研发组"},
    {"kind":"department", "id":"platform", "name":"平台部"}
  ],
  "users": [{
    "subject":"upstream-stable-subject",
    "name":"测试用户",
    "active":true,
    "groups":["engineering"],
    "departments":["platform"]
  }]
}
```

`subject` 必须与登录映射的主体完全一致。目录在首次登录前创建用户时同样保持待授权；远程部署的引导管理员应先首次登录，再开始快照同步，否则需从本地管理通道授权。目录模式中除首次引导管理员外，未出现在目录里的用户不能登录。

每次推送是该身份源的**完整快照**：遗漏用户或身份源组织会停用，非增量追加。本地管理的策略组与成员关系会保留，但不能让已停用或遗漏的用户继续访问。必须先成功拉取所有分页并核验目录完整性，再推送；目录请求失败不能推送空快照。`version` 为持久化的单调递增正整数（例如毫秒时间戳）；过时或重复版本返回 409。更新在数据库事务内原子应用，失败不影响已有目录。上限为 8 MiB、10,000 用户、2,000 个部门/组、每用户 256 个成员关系。成员关系是平面的；若要继承父部门权限，适配器需显式输出祖先部门 ID。

仅可推送身份、名称、启停和成员关系，额外权限字段会被拒绝。离职/冻结生效时间取决于同步间隔；登录声明模式无法主动发现上游离职或全局退出。已有授权在下一次调用检查时失效，不等待 JWT 自然到期。

## 客户端与部署边界

员工安装、登录与 Broker 授权统一见[用户手册](user-guide.zh-CN.md)。客户端只启动 `mcpbridge`，不填写上游 Token 或 client secret。

本地 access JWT 有效期 10 分钟；请求 `offline_access` 后可取得轮换 refresh token，SSO 会话最长 8 小时，之后重新登录。上游凭证不长期保存；本地刷新不会重新查询上游账号。已消费 refresh token 被重放时撤销整个会话。签名私钥使用现有配置密钥加密保存在数据库，refresh token 只保存摘要；access JWT 仍需通过数据库中的会话与最新用户权限检查。备份时必须一同保留数据库与匹配的配置加密密钥。

新部署的 SQLite 和单实例 PostgreSQL 使用 schema 10，备份时分别保管数据库与匹配密钥。授权请求和一次性 code 在进程内，重启需要重新开始未完成的登录；持久化会话和签名密钥保留。登录、目录同步入口仍应纳入已有 HTTPS 代理的请求大小与速率保护。会话最长 8 小时不等于身份源实时在线校验；全局登出、SCIM、SAML、多身份源选择和多实例一致性不属于本次实现。
