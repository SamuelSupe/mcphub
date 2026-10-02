# MCPHub v2.2.2 — MCPBridge client / MCPBridge 客户端

The local client is now **MCPBridge**, executable **`mcpbridge`**. The server remains **`mcphub`**. Distinct archive names, command output and bilingual installation guides make it clear which program to download.

本地客户端改名为 **MCPBridge**，可执行文件名为 **`mcpbridge`**；服务端继续叫 **`mcphub`**。下载包名称、命令输出和中英文安装指南同时更新，明确区分两种程序。

| Role / 用途 | Executable / 程序 | Download / 下载包 | Commands / 命令 |
| --- | --- | --- | --- |
| Gateway server / 网关服务端 | `mcphub` | `mcphub_v2.2.2_<OS>_<arch>.tar.gz` | `serve`, `validate`, `verify-audit` |
| Local client / Agent 客户端 | `mcpbridge` / `mcpbridge.exe` | `mcpbridge_v2.2.2_<OS>_<arch>.tar.gz` or Windows `.zip` | `setup`, `login`, `connect`, `doctor`, `client`, `broker`, `admin` |

## Changes / 变化

- Rename the client command, executable, Go entry point and release archives from `mcphub-cli` to `mcpbridge`. Source installation is now `go install github.com/SamuelSupe/mcphub/v2/cmd/mcpbridge@v2.2.2`.
- Generated Agent configuration, portal guidance, diagnostics and current documentation all use `mcpbridge`.
- Both programs support `--version` / `version`, printing name, version and role: `mcphub 2.2.2 (server)` or `mcpbridge 2.2.2 (client)`. Running a server command in MCPBridge points to the server executable.
- Release checks verify the compiled Go entry point inside every archive, exercise the packaged client and retain packaged-server configuration/startup checks.
- Preserve credential storage (`~/.mcphub`, `%USERPROFILE%\.mcphub`, `MCPHUB_HOME`), profiles, client grants, Broker IPC format and registered OAuth client IDs. Server configuration and schema **9** remain compatible with v2.2.0/v2.2.1.

- 客户端命令、可执行文件、Go 入口和发布包由 `mcphub-cli` 改为 `mcpbridge`，源码安装使用 `go install github.com/SamuelSupe/mcphub/v2/cmd/mcpbridge@v2.2.2`。
- Agent 配置生成、门户提示、诊断信息和当前文档统一使用 `mcpbridge`。
- 两个程序支持 `--version` / `version`，明确显示名称、版本和身份：`mcphub 2.2.2 (server)` 或 `mcpbridge 2.2.2 (client)`。在客户端运行服务端命令时，提示使用服务端程序。
- 发布检查逐包校验实际编译入口，运行解压后的客户端，保留服务端配置校验与启动检查。
- 保留凭证目录（`~/.mcphub`、`%USERPROFILE%\.mcphub`、`MCPHUB_HOME`）、profile、客户端授权、Broker IPC 格式及已注册 OAuth Client ID；服务端配置和 **schema 9** 与 v2.2.0/v2.2.1 兼容。

## Upgrade and rollback / 升级与回滚

1. Quit your Agent. If an old Broker is running, stop it with `mcphub-cli broker stop` before replacing the connector.
2. Download and verify the **MCPBridge client package**. Install it as `mcpbridge` (`mcpbridge.exe` on Windows), then run `mcpbridge --version` and `mcpbridge setup --help`.
3. Keep existing credential directories, `MCPHUB_HOME`, profiles and OAuth client IDs. Update the Agent's executable path from `mcphub-cli` to `mcpbridge`, or regenerate configuration with `mcpbridge setup --profile work`. Existing saved authorization works; normal expiry/revocation rules still apply.
4. Administrators upgrading the gateway use the **mcphub server package**. Back up the database, matching encryption key and configuration; retain one active instance. Upgrading from v2.2.0/v2.2.1 adds no schema migration. Earlier versions follow the [schema 9 upgrade procedure](RELEASE_NOTES_v2.2.1.md#upgrade-and-rollback--升级与回滚).
5. To roll back the client, stop its Broker, reinstall the old client and restore its executable path in the Agent configuration. Preserve credentials and grants. The v2.2.1 release remains available under its original names.

1. 退出 Agent，若旧 Broker 正在运行，先用 `mcphub-cli broker stop` 停止。
2. 下载并校验 **MCPBridge 客户端包**，安装为 `mcpbridge`（Windows 为 `mcpbridge.exe`），执行 `mcpbridge --version` 和 `mcpbridge setup --help`。
3. 保留凭证目录、`MCPHUB_HOME`、profile 和 OAuth Client ID。把 Agent 程序路径从 `mcphub-cli` 更新为 `mcpbridge`，或执行 `mcpbridge setup --profile work` 重新生成配置。已有授权可继续使用，正常到期/撤销规则仍生效。
4. 管理员升级网关选择 **mcphub 服务端包**。备份数据库、匹配的加密密钥与配置，保持一个活跃实例。从 v2.2.0/v2.2.1 升级不新增 schema 迁移，更早版本按 [schema 9 升级流程](RELEASE_NOTES_v2.2.1.md#upgrade-and-rollback--升级与回滚)操作。
5. 回滚客户端时停止 Broker，重装旧客户端并恢复 Agent 中旧的程序路径，保留凭证和授权；v2.2.1 继续提供原名称的文件。

## Validation / 验证

Release gates cover Linux race/vet, native Windows x64/ARM64 client tests, browser JavaScript syntax, container/cross-platform builds, archive identity/content/checksum verification and packaged-client/server smoke checks. Local backend checks run in OrbStack. Existing OAuth client-ID fixtures remain unchanged to verify compatibility. Real enterprise identity/Feishu tenants require deployment-specific acceptance.

发布检查覆盖 Linux race/vet、Windows x64/ARM64 原生客户端测试、浏览器脚本语法、容器/跨平台构建、压缩包身份/内容/校验和及包内客户端/服务端运行检查。本地后端验证使用 OrbStack，OAuth Client ID 测试夹具保持原值以验证兼容性；真实企业身份源/飞书租户仍需部署环境验收。

[中文客户端安装](https://samuelsupe.github.io/mcphub/install.html) · [English client installation](https://samuelsupe.github.io/mcphub/en/install.html) · [中文服务端安装](https://samuelsupe.github.io/mcphub/admin/install.html) · [English server installation](https://samuelsupe.github.io/mcphub/en/admin/install.html)
