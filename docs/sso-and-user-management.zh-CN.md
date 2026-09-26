# 外部 SSO 与 MCPHub 用户权限

MCPHub 可以作为身份桥接服务接入 OIDC 或支持授权码、PKCE S256 和 Bearer UserInfo 的 OAuth2 身份源。客户端继续使用 `mcphub-cli`，无需持有上游应用密钥。当前没有配置或连接任何飞书租户；飞书兼容性需由实际应用的授权接口、UserInfo 字段、租户配置和目录适配器确认。

```text
用户浏览器 → MCPHub /sso → 企业身份服务
                         ↓ 验证身份
               MCPHub 用户、部门、用户组权限
                         ↓ 本地签发的 JWT
mcphub-cli / 本地 Broker → MCPHub → 工具策略 → 写审批 → 后端
```

上游 Token 仅用于身份验证，不作为 MCPHub 调用凭证，也不传给工具后端。不开启 `auth.sso` 时，保留现有外部 JWT 验证模式。

## 配置身份源和公开客户端

下面是添加到现有配置的片段。MCPHub 对外和身份服务必须使用 HTTPS；网关内网监听器保持私有。`auth.issuer` 固定为 MCP 公网域名下的 `/sso`，反向代理必须转发 `/sso/*`、`/.well-known/oauth-authorization-server/sso`、`/.well-known/openid-configuration/sso` 和原有 MCP 元数据路径。

```yaml
server:
  public_url: https://hub.example.com/mcp

auth:
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
    # 精确匹配上游 sub，仅首次创建用户时授予管理员角色。
    # 上线完成后移除；后续登录不能恢复被管理员撤销的权限。
    bootstrap_subjects: [initial-admin-subject]
    clients:
      - id: mcphub-cli
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

在上游注册**机密 Web 客户端**，固定回调为 `https://hub.example.com/sso/callback`。上游 secret 只放服务端环境变量。MCPHub 的下游客户端是预注册的公开客户端，管理页面和用户门户不要配置 `client_secret_env`。建议给每个应用分别注册 client ID 和资源范围。上面的管理员客户端只注册了浏览器回调；若需要 `mcphub-cli login --admin`，另注册带 loopback 回调、admin resource 的客户端。

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

## 管理用户与组织

进入管理页面的 **用户与组织**。首次发现用户默认待授权，管理员需启用账号并配置直接权限或组织授权；新组和新部门没有任何权限。`bootstrap_subjects` 是仅首次创建时生效的管理员引导入口，不会自动给管理员开放业务工具。

- 本地启停与目录启停都必须允许；目录恢复不能重新启用被管理员停用的用户。
- 角色为 `admin`、`approver`、`security_reviewer`，分别映射现有管理员、写审批、配置审批 scope。原有审批人范围和禁止自审条件仍然适用。
- Scope 与 endpoint、精确原始工具名、业务资源条件分别配置。空工具列表不允许工具调用；不会隐式授予新发现工具。
- 提示词、资源读取和订阅需要单独授权。业务 `resource_rules` 只检查工具参数，不是 MCP resource URI 的所有权策略。
- 用户直接权限与有效部门/组权限取并集。每条服务授权的工具、写申请开关和资源条件作为一个整体匹配；不能从不同授权拼接出更宽的业务资源范围。一条无资源限制的授权会放开该工具的用户级资源限制，因此只应在确实需要时配置。
- 实际调用还需满足 Token scope、客户端 Grant、endpoint/tool scope、发布和启停状态、共享资源策略。允许申请写操作仍然需要逐次审批。

保存采用 `If-Match` 版本检查，并记入变更记录。登录同步、目录推送不修改本地角色、scope 或工具授权。Scope 撤销每次验票生效；新增 scope 需重新登录取得新的授权上限。组织权限变化会重新计算有效权限，旧 MCP 视图不能继续调用；失去权限的在途请求收到取消信号，不能撤回已经完成的写入。部分客户端遇到授权拒绝或流关闭后需重建连接。

管理员 API：`GET /api/v1/identities`；`PUT /api/v1/identities/{id}`（`If-Match: "revision"`）：

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

1. **登录声明同步**：配置 `groups_claim` / `departments_claim`（点分路径），值为字符串数组。每次成功认证完整替换该用户的成员关系。未返回某项声明表示该项没有成员关系；不会追加保留旧组。组和部门的同名 ID 相互隔离。
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

每次推送是该身份源的**完整快照**：遗漏用户或组织会停用，非增量追加。必须先成功拉取所有分页并核验目录完整性，再推送；目录请求失败不能推送空快照。`version` 为持久化的单调递增正整数（例如毫秒时间戳）；过时或重复版本返回 409。更新在数据库事务内原子应用，失败不影响已有目录。上限为 8 MiB、10,000 用户、2,000 个部门/组、每用户 256 个成员关系。成员关系是平面的；若要继承父部门权限，适配器需显式输出祖先部门 ID。

仅可推送身份、名称、启停和成员关系，额外权限字段会被拒绝。离职/冻结生效时间取决于同步间隔；登录声明模式无法主动发现上游离职或全局退出。已有授权在下一次调用检查时失效，不等待 JWT 自然到期。

## 客户端与部署边界

```bash
mcphub-cli login --server https://hub.example.com/mcp --client-id mcphub-cli --profile work
mcphub-cli connect --profile work
mcphub-cli status --profile work
mcphub-cli logout --profile work
```

需要本地 Broker 时继续使用已有 `setup`/客户端授权流程。MCP 客户端配置仍只启动 `mcphub-cli`；不填写上游 Token 或 client secret。

本地 access JWT 有效期 10 分钟；请求 `offline_access` 后可取得轮换 refresh token，SSO 会话最长 8 小时，之后重新登录。上游凭证不长期保存；本地刷新不会重新查询上游账号。已消费 refresh token 被重放时撤销整个会话。签名私钥使用现有配置密钥加密保存在数据库，refresh token 只保存摘要；access JWT 仍需通过数据库中的会话与最新用户权限检查。备份时必须一同保留数据库与匹配的配置加密密钥。

SQLite 和单实例 PostgreSQL 当前源码使用 schema **8**（v2.0.0 为 schema 7）；启动自动迁移，旧二进制不可写入升级后的库。授权请求和一次性 code 在进程内，重启需要重新开始未完成的登录；持久化会话和签名密钥保留。登录、目录同步入口仍应纳入已有 HTTPS 代理的请求大小与速率保护。会话最长 8 小时不等于身份源实时在线校验；全局登出、SCIM、SAML、多身份源选择和多实例一致性不属于本次实现。
