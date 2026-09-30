# MCPHub 帮助中心

中文用户指南与管理员手册，以 MCPHub v2.2.0 为基准。静态多页站点，浏览器端无框架或第三方依赖；构建使用 Node.js 20 以上与 Marked。

- `pages.json` 与 `content/*.html`：19 篇用户指南的顺序、导航和正文。
- `admin-pages.json`：20 篇管理员章节，按源 Markdown 文件和标题组织。
- `content/admin/index.html`：管理员入口页。
- `admin-content.mjs`：读取原始手册、生成标题锚点，将已收录章节的链接转换为站内链接。
- `assets/`：共用样式、全文搜索、移动目录和代码复制。
- `build.mjs`：生成独立 HTML、全站搜索索引，并检查站内链接、资源和锚点。
- `dist/`：生成的发布目录，不纳入版本控制。

## 构建与预览

在此目录执行：

```bash
npm ci --ignore-scripts
npm run build
python3 -m http.server 4387 --bind 127.0.0.1 --directory dist
```

打开 `http://127.0.0.1:4387/` 查看用户指南，打开 `/admin/` 查看管理员手册。所有章节支持直接访问、刷新和分享，导航无需 JavaScript。

## 修改文档

用户正文通过 HTML 维护，代码块转义 `<`、`>`、`&`；二级标题带唯一 `id`。新增章节加入 `pages.json`，内部链接使用 `章节.html#标题id`。

管理员章节直接读取 `docs/admin-guide.zh-CN.md`、`deploy/README.zh-CN.md`、SSO、Vault、配置参考及升级说明。修改这些原始 Markdown 即可同步网页，不另存一套正文。`admin-pages.json` 的 `sources` 指定源文件与标题；省略标题读取整篇，`children: false` 只读取当前小节，不包含下级章节。移动或改名源标题时同步更新清单。新增任务页可组合多处小节，正文中的交叉引用会自动转换；未收录的参考材料仍链接到 GitHub。

用户指南与管理员手册通过侧栏切换，搜索覆盖两种手册并显示所属指南。修改搜索或导航后，应在 Chrome 检查管理员子目录中的搜索结果、跨手册链接及移动目录。

## 发布

[GitHub Pages](https://samuelsupe.github.io/mcphub/) 使用 `.github/workflows/docs.yml`。`main` 上的站点、源 Markdown 或升级说明变更会自动构建和发布，也可手动触发。只上传 `dist`，不包含 MCPHub 配置、凭证、数据库或服务端源码。

用户内容依据本仓库 CLI、个人门户、用户手册和权限实现；客户端配置核对 OpenAI、Claude Code 与 VS Code 官方文档。管理员内容保持原始手册的部署、审批、身份和运维边界。下载名已与 v2.2.0 Release 资源核对；实际企业 SSO、Vault 和审批须在部署环境完成验收。
