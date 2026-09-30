import { readFileSync, writeFileSync, mkdirSync, cpSync, rmSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { loadAdminContent } from './admin-content.mjs';
import { locales } from './i18n.mjs';

const root = path.dirname(fileURLToPath(import.meta.url));
const repository = path.dirname(root);
const siteUrl = 'https://samuelsupe.github.io/mcphub/';
const pages = [];
const adminContent = new Map();
for (const [language, locale] of Object.entries(locales)) {
  const userPages = JSON.parse(readFileSync(path.join(root, 'pages' + locale.suffix + '.json'), 'utf8'));
  const adminPages = JSON.parse(readFileSync(path.join(root, 'admin-pages' + locale.suffix + '.json'), 'utf8'));
  adminContent.set(language, loadAdminContent(repository, adminPages, language));
  for (const [guide, guidePages] of [['user', userPages], ['admin', adminPages]]) {
    pages.push(...guidePages.map(page => ({
      ...page, guide, language, locale, guideTitle: locale[guide], file: locale.prefix + page.slug + '.html',
    })));
  }
}
for (const guide of ['user', 'admin']) {
  const slugs = language => pages.filter(page => page.guide === guide && page.language === language).map(page => page.slug);
  if (JSON.stringify(slugs('zh-CN')) !== JSON.stringify(slugs('en'))) throw new Error('Missing or reordered translation: ' + guide);
}
const output = path.join(root, 'dist');
rmSync(output, { recursive: true, force: true });
for (const page of pages) mkdirSync(path.dirname(path.join(output, page.file)), { recursive: true });
cpSync(path.join(root, 'assets'), path.join(output, 'assets'), { recursive: true });
for (const file of [
  'config.example.yaml', 'deploy/config.local.yaml', 'deploy/config.remote-sqlite.yaml',
  'deploy/config.remote-postgres.yaml', 'deploy/config.feishu-vault.example.yaml',
]) {
  const destination = path.join(output, 'examples', file);
  mkdirSync(path.dirname(destination), { recursive: true });
  cpSync(path.join(repository, file), destination);
}

const escape = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;')
  .replaceAll('>', '&gt;').replaceAll('"', '&quot;');
const logo = '<svg viewBox="0 0 28 32" aria-hidden="true"><rect x="2" y="7" width="5" height="21" rx="2.5"/><rect x="11" width="5" height="32" rx="2.5"/><rect x="20" y="3" width="5" height="27" rx="2.5"/></svg>';
const icon = encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="7" fill="#173f38"/><g fill="#ddf4c0"><rect x="6" y="11" width="4" height="15" rx="2"/><rect x="14" y="5" width="4" height="22" rx="2"/><rect x="22" y="8" width="4" height="18" rx="2"/></g></svg>');
const indexes = { 'zh-CN': [], en: [] };

for (const page of pages) {
  const locale = page.locale;
  const body = adminContent.get(page.language).get(page.slug)
    ?? readFileSync(path.join(root, 'content', page.file), 'utf8');
  const headings = [...body.matchAll(/<h2 id="([^"]+)">([\s\S]*?)<\/h2>/g)];
  const guidePages = pages.filter(item => item.guide === page.guide && item.language === page.language);
  const position = guidePages.indexOf(page);
  const languageRoot = page.guide === 'admin' ? '../' : '';
  const home = page.guide === 'admin' ? 'admin/index.html' : 'index.html';
  const siteLink = file => path.posix.relative(path.posix.dirname(page.file), file);
  const link = file => siteLink(locale.prefix + file);
  const counterpart = locales[locale.otherLanguage].prefix + page.slug + '.html';
  const nav = [...new Set(guidePages.map(item => item.group))].map(group => `
    <div class="nav-group"><p>${escape(group)}</p>
      ${guidePages.filter(item => item.group === group).map(item => `<a href="${link(item.slug + '.html')}"${item.slug === page.slug ? ' aria-current="page"' : ''}>${escape(item.title)}</a>`).join('\n')}
    </div>`).join('\n');
  const adjacent = (item, label, next = false) => item
    ? `<a href="${link(item.slug + '.html')}"><span>${label}</span><strong>${escape(item.title)} ${next ? '→' : ''}</strong></a>`
    : '<div></div>';
  const starter = page.guide === 'admin'
    ? '<div class="toc-bottom">' + locale.adminStart + '<a href="install.html">' + locale.adminStartLink + '</a></div>'
    : '<div class="toc-bottom">' + locale.userStart + '<a href="quickstart.html">' + locale.userStartLink + '</a></div>';
  const footer = page.guide === 'admin'
    ? locale.adminFooter + '<a href="configuration.html">' + locale.adminFooterLink + '</a>'
    : locale.userFooter + '<a href="support.html">' + locale.userFooterLink + '</a>';
  const sourceLink = page.sources
    ? `<a class="article-source" href="https://github.com/SamuelSupe/mcphub/blob/main/${page.sources[0].file}">${locale.source}</a>` : '';

  writeFileSync(path.join(output, page.file), `<!doctype html>
<html lang="${page.language}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>${escape(page.title)} · MCPHub ${locale.helpCenter}</title>
  <meta name="description" content="${escape(page.description)}">
  <meta name="theme-color" content="#173f38">
  <link rel="canonical" href="${siteUrl + page.file}">
  <link rel="alternate" hreflang="zh-CN" href="${siteUrl + page.slug}.html">
  <link rel="alternate" hreflang="en" href="${siteUrl}en/${page.slug}.html">
  <link rel="alternate" hreflang="x-default" href="${siteUrl + page.slug}.html">
  <link rel="icon" type="image/svg+xml" href="data:image/svg+xml,${icon}">
  <link rel="stylesheet" href="${siteLink('assets/styles.css')}">
  <script defer src="${siteLink('assets/app.js')}"></script>
</head>
<body data-root="${languageRoot}">
<a class="skip" href="#main">${locale.skip}</a>
<header class="header">
  <a class="brand" href="${link('index.html')}">${logo}<strong>MCPHub</strong><span>${locale.helpCenter}</span></a>
  <div class="header-actions">
    <button class="search-trigger" aria-haspopup="dialog"><span class="search-long">${locale.search}</span><span class="search-short">${locale.searchShort}</span><kbd>⌘ K</kbd></button>
    <a class="language-switch" href="${siteLink(counterpart)}" lang="${locale.otherLanguage}" hreflang="${locale.otherLanguage}" aria-label="${locale.switchLanguage}">${locale.language}</a>
    <a class="source-link" href="https://github.com/SamuelSupe/mcphub" target="_blank" rel="noopener">${locale.project}</a>
    <button id="menu-toggle" aria-expanded="false" aria-controls="sidebar">${locale.menu}</button>
  </div>
</header>
<div class="layout">
  <aside id="sidebar" class="sidebar">
    <nav class="guide-switch" aria-label="${locale.chooseGuide}">
      <a href="${link('index.html')}"${page.guide === 'user' ? ' aria-current="true"' : ''}>${locale.user}</a>
      <a href="${link('admin/index.html')}"${page.guide === 'admin' ? ' aria-current="true"' : ''}>${locale.admin}</a>
    </nav>
    <div class="edition">${page.guideTitle} <span>v2.2.0</span></div>
    <nav aria-label="${locale.chapters}">${nav}</nav>
    <div class="sidebar-note">${locale.note}</div>
  </aside>
  <main id="main" class="main" tabindex="-1">
    <div class="breadcrumbs"><a href="${link(home)}">${page.guideTitle}</a><span>/</span><span>${escape(page.group)}</span></div>
    <article>
      <p class="eyebrow">${escape(page.group)}</p>
      <h1>${escape(page.title)}</h1>
      <p class="lead">${escape(page.description)}</p>
      <div class="article-body">${body.replaceAll('<table>', '<div class="table-wrap"><table>').replaceAll('</table>', '</table></div>')}</div>
    </article>
    <div class="article-meta">${locale.updated} ${sourceLink}</div>
    <nav class="pagination" aria-label="${locale.related}">${adjacent(guidePages[position - 1], locale.previous)}${adjacent(guidePages[position + 1], locale.next, true)}</nav>
    <footer>${footer}<span> MCPHub ${locale.helpCenter}</span></footer>
  </main>
  <aside class="toc">
    <p>${locale.outline}</p>
    <nav aria-label="${locale.pageOutline}">${headings.map(([, id, text]) => `<a href="#${id}">${text}</a>`).join('\n')}</nav>
    ${starter}
  </aside>
</div>
<dialog id="search-dialog" aria-labelledby="search-title">
  <div class="search-head"><h2 id="search-title">${locale.searchTitle}</h2><button id="search-close" aria-label="${locale.closeSearch}">${locale.close} <kbd>Esc</kbd></button></div>
  <label class="search-label" for="search-input">${locale.searchLabel}</label>
  <input id="search-input" type="search" placeholder="${locale.placeholder}" autocomplete="off">
  <p id="search-status" role="status" aria-live="polite"></p>
  <div id="search-results"></div>
</dialog>
</body>
</html>`);
  indexes[page.language].push({
    slug: page.slug, title: page.title, group: page.group, guideTitle: page.guideTitle,
    description: page.description,
    headings: headings.map(([, id, title]) => ({ id, title: title.replace(/<[^>]*>/g, '') })),
    text: body.replace(/<[^>]*>/g, ' ').replace(/&[a-z#0-9]+;/gi, ' ').replace(/\s+/g, ' ').trim(),
  });
}

for (const [language, index] of Object.entries(indexes)) {
  writeFileSync(path.join(output, locales[language].prefix + 'search-index.json'), JSON.stringify(index));
}
writeFileSync(path.join(output, '404.html'), `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>页面未找到 / Page not found · MCPHub</title><link rel="stylesheet" href="${siteUrl}assets/styles.css"><main class="not-found"><p class="eyebrow">404</p><h1>这篇文档不存在</h1><p>链接可能有误，请回到帮助中心选择章节。</p><a href="${siteUrl}">返回中文帮助中心 →</a><div lang="en"><h2>Page not found</h2><p>The link may be incorrect. Choose a chapter from the help center.</p><a href="${siteUrl}en/">Open the English help center →</a></div></main></html>`);

let checkedLinks = 0;
for (const page of pages) {
  const html = readFileSync(path.join(output, page.file), 'utf8');
  const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map(match => match[1]);
  if (new Set(ids).size !== ids.length) throw new Error(`Duplicate anchor in ${page.file}`);
  for (const [, target] of html.matchAll(/\b(?:href|src)="([^"]+)"/g)) {
    if (/^(?:https?:|data:|mailto:)/.test(target)) continue;
    const [file, fragment] = target.split('#');
    const destination = path.resolve(output, path.dirname(page.file), file || path.basename(page.file));
    if (!destination.startsWith(output + path.sep) || !existsSync(destination)) throw new Error(`Broken link in ${page.file}: ${target}`);
    if (fragment && !readFileSync(destination, 'utf8').includes(`id="${decodeURIComponent(fragment)}"`)) throw new Error(`Missing anchor in ${page.file}: ${target}`);
    checkedLinks++;
  }
}
console.log(`Built ${pages.length} pages in Chinese and English; validated ${checkedLinks} local links and assets.`);
