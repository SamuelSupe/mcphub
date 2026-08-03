# MCPHub v1.1.0 — Unreleased / 待发布

This document describes the v1.1.0 development line; it is not a published release. The latest published and downloadable release remains [v1.0.0](https://github.com/SamuelSupe/mcphub/releases/tag/v1.0.0), so installation and archive links stay on v1.0.0 until this development line is released.

Repository: [SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · module: `github.com/SamuelSupe/mcphub` · license: [Apache License 2.0](LICENSE)

## English

### Development scope

- Adds backend-local `tool_rules`. Each rule has a `match` glob and `required_scopes`.
- Evaluates `match` with Go `path.Match` against the original backend tool name before the backend ID is added; matching is full-string and case-sensitive.
- Unions and deduplicates scopes from every matching rule, then requires all distinct rule scopes together with the backend-level `required_scopes`.
- Hides unauthorized tools from `tools/list`. A known direct call without the required scopes returns 403 with `WWW-Authenticate: Bearer error="insufficient_scope", resource_metadata="<path-aware metadata URL>", scope="<space-delimited missing scopes>"`.
- Keeps unmatched rules valid and emits one warning per catalog generation; a later catalog refresh can make the same rule match.
- Reloads backend-local `tool_rules` through SIGHUP with the backend policy.

### Configuration example

`config.example.yaml` includes a scoped `admin.*` rule under the existing `primary` backend. The example uses environment placeholders only and does not add credentials or a new backend.

### Compatibility and release status

The v1.0.0 wire, security, and deployment boundaries remain the baseline until this development line is published. The project remains available under the Apache License 2.0; see [SECURITY.md](SECURITY.md), [CONTRIBUTING.md](CONTRIBUTING.md), and the [v1.0.0 release notes](RELEASE_NOTES_v1.0.0.md) for the current published guidance.

## 中文

### 开发范围

- 增加 backend-local `tool_rules`。每条规则包含 `match` glob 和 `required_scopes`。
- 在添加 backend ID 形成公开名称之前，使用 Go `path.Match` 针对原始 backend tool name 匹配；匹配为整串且区分大小写。
- 合并并去重所有匹配规则的 scope，再要求这些不同的规则 scope 与 backend 级 `required_scopes` 一起全部满足。
- 未授权 tool 从 `tools/list` 中隐藏。已知 tool 在缺少所需 scope 时被直接调用，返回 403，并带有 `WWW-Authenticate: Bearer error="insufficient_scope", resource_metadata="<path-aware metadata URL>", scope="<space-delimited missing scopes>"`。
- 规则当前目录 generation 没有匹配项时仍保持有效，并且每个目录 generation 只 warning 一次；后续目录刷新出现匹配 tool 后即可生效。
- backend-local `tool_rules` 支持通过 SIGHUP 随 backend policy 热重载。

### 配置示例

`config.example.yaml` 已在现有 `primary` backend 下加入带 scope 的 `admin.*` 规则。示例只使用环境变量占位符，不增加凭证或新 backend。

### 兼容性与发布状态

在本开发线正式发布前，v1.0.0 的 wire、安全和部署边界仍是基线。项目继续采用 Apache License 2.0；当前发布指导见 [SECURITY.md](SECURITY.md)、[CONTRIBUTING.md](CONTRIBUTING.md) 和 [v1.0.0 发行说明](RELEASE_NOTES_v1.0.0.md)。
