# Vault 与个人上游账号

MCPHub v2.1.0 起提供此能力。支持 HashiCorp Vault KV v2、SQLite 和单实例 PostgreSQL；首次启动迁移到 schema 8，升级前同时备份数据库、加密密钥和 Vault 数据。

## 用户如何使用

1. 像以前一样使用 `mcphub-cli login` 登录 MCPHub，或通过现有 Broker 配置 MCP 客户端。
2. 打开 `https://hub.example.com/client-auth/`，在「已连接账号」点击目标服务的「连接账号」。支持浏览器授权的服务会打开上游登录页；其他服务只需填写个人 Token。
3. 返回客户端继续调用。客户端配置、Broker 和 Agent 都不需要上游 Token，也不需要 Vault 路径。

账号可以同时供该用户已授权的多个客户端使用。客户端仍只能访问自己的 ClientGrant 范围；写工具及未分类工具仍须逐次审批。「查看授权详情」显示账号、权限、有效期和能否自动续期。

更换账号或断开连接会使该用户在此 endpoint 的旧 ClientGrant 失效，并取消已接纳的请求和订阅。重新连接后，需要重新进行客户端授权；已完成的操作不会撤销。首次连接账号可以直接满足已有客户端授权。退出网页会话或本机 `logout` 不等于撤销上游账号；请在门户点击「断开连接」。

## 管理员如何配置

先在服务配置中启用 Vault，再到「MCP 后端 → 上游认证 → Vault」选择「共享服务账号」或「每个用户自己的账号」。页面提供 Vault 连通性检查、连接方式和回调地址。字段与请求头映射放在高级设置中。

共享模式使用管理员提供的固定 Vault 路径；个人模式由服务器分配路径，用户与 Agent 均不能指定。个人模式要求启用用户授权门户和 `require_client_grant`，UI 会自动勾选后者。Vault 全局配置改变需要重启；endpoint 配置沿用现有变更治理和热更新流程。

以下片段应合并到已有的远程管理配置。保留现有 `server`、`auth` 和 `admin` 设置；上游账号配置不代替用户登录配置。

```yaml
vault:
  address: https://vault.example.com
  mount: secret                 # 必须是 KV v2
  prefix: mcphub                # 个人账号存储前缀，每个部署独立
  role_id_env: MCPHUB_VAULT_ROLE_ID
  secret_id_env: MCPHUB_VAULT_SECRET_ID
  # auth_mount: approle
  # namespace: team-a           # Vault Enterprise 可选
  # ca_file: /etc/mcphub/vault-ca.pem

client_authorization:
  enabled: true
  client_id: mcphub-portal      # MCPHub 登录身份服务中的客户端

backends:
  - id: projects
    url: https://projects.example.com/mcp
    require_client_grant: true
    required_scopes: [projects:access]  # 访问 MCPHub 所需的 scope
    published_tools: [get_project, update_project]
    tool_rules:
      - match: get_project
        effect: read
      - match: update_project
        effect: write
    credentials:
      mode: personal
      discovery_path: discovery/projects
      oauth:
        issuer: https://accounts.example.com
        client_id: mcphub-projects       # 上游身份服务中的客户端
        scopes: [projects.read, projects.write] # 上游签发的 scope
        # client_secret_path: oauth/projects
```

在上游身份服务注册 `https://hub.example.com/client-auth/api/accounts/callback`，即 `server.public_url` 的 origin 加此路径。授权服务须提供 OIDC discovery、PKCE S256、ID Token，以及 endpoint 接受的 access token。支持公开客户端 `none`、机密客户端 `client_secret_basic` / `client_secret_post`。机密客户端的 Vault 路径内须有 `client_secret` 字段。

授权和换码请求携带 endpoint 完整 URL 作为 `resource`；MCPHub 校验 discovery issuer、浏览器会话与 state、授权响应 issuer、ID Token 的签名/audience/nonce，并执行无工具调用的 MCP 握手验证。只有全部成功，才替换原有账号。授权尝试五分钟后过期；拒绝或校验失败保留原连接。网络认证端点必须使用 HTTPS。

配置中的上游 scope 原样申请，并加入 `openid`；身份服务声明支持时加入 `offline_access`。刷新后权限收窄、刷新授权失效时要求重新连接。未签发 refresh token 也可连接，页面会提示到期后重连。

如果上游仅支持 PAT/API key，省略 `credentials.oauth`。用户在门户输入自己的 Token，可填写账号备注和到期时间。默认发送 `Authorization: Bearer <token>`，例如 API key 可设 `header: X-API-Key`、`scheme: ""`。PAT 的账号备注和有效期由用户提供，不是经过 OIDC 验证的身份声明。

共享凭证示例：

```yaml
credentials:
  mode: shared
  path: services/projects
  field: token
  header: Authorization
  scheme: Bearer
```

通过 Vault 自己的管理工具在 `secret/services/projects` 写入 `token` 字段。MCPHub 配置只填写挂载点内的路径 `services/projects`，不包含 `secret/` 或 `data/`。共享凭证不自动执行第三方 OAuth 刷新；轮换由 Vault 运维或上游系统负责。

`discovery_path` 是仅用于 MCP 目录发现的共享凭证，要求具备最小的初始化和列目录权限。目录允许匿名访问时可以省略。管理员发布的工具必须可被此发现连接看到；个人目录可以减少可见工具或改变说明文字，但工具 schema/策略仍须匹配用户确认过的授权。个人调用不会降级使用发现凭证；共享连接的提示词、资源和通知也不会混入个人视图。

## Vault 权限与运维

推荐 AppRole；开发环境也可用 `token_env: MCPHUB_VAULT_TOKEN` 替代两个 AppRole 环境变量。不要在 YAML 中放入 Vault Token 或 SecretID。AppRole 到期前重新登录，因此 SecretID 必须按部署方式配置有效期、使用次数及轮换。环境变量变更需要重启进程；配置检查使用 `lookup-self`，不证明所有业务路径均有权限。

以 `mount: secret`、`prefix: mcphub` 为例：

```hcl
path "secret/data/mcphub/accounts/*" {
  capabilities = ["create", "update", "read"]
}
path "secret/metadata/mcphub/accounts/*" {
  capabilities = ["delete"]
}
path "secret/data/discovery/projects" {
  capabilities = ["read"]
}
path "secret/data/oauth/projects" {
  capabilities = ["read"]
}
path "auth/token/lookup-self" {
  capabilities = ["read"]
}
```

按需增加共享凭证的精确读取路径。不要将 root Token 用于部署。Vault 地址默认必须 HTTPS，可配专用 CA；开发用 HTTP 仅允许显式启用 `allow_insecure_http` 的数字回环地址。客户端不跟随 Vault/OAuth/上游认证请求的重定向。

个人密文在 Vault KV v2；数据库保存加密的归属、endpoint UID、配置指纹、版本及账号说明。刷新使用进程内互斥和 KV CAS，多个本地连接器不会重复消费同一个 refresh token。只有新 Token 保存成功，调用才获得凭证。若刷新成功但持久化暂时失败，进程保留新 Token 后续重试写入；若此时进程崩溃，可能需要重新连接，不能保证跨 OAuth 服务和 Vault 的原子事务。

断开账号先撤销本地关联和 ClientGrant，再删除 Vault 中该凭证的全部版本；Vault 不可用时，持久化清理队列会继续重试。连接中断造成的孤立凭证也会进入清理队列。备份或灾难恢复必须协调数据库与 Vault；只恢复旧数据库可能恢复旧授权状态，恢复后应重新核对授权。

## 授权边界

- Hub 登录身份、ClientGrant、用户/组策略、scope、业务资源限制、工具发布/启停和写审批继续独立生效。上游权限再决定真实可执行范围；两种 scope 不进行字符串互换。
- Broker/Agent 获得的是 Hub 授权。上游 access/refresh token 仅在服务器访问 Vault、OAuth 服务和指定 endpoint 时使用，不提供导出接口。请求诊断新增凭证关联 ID 和上游账号说明，不记录 Token。
- MCP 会话、资源链接与订阅按用户/Grant 隔离。相同操作因网络失败不会自动重放。授权撤销只能取消在途请求，不能保证撤销已提交到后端的写入。
- KV 存储/删除不等于上游授权撤销；本版不会调用各供应商的 revoke API。必要时还需在上游服务撤销授权。Vault 管理员、Hub 进程及可修改配置的管理员属于信任边界，仍需最小权限、独立安全审批和网络隔离。
- 首版覆盖远端 MCP endpoint，尚未接入 HTTP 工具组、动态数据库/云凭证、Vault Agentic IAM/OBO 或多实例刷新协调。个人 OAuth 当前要求 OIDC，不包含各 SaaS 专有 OAuth 适配。
- endpoint 必须接受并按个人凭证鉴权。若 endpoint 内部固定使用共享数据库账号，仅在 Hub 接入 Vault 不会把数据库权限自动变成个人权限。

参考：[Vault KV v2 API](https://developer.hashicorp.com/vault/api-docs/secret/kv/kv-v2)、[AppRole](https://developer.hashicorp.com/vault/docs/auth/approle)。
