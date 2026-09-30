import { readFileSync, writeFileSync, mkdirSync, cpSync, rmSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { loadAdminContent } from './admin-content.mjs';

const root = path.dirname(fileURLToPath(import.meta.url));
const repository = path.dirname(root);
const userPages = JSON.parse(readFileSync(path.join(root, 'pages.json'), 'utf8'));
const adminPages = JSON.parse(readFileSync(path.join(root, 'admin-pages.json'), 'utf8'));
const pages = [
  ...userPages.map(page => ({ ...page, guide: 'user', guideTitle: '用户指南' })),
  ...adminPages.map(page => ({ ...page, guide: 'admin', guideTitle: '管理员手册' })),
];
const adminContent = loadAdminContent(repository, adminPages);
const output = path.join(root, 'dist');
rmSync(output, { recursive: true, force: true });
mkdirSync(path.join(output, 'admin'), { recursive: true });
cpSync(path.join(root, 'assets'), path.join(output, 'assets'), { recursive: true });

const escape = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;')
  .replaceAll('>', '&gt;').replaceAll('"', '&quot;');
const logo = '<svg viewBox="0 0 28 32" aria-hidden="true"><rect x="2" y="7" width="5" height="21" rx="2.5"/><rect x="11" width="5" height="32" rx="2.5"/><rect x="20" y="3" width="5" height="27" rx="2.5"/></svg>';
const icon = encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="7" fill="#173f38"/><g fill="#ddf4c0"><rect x="6" y="11" width="4" height="15" rx="2"/><rect x="14" y="5" width="4" height="22" rx="2"/><rect x="22" y="8" width="4" height="18" rx="2"/></g></svg>');
const index = [];

for (const page of pages) {
  const body = adminContent.get(page.slug)
    ?? readFileSync(path.join(root, 'content', `${page.slug}.html`), 'utf8');
  const headings = [...body.matchAll(/<h2 id="([^"]+)">([\s\S]*?)<\/h2>/g)];
  const guidePages = pages.filter(item => item.guide === page.guide);
  const position = guidePages.indexOf(page);
  const prefix = page.guide === 'admin' ? '../' : '';
  const home = page.guide === 'admin' ? 'admin/index.html' : 'index.html';
  const link = file => prefix + file;
  const nav = [...new Set(guidePages.map(item => item.group))].map(group => `
    <div class="nav-group"><p>${escape(group)}</p>
      ${guidePages.filter(item => item.group === group).map(item => `<a href="${link(item.slug + '.html')}"${item.slug === page.slug ? ' aria-current="page"' : ''}>${escape(item.title)}</a>`).join('\n')}
    </div>`).join('\n');
  const adjacent = (item, label) => item
    ? `<a href="${link(item.slug + '.html')}"><span>${label}</span><strong>${escape(item.title)} ${label === '下一篇' ? '→' : ''}</strong></a>`
    : '<div></div>';
  const starter = page.guide === 'admin'
    ? '<div class="toc-bottom">部署从这里开始<a href="install.html">安装与首次启动 →</a></div>'
    : '<div class="toc-bottom">新用户从这里开始<a href="quickstart.html">第一次接入 →</a></div>';
  const footer = page.guide === 'admin'
    ? '需要核对字段？<a href="configuration.html">查看 YAML 与协议参考</a>'
    : '遇到接入问题？<a href="support.html">整理诊断信息并联系管理员</a>';
  const sourceLink = page.sources
    ? `<a class="article-source" href="https://github.com/SamuelSupe/mcphub/blob/main/${page.sources[0].file}">查看原始文档 ↗</a>` : '';

  writeFileSync(path.join(output, `${page.slug}.html`), `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>${escape(page.title)} · MCPHub 帮助中心</title>
  <meta name="description" content="${escape(page.description)}">
  <meta name="theme-color" content="#173f38">
  <link rel="icon" type="image/svg+xml" href="data:image/svg+xml,${icon}">
  <link rel="stylesheet" href="${link('assets/styles.css')}">
  <script defer src="${link('assets/app.js')}"></script>
</head>
<body data-root="${prefix}">
<a class="skip" href="#main">跳到正文</a>
<header class="header">
  <a class="brand" href="${link('index.html')}">${logo}<strong>MCPHub</strong><span>帮助中心</span></a>
  <div class="header-actions">
    <button class="search-trigger" aria-haspopup="dialog"><span>搜索文档</span><kbd>⌘ K</kbd></button>
    <a class="source-link" href="https://github.com/SamuelSupe/mcphub" target="_blank" rel="noopener">项目首页 ↗</a>
    <button id="menu-toggle" aria-expanded="false" aria-controls="sidebar">目录</button>
  </div>
</header>
<div class="layout">
  <aside id="sidebar" class="sidebar">
    <nav class="guide-switch" aria-label="选择手册">
      <a href="${link('index.html')}"${page.guide === 'user' ? ' aria-current="true"' : ''}>用户指南</a>
      <a href="${link('admin/index.html')}"${page.guide === 'admin' ? ' aria-current="true"' : ''}>管理员手册</a>
    </nav>
    <div class="edition">${page.guideTitle} <span>v2.2.0</span></div>
    <nav aria-label="文档章节">${nav}</nav>
    <div class="sidebar-note">给每一次工具调用<br>清晰的权限边界。</div>
  </aside>
  <main id="main" class="main" tabindex="-1">
    <div class="breadcrumbs"><a href="${link(home)}">${page.guideTitle}</a><span>/</span><span>${escape(page.group)}</span></div>
    <article>
      <p class="eyebrow">${escape(page.group)}</p>
      <h1>${escape(page.title)}</h1>
      <p class="lead">${escape(page.description)}</p>
      <div class="article-body">${body.replaceAll('<table>', '<div class="table-wrap"><table>').replaceAll('</table>', '</table></div>')}</div>
    </article>
    <div class="article-meta">适用于 MCPHub v2.2.0 · 更新于 2026 年 9 月 30 日 ${sourceLink}</div>
    <nav class="pagination" aria-label="相邻章节">${adjacent(guidePages[position - 1], '上一篇')}${adjacent(guidePages[position + 1], '下一篇')}</nav>
    <footer>${footer}<span> MCPHub 帮助中心</span></footer>
  </main>
  <aside class="toc">
    <p>本页内容</p>
    <nav aria-label="页内目录">${headings.map(([, id, text]) => `<a href="#${id}">${text}</a>`).join('\n')}</nav>
    ${starter}
  </aside>
</div>
<dialog id="search-dialog" aria-labelledby="search-title">
  <div class="search-head"><h2 id="search-title">搜索帮助文档</h2><button id="search-close" aria-label="关闭搜索">关闭 <kbd>Esc</kbd></button></div>
  <label class="search-label" for="search-input">输入问题、操作或错误信息</label>
  <input id="search-input" type="search" placeholder="例如：授权到期、401、连接账号" autocomplete="off">
  <p id="search-status" role="status" aria-live="polite"></p>
  <div id="search-results"></div>
</dialog>
</body>
</html>`);
  index.push({
    slug: page.slug, title: page.title, group: page.group, guideTitle: page.guideTitle,
    description: page.description,
    headings: headings.map(([, id, title]) => ({ id, title: title.replace(/<[^>]*>/g, '') })),
    text: body.replace(/<[^>]*>/g, ' ').replace(/&[a-z#0-9]+;/gi, ' ').replace(/\s+/g, ' ').trim(),
  });
}

writeFileSync(path.join(output, 'search-index.json'), JSON.stringify(index));
const siteUrl = 'https://samuelsupe.github.io/mcphub/';
writeFileSync(path.join(output, '404.html'), `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>页面未找到 · MCPHub</title><link rel="stylesheet" href="${siteUrl}assets/styles.css"><main class="not-found"><p class="eyebrow">404</p><h1>这篇文档不存在</h1><p>链接可能有误，请回到帮助中心选择章节。</p><a href="${siteUrl}">返回帮助中心 →</a></main></html>`);

let checkedLinks = 0;
for (const page of pages) {
  const html = readFileSync(path.join(output, `${page.slug}.html`), 'utf8');
  const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map(match => match[1]);
  if (new Set(ids).size !== ids.length) throw new Error(`Duplicate anchor in ${page.slug}`);
  for (const [, target] of html.matchAll(/\b(?:href|src)="([^"]+)"/g)) {
    if (/^(?:https?:|data:|mailto:)/.test(target)) continue;
    const [file, fragment] = target.split('#');
    const destination = path.resolve(output, path.dirname(page.slug), file || `${path.basename(page.slug)}.html`);
    if (!destination.startsWith(output + path.sep) || !existsSync(destination)) throw new Error(`Broken link in ${page.slug}: ${target}`);
    if (fragment && !readFileSync(destination, 'utf8').includes(`id="${decodeURIComponent(fragment)}"`)) throw new Error(`Missing anchor in ${page.slug}: ${target}`);
    checkedLinks++;
  }
}
console.log(`Built ${userPages.length} user pages and ${adminPages.length} administrator pages; validated ${checkedLinks} local links and assets.`);
