# 在管理台配置 LDAP 与 OIDC

> 本指南用于 v2.3.0 新部署，不包含迁移流程。

默认保留 MCPHub 内建账号。管理员登录后打开 **访问控制 → 身份服务**，可同时启用一条 LDAP 连接与一条 OIDC 连接。服务账号密码和 Client Secret 使用配置密钥加密保存在 SQLite 或 PostgreSQL 中，读取接口只返回「已配置」状态；表单留空保留原密钥。

保存立即生效，不需要修改环境变量或重启。首次保存后，数据库中的身份连接配置优先于 YAML 的 `auth.sso.upstream`。修改或停用连接会撤销该来源的旧会话、刷新凭证与待完成的授权；重新启用不会恢复旧凭证。OIDC 的 Issuer、Client ID、Subject 或租户边界，以及 LDAP 的地址、用户／组搜索范围、过滤器或稳定 ID 属性改变时，会建立新的身份来源；旧来源的组授权不会自动继承。

UI 配置要求 `auth.mode: builtin` 和管理存储；独立外部认证模式继续由 YAML 管理。始终保留本地管理员及服务器本机恢复入口，连接测试不会授予任何用户权限。

## 配置 OIDC

请求 Scopes、名称、组、部门和租户 Claim 位于「身份映射与租户限制」高级选项中。

1. 在身份服务创建机密 Web 客户端，启用授权码流程与 PKCE S256。
2. 将页面显示的 **回调地址** 原样注册到身份服务，格式为 `https://<MCP 公开域名>/sso/callback`。
3. 启用 OIDC，填写 Issuer URL、Client ID、Client Secret 和客户端认证方式。Issuer 必须是 HTTPS；支持 `client_secret_post` 与 `client_secret_basic`。
4. 设置请求 Scope（通常为 `openid profile email`）和显示名称、组、部门 Claim。组／部门 Claim 必须返回字符串数组；Subject 固定使用经过签名验证的 `sub`。租户限制需要同时填写 Claim 与允许值。
5. 点击 **测试 OIDC 连接**，再保存。测试只检查 HTTPS 发现文档、PKCE 支持与签名密钥端点；客户端密钥、租户、组映射必须通过实际登录验收。

管理员门户、用户门户与 MCPBridge 都由 MCPHub 签发凭证；企业身份服务的 Token 不发给 MCP 客户端。OIDC 登录后，首次发现的用户默认待授权。管理员在「用户与组」启用账号，并给对应来源的组分配权限。

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

组搜索可全部留空，此时管理员可为该 LDAP 来源创建策略组并手工添加成员。填写组搜索时必须同时填写 Base DN、过滤器、稳定 ID 与显示名称属性。不会自动遍历嵌套组。

点击 **测试 LDAP 连接** 验证 TLS、服务账号绑定与用户／组 Base DN 的可读性，再保存。测试不会验证某个用户密码；实际登录还需核对唯一匹配、稳定 ID 和组成员。用户在企业登录页选择 **LDAP** 后输入目录账号与密码；目录密码不保存在 MCPHub 中。

LDAP 登录有每来源／用户名的速率限制、共享 IP 限制和并发上限，目录自身的锁定策略仍应启用。LDAP 密码登录是单因素，不能满足需要加强认证的写审批；此类审批由内建 TOTP 或经过验收的企业 OIDC 加强认证用户完成。

## 为企业用户分配组权限

1. 用测试企业账号登录一次，确认「用户与组」出现正确来源的用户、显示名称与目录组。
2. 在 MCPHub 中启用该用户。
3. 展开对应组，配置角色、Scope、服务、工具与业务资源条件；用户没有直接权限字段。
4. 重新登录，检查工具目录及实际调用。未发布工具和未授权服务不可见；写操作仍需审批。

OIDC、LDAP 与本地账号的身份和组分别管理，同名用户不会合并。「用户与组」列表会显示各记录的身份来源，也可按 LDAP、OIDC 或来源地址搜索，避免混淆同名组。目录组成员关系只读，登录刷新时由身份源覆盖；管理员创建的策略组成员关系保留。有效组权限取并集，不提供拒绝覆盖。

LDAP 账号与组变化在**下一次登录**读取，本次实现不轮询 LDAP，也不做全目录预同步。停用目录账号不会自动撤销已经签发的 MCPHub 会话；需要即时阻断时，在 MCPHub 停用用户、收回组权限或停用该 LDAP 连接。访问凭证最多 10 分钟，刷新会话最多 8 小时；到期后必须再次登录。

## 验收与排障

- OIDC 回调失败：确认 Issuer、回调地址完全一致，客户端密钥和认证方式正确，并支持 PKCE S256；确认 `sub`、租户和组 Claim 的类型。
- LDAP TLS 失败：检查端口、证书主机名、有效期、CA 链及 StartTLS 支持，不要关闭证书验证。
- LDAP 服务绑定失败：检查服务账号完整 DN、密码和搜索权限。
- LDAP 用户登录失败：检查用户过滤器得到唯一结果、密码正确、稳定 ID 属性可读及组搜索完整；LDAP／AD 返回的账号锁定状态仍生效。
- 登录成功但无权限：在 MCPHub 启用账号，并向该来源的组分配权限，再重新登录；不要给同名本地组授权后期待企业用户继承。
- 保存冲突：刷新读取最新版本，重新填写变更。`If-Match` 防止覆盖其他管理员的配置。

管理 API 为 `GET /api/v1/identity-providers`、`PUT /api/v1/identity-providers` 和 `POST /api/v1/identity-providers/probe`。写操作和连接测试需要管理员权限、浏览器 CSRF／同源检查以及当前 `If-Match`（初始值为 `"0"`）。PUT 提交完整的 `oidc` 与 `ldap` 设置；密钥字段省略保留，显式空字符串清除。probe 的 `source` 为 `oidc` 或 `ldap`，不保存配置，也不授予权限。
