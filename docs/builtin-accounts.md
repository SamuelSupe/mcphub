# Built-in accounts, passwords and MFA

[中文](builtin-accounts.zh-CN.md) · [Administrator manual](admin-guide.md)

The default installation uses MCPHub accounts. A fresh deployment needs the public HTTPS gateway URL and configuration encryption key, without a separate identity service. Enterprise LDAP/OIDC is optional.

## Initialize and sign in

1. Validate and start MCPHub with the packaged `config.example.yaml`. The console initially asks for local initialization.
2. In another terminal on the server, load the same deployment environment and run `mcphub init-admin --config config.yaml --username admin`. Password input is hidden and must be confirmed; use at least 12 characters.
3. Open `http://127.0.0.1:8081/` and sign in. Remote deployments use the configured HTTPS administrator URL.
4. Create groups in **Users & groups**, assign roles, scopes, services, tools and resource permissions to those groups, then create users and add them to groups. New accounts can sign in, but cannot call tools until authorized.

Initialization is local only. There is no anonymous web bootstrap or default password, and repeat initialization is rejected. Initialization creates the **Administrators** group with the `admin` role and adds the first account to it; it gives that user no direct permissions. The command reads YAML and environment variables; the second terminal must also set `MCPHUB_PUBLIC_URL` and the same `MCPHUB_CONFIG_KEY`.

For automated installations, `--password-stdin` reads one password line. Keep passwords out of command arguments, public scripts and YAML.

## Groups and permissions

Users hold account state and group membership. Roles, scopes, service, tool and business-resource grants belong to groups; direct user grants are rejected. Users may join multiple groups and inherit the union of active groups from the same identity provider. Disabled groups grant nothing. Groups are flat, without nesting.

1. Select **Create group**, enter a name and choose the identity provider. Use MCPHub for local accounts or the configured enterprise provider for enterprise identities. Membership cannot cross providers.
2. Expand the group and assign administration, reviewer or security roles, scopes, services, exact original tool names and resource conditions. New groups have no grants.
3. Expand a user, select **Groups** and save. New local users can sign in but have no groups or tool access; enterprise users must also be enabled in MCPHub.
4. Sign in again and verify the tool catalog and a real call. Removing membership, disabling a group or revoking its grants recomputes access and blocks unauthorized calls. Added scopes need a new login to expand the credential ceiling.

Multiple groups provide a union, without deny overrides. A narrow grant cannot cancel another group's broad grant. The tool, write-request flag and all resource conditions must match within one access entry; permissions from different groups cannot be stitched into a broader entry. Writes still require individual approval.

Enterprise claim/directory memberships are read-only in the console. Administrators can also create locally managed policy groups for that enterprise provider and add users manually. Login and directory synchronization preserve these memberships, and an upstream source cannot add users to policy groups. Directory-deactivated users remain denied even if a policy membership persists.

The last effective administrator cannot be disabled, removed from its administration group, or lose access through disabling/demoting the group. Add another active user to a group with administration access first.

## Passwords, disabling and recovery

Users change their password in **My account** in the console or personal authorization portal. Administrators reset a local user's password or disable the account in **Users & groups**. Deliver reset passwords through a trusted channel and ask the user to change them.

Changing or resetting a password, or disabling an account, revokes access sessions and refresh credentials. Unredeemed authorization codes from before the reset are invalid. Re-enabling an account does not restore old sessions. The last active local administrator cannot be disabled or lose administration permissions.

Passwords are stored as [Argon2id](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html) hashes with random salts: 19 MiB, 2 iterations and 1 lane. Five consecutive failures lock an account for five minutes. Separate IP and concurrent hashing limits protect login capacity. Restarting does not clear account lockouts.

Concurrent logins share the same account failure counters and authenticator usage record. A TOTP code can succeed only once; a password reset or account disable during login invalidates the old password verification result.

Another administrator can reset a forgotten password. If the sole administrator loses a password or authenticator, an operator with server access and the database encryption key can recover locally:

```bash
mcphub init-admin --config config.yaml --username admin --reset
# Clear MFA only when the authenticator is lost:
mcphub init-admin --config config.yaml --username admin --reset --reset-mfa
```

Recovery does not enable disabled users, change group membership or alter group permissions. It revokes existing sessions. Protect server access, the database and its matching encryption key.

## MFA and approval verification

In **My account**, enter your current password, select **Enable MFA**, add the displayed secret to a TOTP authenticator, and confirm with its six-digit code. The secret is encrypted with the configuration key. Confirmation signs out all sessions.

Once enabled, every local account sign-in and password change requires both the password and TOTP. The implementation uses [RFC 6238](https://www.rfc-editor.org/rfc/rfc6238) SHA1, six digits and a 30-second interval, allowing one adjacent interval. Codes cannot be reused; wait for the next interval after enrollment or sign-in before another authentication.

An approval with `require_step_up: true` requires a fresh password and authenticator code for that specific approval. The proof is bound to the same user, browser session and approval, expires after two minutes and is single-use. **Ordinary password sign-in is not MFA and cannot satisfy step-up verification.** The local MFA ACR is `urn:mcphub:auth:password-totp`. Accounts without an authenticator must enroll before approving such requests.

Enterprise identities use their OIDC provider's verification. Explicitly configure and verify enterprise ACRs. Plain OAuth2 UserInfo cannot prove MFA. MFA neither grants a reviewer role nor replaces review of the requested operation.

## OAuth and enterprise identities

With `auth.mode: builtin`, the issuer is derived from the HTTPS origin of `server.public_url`, using `/sso`. Do not set `MCPHUB_AUTH_ISSUER`. The console, personal portal and MCPBridge share permissions inherited from groups and Hub token/refresh flow.

Default public OAuth registrations:

| Client ID | Purpose | Resource |
| --- | --- | --- |
| `mcpbridge` | User connector; loopback callbacks may use an ephemeral port | Complete MCP URL |
| `mcpbridge-admin` | Remote administration CLI | Administrator origin |
| `mcphub-admin` | Console browser | Administrator origin |
| `mcphub-portal` | Personal authorization portal | Complete MCP URL |

Clients use standard authorization code, PKCE S256, resource binding and rotating refresh credentials. Proxy `/sso/*`, `/.well-known/oauth-authorization-server/sso`, `/.well-known/openid-configuration/sso`, MCP metadata and `/client-auth/*` to the gateway. Local console sign-in does not depend on the public address being reachable; remote users and MCPBridge need working, trusted HTTPS.

To add enterprise login, configure [LDAP and OIDC](enterprise-login.md) in the console’s Identity services page; both can be enabled together. The login page offers enterprise sign-in, with a LDAP account selector on the authorization page. Enterprise and local accounts have separate identity records and groups: matching names never merge accounts or inherit access. New enterprise identities start pending. Directory synchronization updates only the configured enterprise provider. Keep a local administrator for local recovery.

Advanced deployments may instead use `auth.mode: external` with an external `auth.issuer`, or the pure YAML example. Those independent deployment options do not offer built-in accounts and are not required for default installation.
