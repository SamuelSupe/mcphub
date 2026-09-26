# MCPHub 文档导航

[English](README.md) · [项目说明](../README.zh-CN.md) · [v2.1.0 发行说明](../RELEASE_NOTES_v2.1.0.md)

## 企业架构与最佳实践

[飞书 SSO、Vault 与企业 Agent 完整指南](feishu-vault-agent-architecture.zh-CN.md)覆盖人员身份、用户和客户端权限、个人上游账号、写审批、Codex / Claude Code 配置、运维及上线验收。

![企业 Agent 接入架构](diagrams/feishu-vault-agents.svg)

- [27 页完整 PDF](feishu-vault-agent-architecture.zh-CN.pdf)：包含渲染后的时序图、可点击目录和完整配置附录。PDF 是 2026-09-26 的评审快照，当前版本状态以在线指南为准。
- [部署配置示例](../deploy/config.feishu-vault.example.yaml)：包含占位配置和待真实租户验证的飞书端点。
- [可缩放 SVG](diagrams/feishu-vault-agents.svg)与 [PNG](diagrams/feishu-vault-agents.png)。

飞书确认身份，MCPHub 检查用户、客户端、工具、scope、资源与写审批，Vault 保管上游凭证，业务 endpoint 继续执行自己的权限检查。指南明确列出飞书目录适配器、真实租户及 MFA 的待完成集成验收要求。

## 配置与操作指南

| 主题 | 中文 | English |
| --- | --- | --- |
| Vault 共享/个人账号及凭证生命周期 | [说明](vault-accounts.zh-CN.md) | [Guide](vault-accounts.md) |
| SSO、用户权限与部门/组同步 | [说明](sso-and-user-management.zh-CN.md) | [Guide](sso-and-user-management.md) |
| 远程管理、SQLite 与单实例 PostgreSQL | [部署](../deploy/README.zh-CN.md) | [Deployment](../deploy/README.md) |
| 客户端 Broker 与授权同步设计 | [设计](broker-authorization-design.zh-CN.md) | - |
| 安全边界 | [安全说明](../README.zh-CN.md#安全说明) | [Policy](../SECURITY.md) |

## 推荐落地顺序

1. 用真实身份服务验证一个显式发布的只读 endpoint。
2. 配置用户/组织权限，完成目录同步的完整性与离职验收。
3. 连接个人上游账号，按 Agent 和 endpoint 分别确认客户端授权。
4. 授权、所需 MFA、撤销、审计与恢复检查通过后，再开放独立审批的写操作。

MCPHub 保持单活部署。PostgreSQL 和 Vault 可以各自高可用，但不会自动提供 Hub 多实例一致性。
