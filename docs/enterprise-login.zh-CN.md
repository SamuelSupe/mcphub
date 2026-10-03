# 在管理台配置 LDAP 与 OIDC

> 本指南用于 v2.4.0 新部署，不包含迁移流程。

默认保留 MCPHub 内建账号。管理员登录后打开 **访问控制 → 身份服务**，可同时启用一条 LDAP 连接与一条 OIDC 连接。服务账号密码和 Client Secret 使用配置密钥加密保存在 SQLite 或 PostgreSQL 中，读取接口只返回「已配置」状态；表单留空保留原密钥。

保存立即生效，不需要修改环境变量或重启。首次保存后，数据库中的身份连接配置优先于 YAML 的 `auth.sso.upstream`。修改或停用连接会撤销该来源的旧会话、刷新凭证与待完成的授权；重新启用不会恢复旧凭证。OIDC 的 Issuer、Client ID、Subject 或租户边界，以及 LDAP 的地址、用户／组搜索范围、过滤器或稳定 ID 属性改变时，会建立新的身份来源；旧来源的组授权不会自动继承。

UI 配置要求 `auth.mode: builtin` 和管理存储；独立外部认证模式继续由 YAML 管理。始终保留本地管理员及服务器本机恢复入口，连接测试不会授予任何用户权限。

## 配置 OIDC

请求 Scopes、名称、组、部门和租户 Claim 位于「身份映射与租户限制」高级选项中。

1. 在身份服务创建机密 Web 客户端，启用授权码流程与 PKCE S256。
2. 将页面显示的 **回调地址** 原样注册到身份服务，格式为 `https://<MCP 公开域名>/sso/callback`。
3. 启用 OIDC，填写 Issuer URL、Client ID、Client Secret 和客户端认证方式。Issuer 必须是 HTTPS；支持 `client_secret_post` 与 `client_secret_basic`。
4. 设置请求 Scope（通常为 `openid profile email`）和显示名称、组、部门 Claim。组／部门 Claim 必须返回字符串数组；Subject 固定使用经过签名验证的 `sub`。租户限制需要同时填写 Claim 与允许值。
5. 点击 **验证 OIDC 登录与映射**，打开测试登录，在企业身份服务完成登录后返回管理台。核对稳定 Subject、名称、组和部门；测试使用草稿中的客户端密钥完成授权码交换、签名、nonce 和租户验证。配置了但未返回的 Claim 会显示提示。**检查基础连接** 仅检查发现文档与签名密钥，不能代替登录测试。
6. 点击保存，核对受影响来源的用户数量与会话撤销提示，再确认。UI 要求发生变更且启用的来源通过当前草稿的登录测试；编辑草稿或超过五分钟后需重新测试。测试不会创建用户、授予权限或替换当前身份源，保存后仍需完成普通企业用户实际授权验收。

管理员门户、用户门户与 MCPBridge 都由 MCPHub 签发凭证；企业身份服务的 Token 不发给 MCP 客户端。OIDC 登录后，首次发现的用户默认待授权。管理员在「用户与组」启用账号，通过独立权限组的组织组映射分配权限。

企业加强认证仍需按照 [审批配置](configuration.zh-CN.md) 配置和验收身份服务的 ACR、MFA／Passkey 策略；填写连接参数不会自动启用或证明 MFA。

## 配置 LDAP

LDAP 使用服务账号搜索唯一的用户 DN，然后以该用户 DN 和输入密码进行绑定。只接受 **LDAPS** 或强制 **StartTLS**，始终验证服务端证书和主机名；不提供跳过证书校验的选项。空密码不会触发绑定。

| 字段 | OpenLDAP 示例 | Active Directory 示例 |
| --- | --- | --- |
| LDAP URL | `ldaps://ldap.example.com:636` | `ldaps://ad.example.com:636` |
| 服务账号 Bind DN | 有搜索权限的完整 DN | 有搜索权限的完整 DN |
| 用户搜索 Base DN | `ou=people,dc=example,dc=com` | `dc=example,dc=com` |
| 用户搜索过滤器 | `(&(objectClass=person)(uid={{username}}))` | `(&(objectClass=user)(sAMAccountName={{username}})(!(userAccountControl:1.2.840.113556.1.4.803:=2)))` |
| 用户稳定 ID 属性 | `entryUUID` | `objectGUID` |
| 用户显示名称属性 | `cn` | `displayName` |
| 组搜索 Base DN | `ou=groups,dc=example,dc=com` | `dc=example,dc=com` |
| 组搜索过滤器 | `(&(objectClass=groupOfNames)(member={{dn}}))` | `(&(objectClass=group)(member={{dn}}))` |
| 组稳定 ID 属性 | `entryUUID` | `objectGUID` |
| 组显示名称属性 | `cn` | `cn` |

`ldap://host:389` 自动执行 StartTLS，成功前不发送绑定密码。私有 CA 可粘贴 PEM 证书；留空使用系统信任。服务账号应只有用户及组搜索权限；不要在地址中填写账号或密码。

用户过滤器必须恰好包含一次 `{{username}}`。组过滤器使用 `{{dn}}` 或 `{{username}}`，替换值按 LDAP 过滤器规则转义。用户搜索必须得到一个结果；多个匹配、查询失败或组数量超过 256 时拒绝登录，不能退回不完整授权。稳定 ID 必须唯一、非空且不可随重命名改变；AD 二进制 GUID 和文本 UUID 都会转换成不透明的身份标识。

组搜索可全部留空，此时管理员可创建独立权限组并为企业用户直接添加成员关系。填写组搜索时必须同时填写 Base DN、过滤器、稳定 ID 与显示名称属性。不会自动遍历嵌套组。

点击 **检查基础连接** 可检查 TLS、服务账号绑定与用户／组 Base DN。保存前点击 **验证 LDAP 登录与映射**，输入普通测试账号及密码，验证用户唯一匹配、稳定 ID、用户绑定与组搜索；核对名称与组映射后保存。测试密码只用于本次验证，不保存；测试不创建用户或替换身份源。目录的失败计数与锁定规则仍生效。用户在企业登录页选择 **LDAP** 后输入目录账号与密码；目录密码不保存在 MCPHub 中。

LDAP 登录有每来源／用户名的速率限制、共享 IP 限制和并发上限，目录自身的锁定策略仍应启用。LDAP 密码登录是单因素，不能满足需要加强认证的写审批；此类审批由内建 TOTP 或经过验收的企业 OIDC 加强认证用户完成。

## 为企业用户分配组权限

1. 用测试企业账号登录一次，确认稳定身份 ID、来源、用户及组织组正确。
2. 在 MCPHub 启用该用户。创建独立权限组，在组上选择服务、工具、角色与资源条件。
3. 在权限组的「组织组映射」中勾选所需 LDAP/OIDC 组织组。同一个权限组可服务多个身份来源，账号身份仍保持隔离。
4. 默认按访问意图保存 Scope 快照；高级显式模式需补齐能力所需 Scope。查看用户的「有效权限」，核对直接组与映射来源。
5. 重新登录并验证目录和调用。未发布或未授权工具不可见；写操作仍需逐次审批。

组织组成员关系由身份源维护，控制台不直接编辑组织组业务权限。按稳定 ID 映射，不能用同名本地组或相同邮箱替代企业身份。取消映射或移除有效组织组成员关系会收回继承权限。

LDAP/OIDC 登录刷新组关系；完整目录快照也可以刷新验证时间。`auth.enterprise_membership_max_age` 默认 `24h`，超过期限拒绝会话刷新和工具调用，用户需重新登录或同步目录。系统不轮询 LDAP，目录变化在下一次登录读取。需要即时阻断时在 MCPHub 停用用户、组织组或身份连接，或收回权限映射。访问 Token 最长 10 分钟，刷新会话最长 8 小时，服务授权还受自己的截止时间限制。

## 验收与排障

- OIDC 回调失败：确认 Issuer、回调地址完全一致，客户端密钥和认证方式正确，并支持 PKCE S256；确认 `sub`、租户和组 Claim 的类型。
- LDAP TLS 失败：检查端口、证书主机名、有效期、CA 链及 StartTLS 支持，不要关闭证书验证。
- LDAP 服务绑定失败：检查服务账号完整 DN、密码和搜索权限。
- LDAP 用户登录失败：检查用户过滤器得到唯一结果、密码正确、稳定 ID 属性可读及组搜索完整；LDAP／AD 返回的账号锁定状态仍生效。
- 登录成功但无权限：在 MCPHub 启用账号，并通过独立权限组分配权限，再重新登录；不要给同名本地组授权后期待企业用户继承。
- 保存冲突：刷新读取最新版本，重新填写变更。`If-Match` 防止覆盖其他管理员的配置。

管理 API 为 `GET /api/v1/identity-providers`、`PUT /api/v1/identity-providers` 和 `POST /api/v1/identity-providers/probe`。写操作和连接测试需要管理员权限、浏览器 CSRF／同源检查以及当前 `If-Match`（初始值为 `"0"`）。PUT 提交完整的 `oidc` 与 `ldap` 设置；密钥字段省略保留，显式空字符串清除。probe 的 `source` 为 `oidc` 或 `ldap`，不保存配置，也不授予权限。

`POST /api/v1/identity-providers/test` 接受相同草稿及 `source`；LDAP 另外提交 `username`、`password`。OIDC 返回一次性的 `start_url`，在浏览器打开后，管理员通过 `GET /api/v1/identity-providers/tests/{id}` 读取结果。测试使用原有 `/sso/callback`，不用注册额外回调。结果只允许发起测试的管理员读取，最多保留五分钟；保存配置、过期或重启使测试失效。返回内容不包含 Token 或密码。UI 的保存前测试与影响确认辅助配置；直接 PUT 的 API 调用方仍应主动完成测试与验收。
