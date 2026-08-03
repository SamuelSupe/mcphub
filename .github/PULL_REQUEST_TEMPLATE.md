## English

### Summary

<!-- What changed and why? Keep the scope focused. -->

### Observable behavior

- User/operator behavior:
- MCP/HTTP/config contract:
- Failure/readiness/reload behavior:

### Security and compatibility

- Authentication/scope impact:
- Backend transport/header impact:
- Reverse-proxy or deployment assumptions:
- Known limits or explicitly excluded capabilities:

### Validation

<!-- List exact commands and results. Say why a check was not run. Do not claim tests you did not run. -->

```text
command:
result:
```

### Checklist

- [ ] I read the relevant README and SECURITY guidance.
- [ ] I preserved the current MCP wire contract and namespace/URI mapping.
- [ ] I added or updated a behavior-boundary test when the change warrants one.
- [ ] I updated both `README.md` and `README.zh-CN.md` when user-facing semantics changed.
- [ ] I removed secrets, tokens, private keys, and production personal data.
- [ ] I did not add a promise for stdio, standalone legacy SSE, native TLS, Tasks, MCP Apps, or custom MCP extensions.

## 中文

### 摘要

<!-- 改了什么？为什么？保持范围聚焦。 -->

### 可观察行为

- 用户/运维行为：
- MCP/HTTP/配置契约：
- 失败/就绪/重载行为：

### 安全与兼容性

- 认证/scope 影响：
- 后端传输/请求头影响：
- 反向代理或部署假设：
- 已知限制或明确排除能力：

### 验证

<!-- 列出准确命令和结果；未运行的检查说明原因。不要声称执行过未执行的测试。 -->

```text
命令：
结果：
```

### 检查清单

- [ ] 我已阅读相关 README 和 SECURITY 指南。
- [ ] 我保留了当前 MCP wire contract 以及命名空间/URI 映射。
- [ ] 需要时，我添加或更新了行为边界测试。
- [ ] 用户可见语义变化时，我同时更新了 `README.md` 和 `README.zh-CN.md`。
- [ ] 我已删除 secret、token、私钥和生产个人数据。
- [ ] 我没有为 stdio、独立旧 SSE、原生 TLS、Tasks、MCP Apps 或自定义 MCP 扩展增加承诺。
