# Configure LDAP and OIDC in the console

> This guide covers v2.4.0 fresh deployments and contains no migration workflow.

Keep MCPHub's built-in accounts enabled. Sign in as an administrator and open **Access control → Identity services** to configure one LDAP connection and one OIDC connection together. Service passwords and client secrets are encrypted with the configuration key in SQLite or PostgreSQL. Read APIs show only whether a secret is configured; leaving its form field blank keeps the current value.

Saving applies immediately without environment changes or a restart. After the first save, database connection settings take precedence over YAML `auth.sso.upstream`. Replacing or disabling a connection revokes its sessions, refresh credentials and pending authorizations; enabling it again does not revive old credentials. Changing the OIDC issuer, client ID, subject or tenant boundary, or the LDAP address, user/group search bases, filters or stable ID attributes creates a new source namespace whose users do not inherit the previous source's grants.

UI configuration requires `auth.mode: builtin` and managed storage. Standalone external authentication remains YAML-managed. Retain a local recovery administrator and server access. Connection tests grant no user permissions.

## Configure OIDC

1. Register a confidential web client with authorization code flow and PKCE S256.
2. Register the exact **Callback URL** shown in the console: `https://<public MCP host>/sso/callback`.
3. Enable OIDC and enter the HTTPS issuer, client ID, client secret and authentication method. Both `client_secret_post` and `client_secret_basic` are supported.
4. Expand **Identity mapping and tenant restrictions** to configure requested scopes (usually `openid profile email`) and display name, group and department claims. Group and department claims must be arrays of strings. The subject always uses the signed `sub` claim. A tenant restriction requires both its claim and allowed value.
5. Select **Verify OIDC sign-in and mapping**, open the test sign-in, authenticate with your enterprise provider, then return to the console. Review the stable subject, name, groups and departments. The draft client secret is used for the code exchange; signature, nonce and tenant checks match ordinary sign-in. Configured claims that were not returned are flagged. **Check connectivity** checks discovery and signing keys only and does not replace this test.
6. Save, review the affected source’s user count and session revocation notice, then confirm. The UI requires changed, enabled sources to pass a test of the current draft. Edits or five minutes invalidate the result. Tests do not create users, grant permissions or replace the active source. After saving, verify actual access with a regular enterprise user.

MCPHub issues credentials to the admin console, user portal and MCPBridge; upstream identity tokens do not reach MCP clients. First-time enterprise users start pending. Enable their accounts in **Users & groups** and grant access through independent permission groups and explicit organization mappings.

Enterprise step-up still requires configured and verified ACR and MFA/Passkey policies in [approval settings](configuration.md). Saving connection settings does not enable or prove MFA.

## Configure LDAP

MCPHub binds a service account, searches for a unique user DN, then binds that DN with the supplied password. It accepts only **LDAPS** or mandatory **StartTLS** and always verifies the server certificate and hostname. Certificate verification cannot be disabled. Empty passwords never trigger a bind.

| Field | OpenLDAP example | Active Directory example |
| --- | --- | --- |
| LDAP URL | `ldaps://ldap.example.com:636` | `ldaps://ad.example.com:636` |
| Service account bind DN | Full DN with search access | Full DN with search access |
| User search base DN | `ou=people,dc=example,dc=com` | `dc=example,dc=com` |
| User search filter | `(&(objectClass=person)(uid={{username}}))` | `(&(objectClass=user)(sAMAccountName={{username}})(!(userAccountControl:1.2.840.113556.1.4.803:=2)))` |
| Stable user ID attribute | `entryUUID` | `objectGUID` |
| User display name attribute | `cn` | `displayName` |
| Group search base DN | `ou=groups,dc=example,dc=com` | `dc=example,dc=com` |
| Group search filter | `(&(objectClass=groupOfNames)(member={{dn}}))` | `(&(objectClass=group)(member={{dn}}))` |
| Stable group ID attribute | `entryUUID` | `objectGUID` |
| Group display name attribute | `cn` | `cn` |

`ldap://host:389` performs StartTLS before sending any bind password. Paste a private CA as PEM certificates if needed; otherwise system trust is used. Give the service account only user/group search access. Do not put credentials in the URL.

The user filter must contain exactly one `{{username}}`. Group filters use `{{dn}}` or `{{username}}`; substituted values are escaped. User searches must return exactly one entry. Ambiguous matches, search failures or more than 256 groups deny sign-in instead of granting partial access. IDs must be unique, nonempty and stable across renames. Binary AD GUIDs and textual UUIDs are encoded as opaque identity subjects.

All group search fields may be blank; administrators can then create independent permission groups and add enterprise users directly. Group search requires its base DN, filter, stable ID and display name attributes together. Nested groups are not traversed automatically.

Use **Check connectivity** to check TLS, the service bind and user/group base DNs. Before saving, select **Verify LDAP sign-in and mapping** and enter a regular test account and password to verify the unique user match, stable ID, user bind and group search. Review the name and group mapping, then save. The test password is used once and never saved; no users are provisioned and the active source is unchanged. Directory failure counters and lockout policies still apply. Users select **LDAP** on the enterprise login page and enter directory credentials. MCPHub does not store directory user passwords.

LDAP sign-in has per-source/username and shared IP rate limits plus a concurrency bound; retain the directory's own lockout policy. LDAP password sign-in is single-factor and cannot approve writes that require step-up. Those approvals need a built-in TOTP reviewer or verified enterprise OIDC step-up.

## Grant group permissions

1. Sign in once with a test enterprise account and verify stable identity IDs, source, user and organization groups.
2. Enable the user in MCPHub. Create an independent permission group and select services, tools, roles and resource conditions.
3. Under **Organization group mappings**, select the LDAP/OIDC groups. One permission group may cover multiple providers while accounts remain isolated.
4. Derived scopes save the current access intent as a snapshot. Explicit scopes must cover selected capabilities. Inspect effective access and its direct or mapped sources.
5. Sign in again and test discovery and calls. Unpublished or unauthorized tools are hidden; writes still require individual approval.

The provider owns organization membership. The console does not edit business permissions on organization groups. Mappings use stable IDs; identical names or emails never substitute identities. Removing mappings or active organization membership withdraws inherited access.

LDAP/OIDC login refreshes membership; complete directory snapshots also refresh verification. `auth.enterprise_membership_max_age` defaults to `24h`. After expiry, refresh and tool access fail until login or directory synchronization. MCPHub does not poll LDAP. Block access immediately by disabling the user, organization group or identity connection, or removing permission mappings. Access tokens last at most 10 minutes and refresh sessions at most 8 hours; service grants have independent deadlines.

## Acceptance and troubleshooting

- OIDC callback failure: check exact issuer/callback matching, secret, authentication method and PKCE S256; verify subject, tenant and membership claim types.
- LDAP TLS failure: check port, certificate hostname, validity, CA chain and StartTLS support. Keep certificate verification enabled.
- Service bind failure: verify the complete service DN, password and search access.
- User sign-in failure: verify one matching entry, password, readable stable ID and complete group results. Directory lockouts remain effective.
- Sign-in succeeds without access: enable the MCPHub account, assign permissions through independent permission groups, then sign in again. A same-named local group does not establish enterprise membership.
- Save conflict: refresh the current revision and reapply the edit. `If-Match` prevents overwriting another administrator's settings.

Admin endpoints are `GET /api/v1/identity-providers`, `PUT /api/v1/identity-providers` and `POST /api/v1/identity-providers/probe`. Writes and probes require administrator access, browser CSRF/origin checks and the current `If-Match` (initially `"0"`). PUT replaces the full `oidc` and `ldap` settings; omit secret fields to keep them, or explicitly send an empty string to clear them. Probe uses `source: oidc` or `source: ldap`, does not save and grants no permissions.

`POST /api/v1/identity-providers/test` accepts the same draft and `source`; LDAP also requires `username` and `password`. OIDC returns a one-use `start_url`. After opening it in a browser, poll `GET /api/v1/identity-providers/tests/{id}` as the initiating administrator. Tests reuse `/sso/callback`, so no additional redirect registration is needed. Results are owner-only and expire after five minutes; saving configuration or restarting also invalidates them. Responses contain no tokens or passwords. The UI adds testing and impact review before saving; direct PUT callers remain responsible for testing and acceptance.
