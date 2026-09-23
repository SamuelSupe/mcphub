# MCPHub v1.3.1

Windows CLI support for the browser-login and local stdio connector introduced in v1.3.0.

[English](#english) | [中文](#中文) · [Changes since v1.3.0](https://github.com/SamuelSupe/mcphub/compare/v1.3.0...v1.3.1)

## English

- Adds `mcphub-cli.exe` downloads for Windows x64 (`amd64`) and ARM64 as ZIP archives. Each includes the CLI, license, and English/Chinese READMEs; `SHA256SUMS` covers both new ZIPs and the existing macOS/Linux archives.
- Opens authorization URLs in the default Windows browser through the Windows shell API. Login retains the same public OAuth client, PKCE, loopback callback, and gateway verification flow. If browser launch fails, the CLI prints a URL to open on the same computer.
- Stores Windows profiles in `%USERPROFILE%\.mcphub\` with a protected DACL for the current user. Credential files are checked for ownership, access rules, and reparse points. Tokens remain unencrypted local JSON.
- Uses Windows file locking to serialize profile updates across connectors and same-directory temporary-file replacement when saving refreshed credentials. macOS/Linux retain their existing Unix permission and locking behavior.
- Adds native Windows x64 and ARM64 CI/release gates for the CLI integration tests, credential-access regressions, process locking, and executable smoke checks. Interactive browser authorization is not automated by these gates.

### Install and upgrade

Choose the ZIP matching your Windows architecture, verify its hash against `SHA256SUMS`, extract it, and put `mcphub-cli.exe` on your PATH or configure its absolute path in your MCP client. The CLI follows [Go's Windows requirements](https://go.dev/wiki/MinimumRequirements#windows), with Windows 10 or later required.

```powershell
.\mcphub-cli.exe login --server https://hub.example.com/mcp --client-id mcphub-cli --profile work
.\mcphub-cli.exe status --profile work
```

Register the public OAuth client at your issuer first, as described in the [Windows CLI guide](https://github.com/SamuelSupe/mcphub/blob/v1.3.1/README.md#windows-cli-quick-start). Windows CLI clients work with existing v1.3.0 gateways; no server upgrade or configuration/database migration is needed for Windows client access. Server binaries remain available for macOS and Linux. Preserve existing profiles, databases, and encryption keys when replacing any installed binaries.

## 中文

- 新增 Windows x64（`amd64`）和 ARM64 的 `mcphub-cli.exe` ZIP 下载。每个包包含 CLI、许可证与中英文 README；`SHA256SUMS` 同时覆盖新增 ZIP 和已有 macOS/Linux 归档。
- 通过 Windows shell API 在默认浏览器打开授权地址，沿用公开 OAuth 客户端、PKCE、回环回调和网关校验流程。无法自动启动浏览器时，会输出可在同一电脑打开的 URL。
- Windows profile 保存在 `%USERPROFILE%\.mcphub\`，使用只授权当前用户的受保护 DACL；读取凭证时检查所有权、访问规则与 reparse point。Token 仍为未加密的本地 JSON。
- 使用 Windows 文件锁串行化多个连接器的 profile 更新，并通过同目录临时文件替换保存刷新后的凭证。macOS/Linux 保留原有 Unix 权限与锁行为。
- CI 和发布流程新增原生 Windows x64、ARM64 验证，覆盖 CLI 集成流程、凭证访问回归、进程间锁与可执行程序冒烟检查；这些检查不自动执行人工浏览器授权操作。

### 安装与升级

按 Windows 架构选择 ZIP，与 `SHA256SUMS` 核对哈希后解压，将 `mcphub-cli.exe` 放入 PATH，或在 MCP 客户端中配置绝对路径。CLI 遵循 [Go 的 Windows 要求](https://go.dev/wiki/MinimumRequirements#windows)，需要 Windows 10 及以上。

```powershell
.\mcphub-cli.exe login --server https://hub.example.com/mcp --client-id mcphub-cli --profile work
.\mcphub-cli.exe status --profile work
```

先按 [Windows CLI 指南](https://github.com/SamuelSupe/mcphub/blob/v1.3.1/README.zh-CN.md#windows-cli-快速开始)在身份服务注册公开 OAuth 客户端。Windows CLI 可连接已有 v1.3.0 网关，无需为此升级服务端、迁移配置或数据库。服务端继续提供 macOS、Linux 下载；替换已有二进制时保留原有 profile、数据库和加密密钥。

## Windows downloads / Windows 下载

| Architecture / 架构 | ZIP |
| --- | --- |
| x64 / Intel / AMD | [mcphub-cli_v1.3.1_windows_amd64.zip](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.1/mcphub-cli_v1.3.1_windows_amd64.zip) |
| ARM64 | [mcphub-cli_v1.3.1_windows_arm64.zip](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.1/mcphub-cli_v1.3.1_windows_arm64.zip) |

[SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v1.3.1/SHA256SUMS) · [All platform downloads / 全部平台下载](https://github.com/SamuelSupe/mcphub/releases/tag/v1.3.1) · [v1.3.0 features / v1.3.0 功能](https://github.com/SamuelSupe/mcphub/blob/v1.3.0/RELEASE_NOTES_v1.3.0.md)
