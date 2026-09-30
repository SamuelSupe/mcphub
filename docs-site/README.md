# MCPHub 最终用户帮助中心

中文用户文档，以 MCPHub v2.2.0 为基准。静态多页站点，无框架或第三方运行依赖。

- pages.json：章节顺序、导航分组、标题和摘要。
- content/*.html：可编辑章节正文；二级标题带唯一 id。
- assets/：样式及搜索、移动目录、代码复制。
- build.mjs：Node 内置库生成独立 HTML 和本地搜索索引。
- dist/：自动生成的发布目录，不纳入版本控制。

构建：node build.mjs。预览：python3 -m http.server 4387 --bind 127.0.0.1 --directory dist。

正文通过 HTML 维护，代码块转义 <、>、&。内部链接使用 章节.html#标题id，新章节加入 pages.json。搜索索引自动生成。所有页面直接访问、刷新和分享，导航无需 JavaScript。

发布使用 .github/workflows/docs.yml，main 上的文档变更自动部署 GitHub Pages，也可手动触发。只上传 dist，不包含 MCPHub 配置、凭证或服务端源码。

内容依据：本仓库 CLI、个人授权门户、用户手册和权限实现。客户端配置另核对 OpenAI 官方 Codex Manual、Claude Code 与 VS Code 官方 MCP 文档，对应页面含来源。下载名已与 v2.2.0 Release 资源核对。
