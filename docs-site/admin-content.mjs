import { readFileSync } from 'node:fs';
import path from 'node:path';
import { Marked, marked } from 'marked';

const normalizeHeading = text => text.replace(/[`*]/g, '');
const headingId = text => normalizeHeading(text).toLowerCase()
  .replace(/[^\p{L}\p{N}_\-\s]/gu, '').replace(/\s/g, '-');
const escapeAttribute = text => text.replaceAll('&', '&amp;').replaceAll('"', '&quot;');

function readSection(repository, source) {
  let markdown = readFileSync(path.join(repository, source.file), 'utf8');
  if (!source.heading) markdown = markdown.replace(/^# .*\n/, '').replace(/^\[English\].*$/m, '');
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

export function loadAdminContent(repository, pages) {
  const sections = new Map();
  const destinations = new Map([
    ['docs/admin-guide.zh-CN.md', 'admin/index.html'],
    ['docs/configuration.zh-CN.md', 'admin/configuration.html'],
    ['deploy/README.zh-CN.md', 'admin/deployment.html'],
    ['docs/sso-and-user-management.zh-CN.md', 'admin/sso.html'],
    ['docs/vault-accounts.zh-CN.md', 'admin/vault.html'],
    ['docs/user-guide.zh-CN.md', 'index.html'],
    ['docs/user-guide.zh-CN.md#接入前准备', 'install.html'],
    ['docs/user-guide.zh-CN.md#接入-mcp-客户端', 'quickstart.html'],
    ['docs/user-guide.zh-CN.md#连接个人账号', 'accounts.html'],
    ['docs/user-guide.zh-CN.md#写操作审批', 'approvals.html'],
    ['docs/user-guide.zh-CN.md#管理客户端授权', 'authorizations.html'],
    ['docs/user-guide.zh-CN.md#诊断与常见问题', 'troubleshooting.html'],
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

  for (const file of ['docs/admin-guide.zh-CN.md', 'docs/configuration.zh-CN.md']) {
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
