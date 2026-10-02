# MCPHub v2.3.0

This release is designed for fresh deployments; no migration procedure for existing configurations or databases is included.

## Built-in accounts by default

- The default and remote administration templates use built-in authentication, persistent SQLite or PostgreSQL storage, and an empty backend list. The local default requires only `MCPHUB_PUBLIC_URL` and `MCPHUB_CONFIG_KEY`; the issuer is derived from the public gateway origin.
- Initialize the first administrator locally with `mcphub init-admin`. There is no default password or anonymous browser bootstrap. Initialization creates an Administrators group; create groups, assign their permissions and add users in the console.
- Permissions belong only to groups. Users inherit active group grants; direct user grants and cross-provider membership are rejected. Group edits and membership removal immediately recompute authorization. Imported enterprise memberships remain owned by the identity source.
- Passwords use salted Argon2id hashes. Password changes, resets and account disabling revoke access and refresh sessions. Persistent account lockouts and bounded login limits protect authentication.
- Local accounts can enroll TOTP MFA. Approval step-up requires a fresh password and authenticator code bound to the specific request and browser session; ordinary password sign-in never counts as MFA.
- The console, personal portal and MCPBridge reuse the existing permissions and OAuth authorization code, PKCE, access-token and rotating refresh flows. LDAP and OIDC are optional, can run together and are configured/tested in the console. Credentials are encrypted; saving applies immediately and revokes sessions for changed sources. LDAP uses verified LDAPS or mandatory StartTLS, stable UUID/GUID identities and source-owned group membership; password login is single-factor.
- Chinese and English account, installation and configuration documentation describe the same fresh deployment flow and packaged templates. The server remains `mcphub`; the client remains `mcpbridge`.

默认使用内建账号：启动网关 → 本机初始化管理员 → 登录管理台 → 创建用户和组、配置组权限、接入后端 → 测试连接 → 发布工具。企业 SSO 为可选功能；本版本仅面向新部署。

## Agent device authorization

- Builtin RFC 8628 device endpoint and one-page local / LDAP / OIDC sign-in plus explicit service/tool consent. Requests persist in SQLite and PostgreSQL, enforce polling limits, and deliver credentials once.
- `mcpbridge pair start/finish --json` and `connect --interactive-auth` expose public pairing status to Agents without revealing credentials; ready requires private installation and an MCP connectivity check.
- New deployment templates require ClientGrant. Group permissions and existing write approvals remain authoritative; revoked access requires explicit reauthentication.
- Bilingual guides and the generated documentation site describe the same release behavior and include matching installation templates.

## Reliability and performance audit

- Completed pairing requests cannot cancel an already delivered login session after a client grant is revoked. Credential delivery also checks the confirmed grant and Broker session deadlines before creating an SSO family.
- SQLite read-modify-write transactions reserve the writer before reading, preventing snapshot-upgrade failures during concurrent permission revocation or local administration. Connection replacement retains foreign-key enforcement and busy waiting; business mutations are not replayed.
- Password hashing runs outside identity admission and database locks; fresh credential, lockout and TOTP checks remain serialized. Group permissions use one batch read, and enterprise source updates scan SSO families once for all affected users.
- Interactive business calls reuse the connected MCP session and rely on the gateway's live authorization checks. Status and catalog refresh still validate authorization; failed calls are not replayed. Temporary private-file failures preserve the pending request, and delayed failures from an old connection cannot close a replacement.
- Endpoint policy lookup reuses the transaction-aware implementation; catalog reconciliation is linear, with a database index for pairing grant lookups. No cache or dependency was added.

审计修复了旧配对申请误删已交付会话、过期授权仍领取凭证的问题；密码散列移出全局锁，组权限批量读取，业务调用不再重复查询授权。回归、性能结果和验收范围见 [审计记录](docs/implementation-audit.zh-CN.md)。

## Build security

- Source builds require Go 1.26.8 or newer, and the container build uses Go 1.26.8. This removes the standard-library security defects present in older candidate binaries built with Go 1.26.0. CI and release workflows now check reachable vulnerabilities with govulncheck before packaging.
- The Feishu/Vault architecture PDF is the explicitly archived September 26, 2026 review snapshot. Use the current bilingual installation guides and configuration examples for v2.3.0 deployment.
