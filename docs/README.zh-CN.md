# MCPHub 文档导航

[English](README.md) · [项目首页](../README.zh-CN.md)

文档对应 v2.2.0。先按职责选择手册，具体字段和专题在需要时查阅。

## 用户手册

**[在线帮助中心](https://samuelsupe.github.io/mcphub/)**：按实际任务浏览 19 篇用户指南，支持全文搜索、页内目录和移动端阅读。

**[打开用户手册](user-guide.zh-CN.md)**：面向使用 Codex、Claude Code 或其他 MCP 客户端的员工。

1. [准备信息与安装 CLI](user-guide.zh-CN.md#接入前准备)
2. [运行向导并接入 MCP 客户端](user-guide.zh-CN.md#接入-mcp-客户端)
3. [连接个人上游账号](user-guide.zh-CN.md#连接个人账号)
4. [处理写审批](user-guide.zh-CN.md#写操作审批)、[管理客户端授权](user-guide.zh-CN.md#管理客户端授权)
5. [诊断与常见问题](user-guide.zh-CN.md#诊断与常见问题)

## 管理员手册

**[打开管理员手册](admin-guide.zh-CN.md)**：面向负责部署、权限、安全治理和运行维护的人员。

1. [部署与安装](admin-guide.zh-CN.md#部署准备与安装)
2. [接入服务并发布工具](admin-guide.zh-CN.md#服务接入与工具发布)
3. [配置用户与组织](admin-guide.zh-CN.md#用户与组织)、[客户端授权](admin-guide.zh-CN.md#启用客户端授权)和[上游账号](admin-guide.zh-CN.md#配置上游账号)
4. [配置写审批与治理](admin-guide.zh-CN.md#写审批与配置治理)
5. [查询授权和请求诊断](admin-guide.zh-CN.md#授权与请求诊断)、[升级与备份](admin-guide.zh-CN.md#升级与备份)

## 配置与专题参考

| 查阅内容 | 中文 | English |
| --- | --- | --- |
| YAML、工具策略、管理 API、协议与热重载 | [配置与协议参考](configuration.zh-CN.md) | [Reference](configuration.md) |
| HTTPS、远程管理、SQLite / PostgreSQL | [部署指南](../deploy/README.zh-CN.md) | [Deployment](../deploy/README.md) |
| SSO、用户权限、部门/组同步与管理员恢复 | [管理员专题](sso-and-user-management.zh-CN.md) | [Administrator guide](sso-and-user-management.md) |
| Vault 共享/个人账号配置、权限与运维 | [管理员专题](vault-accounts.zh-CN.md) | [Administrator guide](vault-accounts.md) |
| 安全边界与漏洞报告 | [运行安全](admin-guide.zh-CN.md#安全说明) | [Security policy](../SECURITY.md) |

## 架构、设计与版本资料

这些资料用于理解设计和核对历史，不作为首次接入步骤。日常命令以两份手册为入口。

- [飞书 SSO、Vault 与企业 Agent 架构](feishu-vault-agent-architecture.zh-CN.md)：跨系统集成方案及待验收事项；附[配置示例](../deploy/config.feishu-vault.example.yaml)、[SVG](diagrams/feishu-vault-agents.svg)和 [PNG](diagrams/feishu-vault-agents.png)。
- [27 页架构 PDF](feishu-vault-agent-architecture.zh-CN.pdf)：2026-09-26 的评审快照，当前状态以在线架构文档为准。
- [客户端 Broker 与授权同步设计](broker-authorization-design.zh-CN.md)：首版历史快照，附 v2.0–v2.2 的 schema 与能力变化。
- [界面截图说明](screenshots/README.md)：v2.1.0 演示环境截图及来源。
- [v2.2.0 发行与升级说明](../RELEASE_NOTES_v2.2.0.md)、[全部发行版本](https://github.com/SamuelSupe/mcphub/releases)。
- [贡献指南](../CONTRIBUTING.md)：开发、验证及文档维护约定。
