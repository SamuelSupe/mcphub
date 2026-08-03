---
name: Bug report / 缺陷报告
about: Report a reproducible MCPHub behavior or documentation defect / 报告可复现的 MCPHub 行为或文档缺陷
title: "[Bug] "
labels: "bug"
assignees: ""
---

## English

### Summary

<!-- What happened, and what did you expect? What is the user-visible impact? -->

### Environment

- MCPHub commit/tag:
- Go version or image tag:
- Deployment shape (direct process / reverse proxy / Docker):
- OS/architecture:
- Backend MCP implementation and URL shape (redact credentials):

### Configuration and request

<!-- Include the smallest relevant redacted YAML. Replace all secrets/tokens with placeholders. -->

```yaml
# redacted configuration
```

<!-- Include a minimal redacted request/response or log excerpt. Never include tokens or secrets. -->

### Reproduction

1.
2.
3.

### Evidence

- Expected:
- Actual:
- HTTP status, `WWW-Authenticate`, `X-Request-Id`, or readiness JSON (if relevant):
- Logs (redacted):

### Scope check

- [ ] I checked the README known-limits section.
- [ ] This is not a request for an explicitly excluded capability such as stdio, standalone legacy SSE, native TLS, Tasks, MCP Apps, or custom MCP extensions.
- [ ] I removed secrets, bearer tokens, private keys, and personal data.

## 中文

### 摘要

<!-- 发生了什么？你期望什么行为？用户可见影响是什么？ -->

### 环境

- MCPHub commit/tag：
- Go 版本或镜像 tag：
- 部署形态（直接进程 / 反向代理 / Docker）：
- OS/架构：
- 后端 MCP 实现和 URL 形态（删除凭证）：

### 配置与请求

<!-- 提供最小相关的脱敏 YAML。所有 secret/token 用占位符替换。 -->

```yaml
# 脱敏后的配置
```

<!-- 提供最小的脱敏请求/响应或日志片段。绝不要包含 token 或 secret。 -->

### 复现步骤

1.
2.
3.

### 证据

- 预期：
- 实际：
- HTTP 状态、`WWW-Authenticate`、`X-Request-Id` 或就绪 JSON（如相关）：
- 日志（脱敏）：

### 范围确认

- [ ] 我已检查 README 的已知限制。
- [ ] 这不是对明确排除能力（例如 stdio、独立旧 SSE、原生 TLS、Tasks、MCP Apps 或自定义 MCP 扩展）的请求。
- [ ] 我已删除 secret、Bearer token、私钥和个人数据。
