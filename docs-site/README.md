# MCPHub 帮助中心

中英文用户指南与管理员手册，以 MCPHub v2.3.0 候选版本为基准，尚未公开发布。每种语言包含 19 篇用户指南和 22 篇管理员章节，共 82 页，覆盖内建账号、组权限与可选企业 SSO。静态多页站点，浏览器端无框架或第三方依赖；构建使用 Node.js 20 以上与 Marked。

- `pages.json`、`pages.en.json` 与 `content/*.html`、`content/en/*.html`：用户指南的顺序、导航和两种语言的正文。
- `admin-pages.json`、`admin-pages.en.json`：管理员章节，按对应语言的源 Markdown 文件和标题组织。
- `content/admin/index.html`、`content/en/admin/index.html`：管理员入口页。
- `admin-content.mjs`：读取原始手册、生成标题锚点，将已收录章节的链接转换为同语言站内链接。
- `i18n.mjs`：页面模板中的中英文导航、提示和语言路由。
- `assets/`：共用样式、按语言搜索、移动目录和代码复制。
- `build.mjs`：生成独立 HTML、两份搜索索引，检查翻译章节对应关系、站内链接、资源和锚点。
- `dist/`：生成的发布目录，不纳入版本控制。

## 构建与预览

在此目录执行：

```bash
npm ci --ignore-scripts
npm run build
python3 -m http.server 4387 --bind 127.0.0.1 --directory dist
```

打开 `http://127.0.0.1:4387/` 查看中文用户指南，`/admin/` 查看中文管理员手册；英文对应 `/en/` 和 `/en/admin/`。所有章节支持直接访问、刷新和分享，导航与语言切换无需 JavaScript。

## 修改文档

用户正文通过 HTML 维护，代码块转义 `<`、`>`、`&`；二级标题带唯一 `id`。新增章节同时加入两个 `pages` 清单，并补齐两种语言的正文。相同章节使用相同 slug 和标题 ID，内部链接使用 `章节.html#标题id`。

管理员章节直接读取对应语言的 `docs/admin-guide.md`、`deploy/README.md`、SSO、Vault、配置参考等材料；中文源文件带 `.zh-CN` 后缀。备份与恢复页读取对应语言的管理员手册。修改原始 Markdown 即可同步网页，不另存一套正文。`admin-pages` 清单的 `sources` 指定源文件与标题；省略标题读取整篇，`children: false` 只读取当前小节，不包含下级章节。移动或改名源标题时同步更新清单。新增任务页可组合多处小节，正文中的交叉引用会自动转换；未收录的参考材料仍链接到 GitHub。

中文地址保持原路径，英文增加 `/en/` 前缀。顶部语言链接跳到另一语言的同一章节；侧栏切换手册时保留语言。搜索只使用当前语言的索引，覆盖用户指南与管理员手册并显示所属指南。修改后，应在 Chrome 检查两种语言的章节切换、管理员子目录搜索、跨手册链接和移动目录。

## 发布

[GitHub Pages](https://samuelsupe.github.io/mcphub/) 使用 `.github/workflows/docs.yml`。`main` 和 PR 变更只构建检查；正式 Release 发布后自动部署，也可手动触发。发行包先以草稿形式验收，确认后公开并同步站点。只上传 `dist`，包括 `examples/` 中未经填写的公开配置模板，不包含真实配置、凭证、数据库或服务端源码。构建直接复制仓库模板，中英文管理员页面的配置链接指向同一份文件；发布站点时同步模板。CI 使用实际服务端校验全部安装配置，并检查 Compose 展开，防止字段、变量和二进制再次脱节。

用户内容依据本仓库 CLI、个人门户、用户手册和权限实现；客户端配置核对 OpenAI、Claude Code 与 VS Code 官方文档。管理员内容保持原始手册的部署、审批、身份和运维边界。候选安装页使用 v2.3.0 包名，并明确尚无公开下载；发布流程校验包内配置与文档并实际启动解压后的程序。实际企业 SSO、Vault 和审批仍须在目标部署环境完成验收。
