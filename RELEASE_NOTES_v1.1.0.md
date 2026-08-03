# MCPHub v1.1.0

MCPHub v1.1.0 adds backend-local tool scope policies while preserving the v1.0.0 protocol and deployment boundaries. The previous v1.0.0 release remains available as historical reference.

Repository: [SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · module: `github.com/SamuelSupe/mcphub` · license: [Apache License 2.0](https://github.com/SamuelSupe/mcphub/blob/v1.1.0/LICENSE)

## English

### Highlights

- Adds backend-local `tool_rules`. Each rule has a `match` glob and `required_scopes`.
- Evaluates `match` with Go `path.Match` against the original backend tool name before the backend ID is added; matching is full-string and case-sensitive.
- Expands existing `${ENV}` placeholders in `match` and rule scope strings. Validation requires every `match` to be non-empty and a valid Go `path.Match` pattern, every rule to declare non-empty `required_scopes` entries with no whitespace or duplicates, and rejects duplicate `match` entries within one backend.
- Unions and deduplicates scopes from every matching rule, then requires all distinct rule scopes together with the backend-level `required_scopes`.
- Hides unauthorized tools from `tools/list`. A known direct call without the required scopes returns 403 with `WWW-Authenticate: Bearer error="insufficient_scope", resource_metadata="<path-aware metadata URL>", scope="<space-delimited missing scopes>"`.
- Keeps unmatched rules valid and emits one warning per catalog generation; a later catalog refresh can make the same rule match.
- Reloads backend-local `tool_rules` through SIGHUP with the backend policy. Rule changes do not change catalog-source identity, so they remain compatible with the existing optional-backend catalog reuse boundary.
- Carries forward the v1.0.0 MCP aggregation, OIDC/JWKS, resource/subscription, streaming, and deployment boundaries; see the [v1.0.0 historical release notes](https://github.com/SamuelSupe/mcphub/blob/v1.0.0/RELEASE_NOTES_v1.0.0.md).

### Release assets

The tag-triggered GitHub Actions workflow [`.github/workflows/release.yml`](https://github.com/SamuelSupe/mcphub/blob/v1.1.0/.github/workflows/release.yml) builds and publishes the following archives and checksum file. Each archive contains the binary, example configuration, license, and English/Chinese READMEs. Verify every downloaded archive against `SHA256SUMS`.

| Platform | Download |
| --- | --- |
| macOS amd64 | [mcphub_v1.1.0_darwin_amd64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/mcphub_v1.1.0_darwin_amd64.tar.gz) |
| macOS arm64 | [mcphub_v1.1.0_darwin_arm64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/mcphub_v1.1.0_darwin_arm64.tar.gz) |
| Linux amd64 | [mcphub_v1.1.0_linux_amd64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/mcphub_v1.1.0_linux_amd64.tar.gz) |
| Linux arm64 | [mcphub_v1.1.0_linux_arm64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/mcphub_v1.1.0_linux_arm64.tar.gz) |
| Checksums | [SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/SHA256SUMS) |

## 中文

MCPHub v1.1.0 在保持 v1.0.0 协议与部署边界的同时，增加了后端本地的工具级 scope 策略。上一版 v1.0.0 仍作为历史版本保留。

仓库：[SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · module：`github.com/SamuelSupe/mcphub` · 许可证：[Apache License 2.0](https://github.com/SamuelSupe/mcphub/blob/v1.1.0/LICENSE)

### 主要变化

- 增加 backend-local `tool_rules`。每条规则包含 `match` glob 和 `required_scopes`。
- 在添加 backend ID 形成公开名称之前，使用 Go `path.Match` 针对原始 backend tool name 匹配；匹配为整串且区分大小写。
- `match` 和规则 scope 字符串支持现有 `${ENV}` 展开。配置校验要求每个 `match` 非空且为有效的 Go `path.Match` 模式，每条规则声明非空的 `required_scopes`，其项不能含空白或重复，并拒绝同一 backend 内重复的 `match`。
- 合并并去重所有匹配规则的 scope，再要求这些不同的规则 scope 与 backend 级 `required_scopes` 一起全部满足。
- 未授权 tool 从 `tools/list` 中隐藏。已知 tool 在缺少所需 scope 时被直接调用，返回 403，并带有 `WWW-Authenticate: Bearer error="insufficient_scope", resource_metadata="<path-aware metadata URL>", scope="<space-delimited missing scopes>"`。
- 规则当前目录 generation 没有匹配项时仍保持有效，并且每个目录 generation 只 warning 一次；后续目录刷新出现匹配 tool 后即可生效。
- backend-local `tool_rules` 支持通过 SIGHUP 随 backend policy 热重载。规则变化不改变目录来源身份，因此仍符合现有 optional backend 目录复用边界。
- 延续 v1.0.0 的 MCP 聚合、OIDC/JWKS、资源/订阅、streaming 和部署边界；详见 [v1.0.0 历史发行说明](https://github.com/SamuelSupe/mcphub/blob/v1.0.0/RELEASE_NOTES_v1.0.0.md)。

### 发布资产

由 tag 触发的 GitHub Actions workflow [`.github/workflows/release.yml`](https://github.com/SamuelSupe/mcphub/blob/v1.1.0/.github/workflows/release.yml) 构建并发布以下归档和校验文件。每个归档包含二进制、示例配置、许可证及中英文 README；下载后请使用 `SHA256SUMS` 校验。

| 平台 | 下载 |
| --- | --- |
| macOS amd64 | [mcphub_v1.1.0_darwin_amd64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/mcphub_v1.1.0_darwin_amd64.tar.gz) |
| macOS arm64 | [mcphub_v1.1.0_darwin_arm64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/mcphub_v1.1.0_darwin_arm64.tar.gz) |
| Linux amd64 | [mcphub_v1.1.0_linux_amd64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/mcphub_v1.1.0_linux_amd64.tar.gz) |
| Linux arm64 | [mcphub_v1.1.0_linux_arm64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/mcphub_v1.1.0_linux_arm64.tar.gz) |
| 校验和 | [SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v1.1.0/SHA256SUMS) |
