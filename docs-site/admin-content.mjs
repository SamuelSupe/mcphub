import { readFileSync } from 'node:fs';
import path from 'node:path';
import { Marked, marked } from 'marked';

const normalizeHeading = text => text.replace(/[`*]/g, '');
const headingId = text => normalizeHeading(text).toLowerCase()
  .replace(/[^\p{L}\p{N}_\-\s]/gu, '').replace(/\s/g, '-');
const escapeAttribute = text => text.replaceAll('&', '&amp;').replaceAll('"', '&quot;');

function readSection(repository, source) {
  let markdown = readFileSync(path.join(repository, source.file), 'utf8');
  if (!source.heading) markdown = markdown.replace(/^# .*\n/, '').replace(/^\[(?:English|中文|简体中文)\].*$/m, '');
  const tokens = marked.lexer(markdown);
  if (!source.heading) return tokens;
  const start = tokens.findIndex(token => token.type === 'heading' && normalizeHeading(token.text) === source.heading);
  if (start < 0) throw new Error(`Missing section: ${source.file} / ${source.heading}`);
  const depth = tokens[start].depth;
  let end = start + 1;
  while (end < tokens.length) {
    const token = tokens[end];
    if (token.type === 'heading' && (source.children === false || token.depth <= depth)) break;
    end++;
  }
  return tokens.slice(start, end).map(token => token.type === 'heading'
    ? { ...token, depth: token.depth - depth + 2 } : token);
}

export function loadAdminContent(repository, pages, language = 'zh-CN') {
  const sections = new Map();
  const suffix = language === 'en' ? '' : '.zh-CN';
  const userGuide = 'docs/user-guide' + suffix + '.md';
  const userSections = language === 'en' ? [
    ['before-you-start', 'install'], ['install-the-cli', 'install'],
    ['connect-an-mcp-client', 'quickstart'], ['connect-a-personal-account', 'accounts'],
    ['write-approvals', 'approvals'], ['manage-client-authorizations', 'authorizations'],
    ['diagnostics-and-troubleshooting', 'troubleshooting'],
    ['manual-login-and-compatible-connections', 'login'], ['local-data-and-logout', 'privacy'],
  ] : [
    ['接入前准备', 'install'], ['接入-mcp-客户端', 'quickstart'],
    ['连接个人账号', 'accounts'], ['写操作审批', 'approvals'],
    ['管理客户端授权', 'authorizations'], ['诊断与常见问题', 'troubleshooting'],
  ];
  const destinations = new Map([
    ['docs/admin-guide' + suffix + '.md', 'admin/index.html'],
    ['docs/configuration' + suffix + '.md', 'admin/configuration.html'],
    ['deploy/README' + suffix + '.md', 'admin/deployment.html'],
    ['docs/sso-and-user-management' + suffix + '.md', 'admin/sso.html'],
    ['docs/vault-accounts' + suffix + '.md', 'admin/vault.html'],
    [userGuide, 'index.html'],
    ...userSections.map(([anchor, slug]) => [userGuide + '#' + anchor, slug + '.html']),
  ]);

  for (const page of pages) {
    const pageSections = (page.sources || []).map(source => ({
      source, tokens: readSection(repository, source),
    }));
    sections.set(page.slug, pageSections);
    for (const { source, tokens } of pageSections) {
      for (const token of tokens) {
        if (token.type !== 'heading') continue;
        const anchor = headingId(token.text);
        destinations.set(`${source.file}#${anchor}`, `${page.slug}.html#${anchor}`);
      }
    }
  }

  for (const file of ['docs/admin-guide' + suffix + '.md', 'docs/configuration' + suffix + '.md']) {
    const headings = marked.lexer(readFileSync(path.join(repository, file), 'utf8'))
      .filter(token => token.type === 'heading' && token.depth > 1);
    for (const token of headings) {
      if (!destinations.has(`${file}#${headingId(token.text)}`)) {
        throw new Error(`Unassigned documentation section: ${file} / ${token.text}`);
      }
    }
  }

  function resolveLink(href, sourceFile, pageSlug) {
    if (/^(?:https?:|mailto:)/.test(href)) return href;
    const [file, fragment = ''] = href.split('#');
    const repositoryPath = path.posix.normalize(path.posix.join(path.posix.dirname(sourceFile), file || path.posix.basename(sourceFile)));
    if (!fragment && (repositoryPath === 'config.example.yaml' || /^deploy\/config\.[\w.-]+\.yaml$/.test(repositoryPath))) {
      const pageFile = (language === 'en' ? 'en/' : '') + pageSlug;
      return path.posix.relative(path.posix.dirname(pageFile), 'examples/' + repositoryPath);
    }
    const key = fragment ? `${repositoryPath}#${decodeURIComponent(fragment)}` : repositoryPath;
    const destination = destinations.get(key);
    if (destination) {
      const [targetFile, anchor] = destination.split('#');
      return path.posix.relative(path.posix.dirname(pageSlug), targetFile) + (anchor ? `#${anchor}` : '');
    }
    if (!fragment && destinations.has(repositoryPath)) {
      return path.posix.relative(path.posix.dirname(pageSlug), destinations.get(repositoryPath));
    }
    return `https://github.com/SamuelSupe/mcphub/blob/main/${repositoryPath}${fragment ? `#${fragment}` : ''}`;
  }

  return new Map(pages.filter(page => page.sources).map(page => {
    const body = sections.get(page.slug).map(({ source, tokens }) => {
      const renderer = new Marked({ renderer: {
        heading(token) {
          return `<h${token.depth} id="${headingId(token.text)}">${this.parser.parseInline(token.tokens)}</h${token.depth}>\n`;
        },
        link(token) {
          const href = resolveLink(token.href, source.file, page.slug);
          const title = token.title ? ` title="${escapeAttribute(token.title)}"` : '';
          return `<a href="${escapeAttribute(href)}"${title}>${this.parser.parseInline(token.tokens)}</a>`;
        },
      } });
      return renderer.parser(tokens);
    }).join('\n');
    return [page.slug, body];
  }));
}
