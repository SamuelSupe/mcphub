# MCPHub documentation

[中文](README.zh-CN.md) · [Project home](../README.md)

These documents cover v2.2.0. Choose a manual for your role, then use the references when you need details.

## User manual

**[Online help center (Chinese)](https://samuelsupe.github.io/mcphub/)**: browse 19 task-oriented guides with search, page outlines and mobile navigation.

**[Open the user manual](user-guide.md)**: for employees using Codex, Claude Code or another MCP client.

1. [Prepare and install the CLI](user-guide.md#before-you-start)
2. [Run setup and connect an MCP client](user-guide.md#connect-an-mcp-client)
3. [Connect a personal upstream account](user-guide.md#connect-a-personal-account)
4. [Handle write approvals](user-guide.md#write-approvals) and [manage client grants](user-guide.md#manage-client-authorizations)
5. [Diagnose connection problems](user-guide.md#diagnostics-and-troubleshooting)

## Administrator manual

**[Online administrator documentation (Chinese)](https://samuelsupe.github.io/mcphub/admin/)**: 20 chapters with the same navigation and full-text search as the user guide, published automatically from the source Markdown.

**[Open the administrator manual](admin-guide.md)**: for deployment, access control, governance and operations.

1. [Prepare and deploy](admin-guide.md#preparation-and-installation)
2. [Connect services and publish tools](admin-guide.md#connect-services-and-publish-tools)
3. [Configure users and organizations](admin-guide.md#users-and-organizations), [client authorization](admin-guide.md#enable-client-authorization) and [upstream accounts](admin-guide.md#configure-upstream-accounts)
4. [Configure write approval and governance](admin-guide.md#write-approval-and-configuration-governance)
5. [Inspect grants and requests](admin-guide.md#grant-and-request-diagnostics), [upgrade and back up](admin-guide.md#upgrades-and-backups)

## Configuration and topic references

| Topic | English | Chinese |
| --- | --- | --- |
| YAML, tool policies, management APIs, protocol and reloads | [Reference](configuration.md) | [配置与协议参考](configuration.zh-CN.md) |
| HTTPS, remote administration, SQLite / PostgreSQL | [Deployment](../deploy/README.md) | [部署指南](../deploy/README.zh-CN.md) |
| SSO, user policies, department/group sync and administrator recovery | [Administrator guide](sso-and-user-management.md) | [管理员专题](sso-and-user-management.zh-CN.md) |
| Vault shared/personal account configuration, policies and operations | [Administrator guide](vault-accounts.md) | [管理员专题](vault-accounts.zh-CN.md) |
| Security boundaries and vulnerability reporting | [Security policy](../SECURITY.md) | [运行安全](admin-guide.zh-CN.md#安全说明) |

## Architecture, design and release material

Use these for design context and historical records. Start with the manuals for everyday commands.

- [Feishu SSO, Vault and enterprise Agent architecture](feishu-vault-agent-architecture.zh-CN.md) (Chinese): integration design and pending acceptance work, with [configuration](../deploy/config.feishu-vault.example.yaml), [SVG](diagrams/feishu-vault-agents.svg) and [PNG](diagrams/feishu-vault-agents.png).
- [27-page architecture PDF](feishu-vault-agent-architecture.zh-CN.pdf): the 2026-09-26 review snapshot; current status is maintained in the online architecture document.
- [Client Broker and authorization design](broker-authorization-design.zh-CN.md) (Chinese): historical first-version snapshot with schema and capability changes through v2.2.
- [Screenshot notes](screenshots/README.md): provenance of the v2.1.0 demonstration screenshots.
- [v2.2.0 release and upgrade notes](../RELEASE_NOTES_v2.2.0.md), [all releases](https://github.com/SamuelSupe/mcphub/releases).
- [Contributing](../CONTRIBUTING.md): development, validation and documentation conventions.
