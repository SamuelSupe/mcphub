# MCPHub 用户手册

[English](user-guide.md) · [文档导航](README.zh-CN.md) · [项目首页](../README.zh-CN.md)

面向在 Codex、Claude Code 或其他 MCP 客户端中使用公司工具的用户。本手册对应 v2.2.0。只需在自己的电脑安装 `mcphub-cli`；服务器、身份服务和 Vault 由管理员配置。

首次接入按 **安装 CLI → 运行接入向导 → 确认授权 → 将配置加入客户端** 完成。服务要求个人上游账号时，先在门户连接账号，再完成连接检查。

- [接入前准备](#接入前准备) · [安装 CLI](#安装-cli) · [接入 MCP 客户端](#接入-mcp-客户端)
- [连接个人账号](#连接个人账号) · [写操作审批](#写操作审批) · [管理客户端授权](#管理客户端授权)
- [诊断与常见问题](#诊断与常见问题) · [手动登录与兼容连接](#手动登录与兼容连接) · [本地数据与退出](#本地数据与退出)

## 接入前准备

向管理员获取以下信息：

| 信息 | 示例或用途 |
| --- | --- |
| MCPHub 地址 | `https://hub.example.com/mcp`，包含完整路径 |
| CLI client ID | 例如 `mcphub-cli`，由管理员预先登记 |
| 已开通的服务与工具 | 首次 SSO 登录可能处于待授权状态，需要管理员启用并分配权限 |
| 回调端口（如有要求） | 固定端口示例 `8765`；在 `setup` 或 `login` 后加 `--callback-port 8765` |

下文的域名、工具名和 `ci_example` 均为示例，请使用实际地址及命令返回的客户端 ID。`work` 是本机保存连接信息的 profile 名称，可以自行命名。向导要求服务端已启用客户端授权；旧部署见[兼容连接](#手动登录与兼容连接)。

## 安装 CLI

从 [v2.2.0 Release](https://github.com/SamuelSupe/mcphub/releases/tag/v2.2.0) 选择与电脑匹配的 CLI 包：

| 平台 | CLI 下载 |
| --- | --- |
| macOS Intel | [mcphub-cli](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub-cli_v2.2.0_darwin_amd64.tar.gz) |
| macOS Apple Silicon | [mcphub-cli](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub-cli_v2.2.0_darwin_arm64.tar.gz) |
| Linux amd64 | [mcphub-cli](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub-cli_v2.2.0_linux_amd64.tar.gz) |
| Linux arm64 | [mcphub-cli](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub-cli_v2.2.0_linux_arm64.tar.gz) |
| Windows x64 | [mcphub-cli.exe（ZIP）](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub-cli_v2.2.0_windows_amd64.zip) |
| Windows ARM64 | [mcphub-cli.exe（ZIP）](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub-cli_v2.2.0_windows_arm64.zip) |

下载后与 [SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/SHA256SUMS) 核对 SHA-256，解压并将可执行文件放入 PATH。Windows 需要 10 或更新版本；Intel/AMD 电脑选 x64，Windows on Arm 选 ARM64。

macOS/Linux 安装示例（以下为 Linux arm64，替换文件名；macOS 用 `shasum -a 256` 校验）：

```bash
sha256sum mcphub-cli_v2.2.0_linux_arm64.tar.gz
# 与 SHA256SUMS 中同名条目逐字核对，一致后再解压。
mkdir -p mcphub-cli-release "$HOME/.local/bin"
tar -xzf mcphub-cli_v2.2.0_linux_arm64.tar.gz -C mcphub-cli-release
install -m 755 mcphub-cli-release/mcphub-cli "$HOME/.local/bin/mcphub-cli"
export PATH="$HOME/.local/bin:$PATH"
mcphub-cli setup --help
```

此 PATH 设置用于当前终端。后续可使用 `"$HOME/.local/bin/mcphub-cli"`，或将该目录加入 shell 的 PATH；图形客户端继续使用向导生成的绝对路径。

Windows PowerShell 示例（文件名按实际架构替换）：

```powershell
Get-FileHash .\mcphub-cli_v2.2.0_windows_amd64.zip -Algorithm SHA256
Expand-Archive .\mcphub-cli_v2.2.0_windows_amd64.zip -DestinationPath .\mcphub-cli
```

未添加 PATH 时，将后续命令中的 `mcphub-cli` 替换为 `.\mcphub-cli\mcphub-cli.exe`。

已安装 Go 1.26 的用户也可执行：

```bash
go install github.com/SamuelSupe/mcphub/v2/cmd/mcphub-cli@v2.2.0
```

Go 安装路径是 `go env GOBIN`，为空时是 `$(go env GOPATH)/bin`，也需要加入 PATH。源码构建的文件需用 `./mcphub-cli` 运行，或按上面的 `install` 步骤安装。

从源码构建可用 `go build -trimpath -o ./mcphub-cli ./cmd/mcphub-cli`。安装后确保 MCP 客户端可以找到可执行文件；向导生成的配置使用实际绝对路径。

## 接入 MCP 客户端

在安装 CLI 的同一台电脑运行：

```bash
mcphub-cli setup --server https://hub.example.com/mcp --client-id mcphub-cli --profile work > mcphub-mcp.json
```

已有 `work` 登录配置时，改用：

```bash
mcphub-cli setup --profile work > mcphub-mcp.json
```

按向导完成以下步骤：

1. 在浏览器登录后，回到终端向导选择服务（endpoint）和具体工具。列表仅显示当前身份有资格申请的已发布工具。
2. 设置可选资源限制、授权时长和客户端名称。资源限制对每个选中工具都生效；空选择不会授予全部工具。写入或未分类工具需单独确认，后续操作仍须逐次审批。
3. 在浏览器核对授权范围及终端配对码，再确认授权。向导中的可选目录本身不授予调用权限。

配置可选择通用 `mcpServers` JSON 或 [VS Code 的 `servers` JSON](https://code.visualstudio.com/docs/agent-customization/mcp-servers)。stdout 只输出可执行文件路径、`connect --profile … --client …` 和 `MCPHUB_HOME`，不含凭证；提示和诊断写入 stderr。将条目合并到现有客户端配置，再重启对应 MCP 连接。向导不会覆盖客户端文件。配置须由同一电脑、同一系统账号执行；容器、SSH 或远程开发环境不能直接复用这些本机路径和 Broker。一个条目对应一个 endpoint，多个服务分别运行向导。

完成授权后，向导验证授权状态、MCP 初始化和目录发现，不执行工具。如果最终诊断失败，保留已批准的授权及生成的配置，以失败退出码结束；修复提示的问题后运行 `doctor`。`Ctrl+C` 可取消。授权到期后需 `client authorize` 或重新运行向导；登录 Token 可刷新不代表 Grant 会自动延期。

生成的通用配置形如：

```json
{
  "mcpServers": {
    "work-projects": {
      "command": "/absolute/path/to/mcphub-cli",
      "args": ["connect", "--profile", "work", "--client", "ci_example"]
    }
  }
}
```

保留向导实际生成的路径、客户端 ID，以及存在时的 `env.MCPHUB_HOME`。Windows JSON 路径需转义反斜杠，例如 `C:\\Tools\\mcphub-cli\\mcphub-cli.exe`。使用要求其他配置格式的客户端时，按其格式填写相同的 command、args 和环境变量。

重新加载客户端的 MCP 连接后，应能看到已授权的工具。可再运行 `mcphub-cli doctor --profile work --client ci_example` 检查连接。登录可续期与客户端授权是否到期是两件事。

## 连接个人账号

1. 使用接入向导或 `mcphub-cli login` 登录 MCPHub。
2. 打开 `https://hub.example.com/client-auth/`，在「已连接账号」点击目标服务的「连接账号」。支持浏览器授权的服务会打开上游登录页；其他服务只需填写个人 Token。
3. 返回客户端继续调用。客户端配置、Broker 和 Agent 都不需要上游 Token，也不需要 Vault 路径。

账号可以同时供该用户已授权的多个客户端使用。客户端仍只能访问自己的 ClientGrant 范围；写工具及未分类工具仍须逐次审批。「查看授权详情」显示账号、权限、有效期和能否自动续期。

更换账号或断开连接会使该用户在此 endpoint 的旧 ClientGrant 失效，并取消已接纳的请求和订阅。重新连接后，需要重新进行客户端授权；已完成的操作不会撤销。首次连接账号可以直接满足已有客户端授权。退出网页会话或本机 `logout` 不等于撤销上游账号；请在门户点击「断开连接」。

## 写操作审批

只读工具在权限检查通过后直接执行。写工具和未分类工具需要逐次审批；客户端获得写申请权限，不代表每次操作已被批准。

1. Agent 调用写工具后若收到 `approval_pending`，操作尚未执行；打开返回的 `approval_url`。
2. 由有权审批的人核对目标、资源、完整参数和可用预览，按页面要求完成加强认证并批准或拒绝。你可能需要把链接交给指定审批人；申请人不一定有审批权。
3. 批准后，由原客户端调用 `mcphub_resume_approval` 并传入 `approval_id`。查询使用 `mcphub_approval_status`，执行前取消使用 `mcphub_cancel_approval`。

审批可能需要两位不同人员，且有有效期。结果不确定时先查询状态并联系管理员，不要反复发起同一写操作。撤销授权或取消申请不能撤回上游已经接受的写入。更换客户端授权后，原授权的审批不能转交给新授权继续执行。

## 管理客户端授权

可在 `https://hub.example.com/client-auth/` 查看、拒绝或撤销自己的授权和会话。一个客户端入口对应一个服务（endpoint）；不同 Agent 或服务分别授权，便于独立撤销。

需要明确指定工具和资源时，可手动操作：

```bash
mcphub-cli client add --profile work --name editor-read --endpoint database-prod \
  --scope mcp:database --scope db:read --tool query --resource /project=project-a
```

在浏览器核对范围与配对码。后续操作按需要选择，`ci_example` 替换为实际客户端 ID：

| 操作 | 命令与效果 |
| --- | --- |
| 查看授权 | `mcphub-cli client list --profile work` |
| 重新授权或续期 | `mcphub-cli client authorize --profile work --client ci_example`，重新浏览器确认后重启 MCP 连接 |
| 调整资源范围 | `mcphub-cli client authorize --profile work --client ci_example --resource /project=project-b`，替换原资源列表并重新确认 |
| 撤销一个授权 | `mcphub-cli client revoke --profile work --client ci_example`，阻止该授权的后续调用 |
| 查看 Broker | `mcphub-cli broker status` |
| 停止 Broker | `mcphub-cli broker stop`，停止本地连接，远端授权仍保留 |

`client add` 输出完整 MCP 配置，参数包含 `connect --profile work --client ci_...`，无需 Token。一个入口对应一个 endpoint，多个 endpoint 使用多个入口。`connect` 按需启动共享 Broker，`broker run` 可前台诊断。可用 `MCPHUB_HOME` 指定其他私有目录，相关进程应保持一致。Broker 日志写入该目录的 `broker.log`；stdio 只传输 MCP 消息。

默认只读，并冻结当前符合条件的工具列表。省略 scope 时，从 endpoint 与可用工具所需权限中选择用户 Token 已具备的范围。重复 `--scope`、`--tool`、`--resource /json/pointer=value` 可以明确缩小授权。需要时显式启用 `--prompts`、`--resources`、`--subscriptions`，订阅必须同时允许 resources。工具参数的资源条件不能解释为 URI 或 prompt 限制，这类能力应单独建立入口。`--ttl` 可缩短服务端上限（服务端可配置 1 分钟至 8 小时）。`client authorize` 保留未指定设置，列表参数替换原列表，并重新要求浏览器确认；替换后须重建原有连接。

`--allow-write-requests` 仅允许发起写申请，具体操作仍受服务端审批、多人复核、MFA、资源版本及业务幂等限制。需要预览或状态工具时，一并加入工具允许列表。另一个客户端或替换后的 Grant 不能查询、取消、恢复或取得旧审批结果，也不能借相同业务 ID 重新执行。

`status`、`client list` 分别显示本地登录缓存、在线授权状态和有效 scope。Broker 每秒检查本地变更，每 30 秒以 ETag 查询远端状态；**服务端撤销立即阻止后续接纳，与轮询无关**，并取消相关活动流。已经被上游接受的副作用不能回滚。`logout` 先清除本地秘密、断开该 profile，再尝试撤销远端会话；离线失败仅保留非秘密会话引用并明确提示“撤销未确认”，可到 `/client-auth/` 撤销旧会话。`broker stop` 只停止本地传输，不撤销 Grant。两者均不吊销身份服务 Token 或退出网页会话。

## 诊断与常见问题

在用户电脑上，可针对 MCP 配置中实际使用的 profile 和客户端入口排障：

```bash
mcphub-cli doctor --profile work
mcphub-cli doctor --profile work --client ci_example
mcphub-cli doctor --profile work --client ci_example --json --timeout 30s
```

`doctor` 检查私有凭证目录、登录及续期、客户端配对、在线 Grant 状态与有效 Scope，再完成 MCP 初始化并读取工具目录第一页。Broker 已运行时通过 Broker 检查；未运行时给出提示，使用同一客户端凭证直接检查远端连接。该命令可能刷新 Token，但不会打开登录窗口、启动 Broker、创建授权或执行工具。报告不包含 Token 或 IPC 凭证，提供下一步操作；阻塞性失败退出码为 `1`，成功或仅警告为 `0`。默认超时 15 秒，可调整为 1 秒至 2 分钟。

`doctor --client ci_...` 和接入向导还会检查个人上游账号：未连接、过期、需重连或凭证服务不可用时诊断失败，并给出下一步；这项只读检查不刷新上游凭证、不执行工具，也不能保证上游业务权限。旧服务未提供账号状态时会明确警告。

### 常见问题

| 现象 | 下一步 |
| --- | --- |
| 首次登录提示待授权 | 联系管理员启用用户并配置服务、Scope 和工具权限 |
| 向导没有可选工具或返回 403 | 确认登录身份与服务选择；请管理员检查发布状态和权限，不要自行扩大 Scope |
| 授权到期、撤销或范围变化 | `mcphub-cli client authorize --profile work --client ci_example`，浏览器确认后重启 MCP 连接 |
| 个人账号未连接、过期或需重连 | 到门户连接/重连账号；更换或断开账号后重新授权客户端 |
| 未自动打开浏览器 | 在同一台电脑打开终端给出的 URL；核对管理员要求的回调端口 |
| 401、登录过期且无法刷新 | 重新 `mcphub-cli login --profile work`，随后重启 MCP 连接 |
| 返回 429 | 等待服务额度恢复；不要自动重放写请求 |
| 服务不可用或持续 503 | 保存诊断报告与请求 ID，交给管理员排查网关、身份服务和后端 |

诊断报告不含 Token。联系管理员时提供发生时间、profile、客户端 ID、endpoint、请求 ID 和诊断结果即可，不要发送 `~/.mcphub/` 中的凭证文件。

## 手动登录与兼容连接

需要单独登录或续期时：

```bash
mcphub-cli login --server https://hub.example.com/mcp --client-id mcphub-cli --profile work
mcphub-cli status --profile work
```

`login` 打开系统浏览器，最多等待 5 分钟接收本机回调。打开失败时，会打印可在同一台电脑浏览器中访问的授权链接。它校验 state/issuer，并完成一次已认证的 MCP 握手，成功后才替换原有凭证；失败或取消会保留原有登录。重复传入 `--scope` 可指定申请的权限，未指定时使用认证 challenge 或资源 metadata 的默认值；身份服务声明支持时会追加 `offline_access`。若未签发 refresh token，仍可登录，但会明确提示到期后需要重新登录。

旧部署或管理员明确允许不使用客户端授权的 endpoint，可使用以下 stdio 配置：

```json
{
  "mcpServers": {
    "mcphub": {
      "command": "/absolute/path/to/mcphub-cli",
      "args": ["connect", "--profile", "work"]
    }
  }
}
```

该方式不能访问强制要求客户端授权的服务。此类服务应使用向导生成的 `--client ci_...` 配置。基础 `login` / `connect` 可接入 v1.x；客户端授权、Broker 与向导需要 v2.0.0 或更新版本，个人账号需要 v2.1.0，个人账号诊断需要 v2.2.0。

`connect` 读取 profile，仅向保存的 MCPHub 地址附加 Bearer token，在到期前自动刷新凭证。工具、提示词、资源、分页、进度与订阅均通过连接器转发，公开名称和 URI 保持一致；取消 stdio 调用会关闭对应 HTTP 响应流。stdout 只输出 MCP 消息，诊断写入 stderr。连接器不会弹出浏览器；401 最多刷新重试一次，403 提示缺失的 scope，网络失败不会自动重放操作。

## 本地数据与退出

```bash
mcphub-cli status --profile work
mcphub-cli logout --profile work
```

退出登录、停止 Broker、撤销客户端授权、断开个人账号是不同操作：停止 Broker 只停止本地连接；撤销客户端授权限制该客户端；断开个人账号影响该服务的上游凭证。个人账号请在门户断开，必要时还需在上游服务撤销授权。

默认 profile 为 `default`。macOS/Linux 数据保存在 `~/.mcphub/`，目录权限 `0700`、文件权限 `0600`；Windows 保存在 `%USERPROFILE%\.mcphub\`，通过仅授权当前用户的 DACL 保护。Windows 凭证目录需位于支持 Windows 访问控制的本地文件系统（如 NTFS）。已有 profile 可直接由 `mcphub-cli` 使用，无需迁移。Token 以本地 JSON 保存，**不做加密**；不要放入共享目录或其他用户可读取的备份。临时文件替换与按 profile 的进程间锁保证多个连接器能够安全轮换 refresh token。

无 Broker 的 profile 中，`status` 显示本地缓存状态、到期时间和能否续期，不输出 Token。`logout` 清除本地 Token、保留非敏感连接设置，使连接器后续请求停止并要求登录；已经接受的请求可能继续完成。它不会吊销身份服务中的 Token 或退出浏览器会话。重新登录后，需要重启该 profile 的已有连接器。
