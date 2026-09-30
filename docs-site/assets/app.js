const labels = document.documentElement.lang === 'en' ? {
  copy: 'Copy', copyCode: 'Copy code', copied: 'Copied', copyFailed: 'Copy manually',
  loading: 'Preparing search…', unavailable: 'Search is unavailable. Use the chapter navigation.',
  ready: count => `Search ${count} help articles. Enter a keyword to start.`,
  found: count => `${count} related articles found`,
  empty: 'No results. Try “authorization”, “account”, “doctor”, or an error code.',
} : {
  copy: '复制', copyCode: '复制代码', copied: '已复制', copyFailed: '请手动复制',
  loading: '正在准备搜索…', unavailable: '搜索暂时不可用，请使用左侧目录。',
  ready: count => `搜索 ${count} 篇帮助文档。输入关键词开始。`,
  found: count => `找到 ${count} 篇相关文档`,
  empty: '没有找到相关文档。试试“授权”“账号”“doctor”或错误码。',
};
const menu = document.querySelector('#menu-toggle');
const sidebar = document.querySelector('#sidebar');
function closeMenu() { sidebar.classList.remove('is-open'); menu.setAttribute('aria-expanded', 'false'); }
menu.addEventListener('click', () => { const open = sidebar.classList.toggle('is-open'); menu.setAttribute('aria-expanded', String(open)); });
document.addEventListener('click', event => { if (!sidebar.contains(event.target) && !menu.contains(event.target)) closeMenu(); });
document.addEventListener('keydown', event => { if (event.key === 'Escape') closeMenu(); });

for (const block of document.querySelectorAll('pre')) {
  const code = block.querySelector('code');
  const button = document.createElement('button');
  button.type = 'button'; button.className = 'copy-button'; button.textContent = labels.copy; button.setAttribute('aria-label', labels.copyCode);
  button.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(code.textContent); button.textContent = labels.copied; }
    catch { button.textContent = labels.copyFailed; }
    setTimeout(() => { button.textContent = labels.copy; }, 2000);
  });
  block.prepend(button);
}

const dialog = document.querySelector('#search-dialog');
const input = document.querySelector('#search-input');
const results = document.querySelector('#search-results');
const status = document.querySelector('#search-status');
const root = document.body.dataset.root || '';
let searchIndex;
let loadingIndex;
async function openSearch() {
  if (!dialog.open) dialog.showModal();
  input.focus();
  if (!searchIndex) {
    status.textContent = labels.loading;
    try {
      loadingIndex ??= fetch(`${root}search-index.json`).then(response => { if (!response.ok) throw new Error('search'); return response.json(); });
      searchIndex = await loadingIndex;
    } catch { loadingIndex = undefined; status.textContent = labels.unavailable; return; }
  }
  renderSearch();
}
function renderSearch() {
  if (!searchIndex) return;
  const query = input.value.trim().toLowerCase();
  results.replaceChildren();
  if (!query) { status.textContent = labels.ready(searchIndex.length); return; }
  const terms = query.split(/\s+/);
  const matches = searchIndex.filter(page => terms.every(term => `${page.title} ${page.description} ${page.text}`.toLowerCase().includes(term))).sort((a, b) => Number(b.title.toLowerCase().includes(query)) - Number(a.title.toLowerCase().includes(query)));
  status.textContent = matches.length ? labels.found(matches.length) : labels.empty;
  for (const page of matches) {
    const link = document.createElement('a'); link.href = `${root}${page.slug}.html`; link.className = 'search-result';
    const group = document.createElement('span'); group.textContent = `${page.guideTitle} · ${page.group}`;
    const title = document.createElement('strong'); title.textContent = page.title;
    const excerpt = document.createElement('p');
    const hit = page.text.toLowerCase().indexOf(terms[0]);
    excerpt.textContent = hit < 0 ? page.description : `${hit > 25 ? '…' : ''}${page.text.slice(Math.max(0, hit - 25), hit + 85)}…`;
    link.append(group, title, excerpt); results.append(link);
  }
}
document.querySelector('.search-trigger').addEventListener('click', openSearch);
document.querySelector('#search-close').addEventListener('click', () => dialog.close());
dialog.addEventListener('click', event => { const rect = dialog.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) dialog.close(); });
input.addEventListener('input', renderSearch);
input.addEventListener('keydown', event => { if (event.key === 'ArrowDown') { event.preventDefault(); results.querySelector('a')?.focus(); } if (event.key === 'Enter') results.querySelector('a')?.click(); });
document.addEventListener('keydown', event => { if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); openSearch(); } });
if (!navigator.platform.toLowerCase().includes('mac')) document.querySelector('.search-trigger kbd').textContent = 'Ctrl K';
