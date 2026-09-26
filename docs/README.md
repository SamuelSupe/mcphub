# MCPHub documentation

[中文](README.zh-CN.md) · [Project README](../README.md) · [v2.1.0 release notes](../RELEASE_NOTES_v2.1.0.md)

## Enterprise architecture and best practices

The [Feishu SSO, Vault and Agent integration guide](feishu-vault-agent-architecture.zh-CN.md) covers identity, user and client permissions, personal upstream accounts, write approval, Codex / Claude Code configuration, operations and deployment acceptance. The full guide is currently in Chinese.

![Enterprise Agent architecture](diagrams/feishu-vault-agents.svg)

- [27-page architecture PDF](feishu-vault-agent-architecture.zh-CN.pdf), with rendered sequence diagrams, a linked table of contents and the full configuration appendix. This is the 2026-09-26 review snapshot; current release status is maintained in the online guide.
- [Deployment configuration](../deploy/config.feishu-vault.example.yaml), using placeholders and candidate Feishu endpoints that require tenant validation.
- [Scalable SVG](diagrams/feishu-vault-agents.svg) and [PNG](diagrams/feishu-vault-agents.png).

Feishu establishes identity; MCPHub enforces user, client, tool, scope, resource and write-approval policies; Vault stores upstream credentials. Business endpoints retain their own authorization checks. The guide explicitly identifies the pending Feishu directory adapter and real-tenant/MFA acceptance requirements.

## Configuration guides

| Topic | English | Chinese |
| --- | --- | --- |
| Vault shared/personal accounts and credential lifecycle | [Guide](vault-accounts.md) | [说明](vault-accounts.zh-CN.md) |
| SSO, user permissions and directory synchronization | [Guide](sso-and-user-management.md) | [说明](sso-and-user-management.zh-CN.md) |
| Remote administration, SQLite and single-instance PostgreSQL | [Deployment](../deploy/README.md) | [部署](../deploy/README.zh-CN.md) |
| Client Broker and synchronized authorization design | - | [设计](broker-authorization-design.zh-CN.md) |
| Security boundaries | [Policy](../SECURITY.md) | [安全说明](../README.zh-CN.md#安全说明) |

## Recommended rollout

1. Validate one explicitly published read-only endpoint with the real identity provider.
2. Configure local user/organization permissions and complete directory synchronization checks.
3. Connect personal upstream accounts and issue separate grants for each Agent and endpoint.
4. Enable independently approved writes only after authorization, MFA where required, revocation, audit and recovery checks pass.

Deploy one active MCPHub process. PostgreSQL and Vault may use their own HA arrangements; they do not add multi-instance coordination to the Hub.
