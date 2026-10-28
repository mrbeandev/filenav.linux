// Admin UI
'use strict';

const savedSort = (() => { try { const v = JSON.parse(localStorage.getItem('fn-sort')); return v && ['name', 'size', 'type', 'mtime'].includes(v.key) ? v : null; } catch { return null; } })();
const S = {
  me: null,
  path: '',
  entries: [],
  sel: new Set(),
  sort: savedSort || { key: 'name', dir: 1 },
  layout: ['list', 'columns'].includes(localStorage.getItem('fn-layout')) ? localStorage.getItem('fn-layout') : 'table', // table/columns on wide screens; phones always use the list
  colSel: null, // file selected in column view (root-relative path)
  search: null, // {q, results, truncated}
  shares: [],   // all shares (for badges)
  clipboard: null,
};
let uploader;

const b64uToBuf = s => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/')), c => c.charCodeAt(0)).buffer;
const bufToB64u = b => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

async function boot() {
  try { S.me = await api('GET', '/api/me'); } catch (e) { document.body.textContent = 'Server unreachable: ' + e.message; return; }
  document.title = S.me.title;
  if (!S.me.authed) return renderLogin();
  startApp();
}

// ---------- login ----------
function renderLogin() {
  const err = h('div', { class: 'err' });
  const hasPasskeys = !!window.PublicKeyCredential && S.me.passkeys_on && S.me.passkeys > 0;
  const pf = passwordField({ placeholder: 'Passcode', autocomplete: 'current-password', 'aria-label': 'Passcode' });
  const pass = pf.input;
  const unlockBtn = h('button', { class: 'btn primary', type: 'submit', style: { justifyContent: 'center' } }, 'Unlock');
  const passForm = h('form', {
    class: 'pass-form' + (hasPasskeys ? ' hidden' : ''),
    onsubmit: e => {
      e.preventDefault(); err.textContent = '';
      busy(unlockBtn, async () => {
        try { await api('POST', '/api/login', { passcode: pass.value }); location.reload(); await new Promise(() => {}); } // keep spinning until reload
        catch (x) { err.textContent = x.message; pass.select(); }
      });
    }
  }, pf.el, unlockBtn);
  const showPass = () => { passForm.classList.remove('hidden'); fallback.classList.add('hidden'); pass.focus(); };
  const fallback = h('button', { class: 'btn ghost small', type: 'button', style: { justifyContent: 'center' }, onclick: showPass }, 'Use passcode instead');
  const pkBtn = h('button', { class: 'btn primary', type: 'button', style: { justifyContent: 'center' }, onclick: ev => busy(ev.currentTarget, () => passkeyLogin(err, false, onPkFail)), html: icon('key') + 'Sign in with passkey' });
  const onPkFail = () => { if (!S.me.passkey_only) showPass(); };
  $('#root').replaceChildren(h('div', { class: 'center' }, h('div', { class: 'card' },
    h('h1', {}, iconEl('logo', 'logo'), S.me.title),
    h('div', { class: 'dim' }, hasPasskeys ? 'Sign in with your passkey.' : 'Enter your passcode to continue.'),
    h('div', { class: 'login-box' },
      hasPasskeys ? pkBtn : null,
      hasPasskeys && !S.me.passkey_only ? fallback : null,
      !S.me.passkey_only || !hasPasskeys ? passForm : null,
      err))));
  if (hasPasskeys) passkeyLogin(err, true, onPkFail); // try passkey immediately
  else pass.focus();
}

async function passkeyLogin(err, silent, onFail) {
  try {
    const o = await api('GET', '/api/passkey/login/begin');
    const cred = await navigator.credentials.get({
      publicKey: { challenge: b64uToBuf(o.challenge), rpId: o.rpId, timeout: o.timeout, userVerification: o.userVerification, allowCredentials: [] },
    });
    await api('POST', '/api/passkey/login/finish', {
      id: bufToB64u(cred.rawId),
      clientDataJSON: bufToB64u(cred.response.clientDataJSON),
      authenticatorData: bufToB64u(cred.response.authenticatorData),
      signature: bufToB64u(cred.response.signature),
    });
    location.reload();
  } catch (e) {
    if (!silent || e.name !== 'NotAllowedError') err.textContent = e.name === 'NotAllowedError' ? 'Passkey sign-in was cancelled or not available.' : e.message;
    onFail && onFail();
  }
}

// ---------- app shell ----------
function startApp() {
  const searchInput = h('input', { class: 'input search', type: 'search', placeholder: 'Search', 'aria-label': 'Search in this folder' });
  let st;
  searchInput.addEventListener('input', () => {
    clearTimeout(st);
    const q = searchInput.value.trim();
    st = setTimeout(() => q ? doSearch(q) : (S.search = null, render()), 350);
  });
  S.searchInput = searchInput;
  const nav = (id, ic, label, fn) => h('button', { class: 'side-item', 'data-view': id, onclick: fn, html: icon(ic) + `<span>${label}</span>` });
  const tab = (id, ic, label, fn) => h('button', { 'data-view': id, onclick: fn, html: icon(ic) + `<span>${label}</span>` });
  // each tab remembers where you were; tapping the tab you're already on goes to its top
  const goTab = (view, top) => {
    if (S.view === view) { if (view === 'files') { S.search = null; searchInput.value = ''; } location.hash = top; return; }
    location.hash = Nav.last[view] || top;
  };
  const goFiles = () => goTab('files', '#/');
  const goStorage = () => goTab('storage', '#storage');
  const goMovies = () => goTab('movies', '#movies');
  const goShares = () => goTab('shares', '#shares');
  const goSettings = () => goTab('settings', '#settings');
  const logout = async () => { await task('Logging out…', api('POST', '/api/logout').catch(() => {})); location.reload(); };
  $('#root').replaceChildren(h('div', { class: 'app' },
    h('aside', { class: 'sidebar' },
      h('a', { class: 'brand', href: '#/', onclick: e => { e.preventDefault(); S.view === 'files' ? goFiles() : (location.hash = '#/'); } }, iconEl('logo', 'logo'), h('span', {}, S.me.title)),
      h('div', { class: 'side-h' }, 'Library'),
      nav('files', 'folder', 'Files', goFiles),
      S.me.movies_on ? nav('movies', 'vid', 'Movies', goMovies) : null,
      S.me.analyzer_on ? nav('storage', 'chart', 'Storage', goStorage) : null,
      S.me.shares_on ? nav('shares', 'link', 'Shared Links', goShares) : null,
      h('div', { class: 'side-h' }, 'Account'),
      S.me.auth ? nav('settings', 'gear', 'Settings', goSettings) : null,
      S.me.auth ? nav('logout', 'logout', 'Log Out', logout) : null,
      h('div', { class: 'side-foot' }, h('div', { class: 'meter', id: 'disk-meter' }), h('span', { class: 'disk', id: 'disk' }),
        S.me.read_only ? h('span', { class: 'badge' }, 'Read-only') : null)),
    h('div', { class: 'content' },
      h('header', { class: 'navbar', id: 'navbar' },
        h('button', { class: 'btn ghost back hidden', id: 'back', 'aria-label': 'Back', onclick: () => history.back(), html: icon('left') + '<span id="back-l"></span>' }),
        h('div', { class: 'nav-title', id: 'nav-title' }),
        h('div', { class: 'nav-actions', id: 'nav-actions' })),
      h('main', { id: 'main' }),
      h('div', { id: 'selbar', class: 'selbar hidden' })),
    h('nav', { class: 'tabbar' },
      tab('files', 'folder', 'Files', goFiles),
      S.me.movies_on ? tab('movies', 'vid', 'Movies', goMovies) : null,
      S.me.analyzer_on ? tab('storage', 'chart', 'Storage', goStorage) : null,
      S.me.shares_on ? tab('shares', 'link', 'Shared', goShares) : null,
      S.me.auth ? tab('settings', 'gear', 'Settings', goSettings) : null)));

  uploader = new Uploader({
    initURL: '/api/upload/init', chunkURL: uid => '/api/upload/' + uid, chunkSize: S.me.chunk_size,
    onChange: () => renderUploads(uploader, $('#uploads')),
    onFileDone: () => { clearTimeout(S._rt); S._rt = setTimeout(() => load(true), 400); },
    onAllDone: () => { load(true); loadDisk(); },
  });
  setupDropTarget(files => upload(files), () => !S.me.read_only && S.view === 'files');
  window.addEventListener('beforeunload', e => { if (uploader.active) { e.preventDefault(); e.returnValue = ''; } });
  window.addEventListener('hashchange', route);
  document.addEventListener('keydown', onKey);
  document.addEventListener('click', e => { if (!e.target.closest('.menu')) closeMenu(); });
  $('#main').addEventListener('scroll', e => $('#navbar').classList.toggle('scrolled', e.target.scrollTop > 40), { passive: true });
  route();
  loadDisk();
}

// hash routes: #/<path> = files, #storage[/<path>] = storage analyzer
// where each tab was left (hash + scroll), kept for the browser session
const Nav = (() => {
  let saved = {};
  try { saved = JSON.parse(sessionStorage.getItem('fn-nav') || '{}'); } catch { }
  return { last: saved.last || {}, scroll: saved.scroll || {}, cur: null,
    persist() { try { sessionStorage.setItem('fn-nav', JSON.stringify({ last: this.last, scroll: this.scroll })); } catch { } } };
})();
const viewOf = hsh => hsh.startsWith('#storage') ? 'storage' : hsh.startsWith('#movies') ? 'movies' : hsh.startsWith('#shares') ? 'shares' : hsh.startsWith('#settings') ? 'settings' : 'files';
// restore the scroll position of a page once its content is rendered
function restoreScroll() {
  const y = Nav.scroll[location.hash || '#/'];
  if (y) requestAnimationFrame(() => { const m = $('#main'); if (m) m.scrollTop = y; });
}

function route() {
  const hsh = location.hash;
  const view = viewOf(hsh);
  // remember the page we're leaving (and how far it was scrolled)
  if (Nav.cur != null) Nav.scroll[Nav.cur] = $('#main').scrollTop;
  Nav.cur = hsh || '#/';
  Nav.last[view] = hsh || '#/';
  const ks = Object.keys(Nav.scroll); if (ks.length > 200) delete Nav.scroll[ks[0]];
  Nav.persist();
  S.view = view;
  S._restorePending = true; // the next full render of this page restores its scroll
  $$('[data-view]').forEach(b => b.classList.toggle('active', b.dataset.view === view));
  $('#navbar').classList.remove('scrolled');
  $('#main').scrollTop = 0;
  if (view !== 'storage') storLast = null;
  if (view === 'shares' || view === 'settings') {
    S.sel.clear(); S.clipboard = null; renderSelbar();
    return view === 'shares' ? showSharesPage() : showSettingsPage(hsh.replace(/^#settings\/?/, '') || 'general');
  }
  if (view === 'movies') { S.sel.clear(); S.clipboard = null; renderSelbar(); return showMovies(hsh.replace(/^#movies\/?/, '')); }
  if (view === 'storage') { S.sel.clear(); S.clipboard = null; renderSelbar(); return showStorage(decodeURIComponent(hsh.replace(/^#storage\/?/, ''))); }
  const p = pathFromHash();
  if (p !== S.path || !S.loaded) { S.search = null; if (S.searchInput) S.searchInput.value = ''; }
  load();
}

const pathFromHash = () => decodeURIComponent(location.hash.replace(/^#\/?/, '')).replace(/\/+$/, '');
function go(p) { location.hash = '#/' + p.split('/').map(encodeURIComponent).join('/'); }

let loadSeq = 0;
async function load(keepSel) {
  if (S.view === 'movies') return;
  if (S.view === 'storage') { setTimeout(route, 900); return; } // analyzer applies changes within ~1 s
  const prev = S.path;
  S.path = pathFromHash();
  if (S._restorePending) { S._restorePending = false; S._restore = true; }
  if (!keepSel) S.sel.clear();
  const seq = ++loadSeq;
  // navigating to another folder: show its title + skeleton if the list is slow
  const skel = prev !== S.path || !S.loaded ? setTimeout(() => { if (seq === loadSeq) renderLoading(); }, 120) : null;
  try {
    const [d, sh] = await Promise.all([api('GET', '/api/list?path=' + encPath(S.path)), S.me.shares_on ? api('GET', '/api/shares') : { shares: [] }]);
    clearTimeout(skel);
    if (seq !== loadSeq) return; // user already went elsewhere
    S.entries = d.entries; S.shares = sh.shares; S.loaded = true;
    COL.cache.delete(S.path); COL.cache.set(S.path, { t: Date.now(), entries: d.entries }); // column view shows it again as a parent column

    for (const n of [...S.sel]) if (!S.entries.some(e => e.name === n)) S.sel.delete(n);
  } catch (e) {
    clearTimeout(skel);
    if (seq !== loadSeq) return;
    if (e.status === 401) return location.reload();
    toast(e.message, true);
    if (e.status === 404 && S.path) return go(S.path.split('/').slice(0, -1).join('/'));
    S.entries = [];
  }
  render();
}

// shown while a folder loads: real title + breadcrumb, skeleton rows
function renderLoading() {
  if (S.view !== 'files') return;
  const parts = S.path ? S.path.split('/') : [];
  const title = parts.length ? parts[parts.length - 1] : 'Files';
  setNav(title, parts.length ? (parts.length > 1 ? parts[parts.length - 2] : 'Files') : null, fileActions());
  const wide = !narrowMQ.matches;
  // column view: keep the columns on screen, drop the ones past the new folder's
  // parent and show a loading column for the folder being opened
  const strip = $('#main .mcols');
  if (wide && S.layout === 'columns' && strip) {
    const parent = parts.slice(0, -1).join('/');
    const cols = [...strip.querySelectorAll('.mcol')];
    const pi = cols.findIndex(c => c.dataset.path === parent);
    if (pi >= 0) {
      cols.slice(pi + 1).forEach(c => c.remove());
      const col = h('div', { class: 'mcol mcol-loading', 'data-path': S.path, style: { width: (COL.widths[colKey(S.path)] || COL.W) + 'px' } },
        h('div', { class: 'mcol-list' }, skeletonRows(6, 'mskel')));
      strip.querySelector('.mcol-end').before(col);
      const t = $('#main .large-title'); if (t) t.textContent = title;
      const sub = $('#main .list-tools .subtitle'); if (sub) sub.textContent = 'Loading…';
      renderCrumbs();
      const wrap = strip.parentNode; wrap.scrollLeft = wrap.scrollWidth;
      return;
    }
  }
  let body = skeletonRows(8, 'group');
  if (wide && S.layout === 'columns') {
    body = h('div', { class: 'mcols-wrap' }, h('div', { class: 'mcols' },
      h('div', { class: 'mcol', style: { width: COL.W + 'px' } }, h('div', { class: 'mcol-list' }, skeletonRows(6, 'mskel'))), h('div', { class: 'mcol-end' })));
  } else if (wide && S.layout === 'table') {
    const cell = w => h('td', {}, h('i', { class: 'skel-tx', style: { width: w } }));
    body = h('div', { class: 'ftable-wrap', 'aria-busy': 'true', 'aria-label': 'Loading' }, h('table', { class: 'ftable skel-table' },
      h('thead', {}, h('tr', {}, h('th', { class: 'c-sel' }), ...[['c-name', 'Name'], ['c-size', 'Size'], ['c-type', 'Type'], ['c-date', 'Date Modified']].map(([c, l]) => h('th', { class: c }, h('button', { disabled: true }, l))), h('th', { class: 'c-more' }))),
      h('tbody', {}, Array.from({ length: 8 }, (_, i) => h('tr', {},
        h('td', { class: 'c-sel' }), h('td', { class: 'c-name' }, h('div', { class: 'nm' }, h('i', { class: 'skel-ic' }), h('i', { class: 'skel-tx', style: { width: (40 + (i * 37) % 45) + '%' } }))),
        cell('60%'), cell('55%'), cell('70%'), h('td', { class: 'c-more' }))))));
  }
  setMain(h('nav', { class: 'crumbs', id: 'crumbs' }), h('h1', { class: 'large-title' }, title),
    h('div', { class: 'list-tools' }, h('p', { class: 'subtitle' }, 'Loading…'), wide ? layoutToggle() : null), body);
  renderCrumbs();
}

async function loadDisk() {
  try {
    const d = await api('GET', '/api/disk');
    $('#disk').textContent = `${fmtSize(d.free)} available of ${fmtSize(d.total)}`;
    const used = 1 - d.free / d.total;
    $('#disk-meter').replaceChildren(h('i', { style: { width: (used * 100).toFixed(1) + '%', background: used > .9 ? 'var(--red)' : used > .75 ? 'var(--orange)' : 'var(--acc)' } }));
  } catch { }
}

let searchSeq = 0;
async function doSearch(q) {
  const seq = ++searchSeq;
  const bar = S.searchInput?.parentNode;
  bar?.classList.add('searching');
  try {
    const r = await api('GET', `/api/search?path=${encPath(S.path)}&q=${encodeURIComponent(q)}`);
    if (seq !== searchSeq) return; // a newer search is running
    S.search = { q, results: r.results, truncated: r.truncated };
    S.sel.clear();
    render();
  } catch (e) { if (seq === searchSeq) toast(e.message, true); }
  finally { if (seq === searchSeq) $('.searchbar')?.classList.remove('searching'); }
}

// ---------- rendering ----------
function setNav(title, backLabel, actions) {
  $('#nav-title').textContent = title;
  const back = $('#back');
  back.classList.toggle('hidden', !backLabel);
  if (backLabel) $('#back-l').textContent = backLabel;
  $('#nav-actions').replaceChildren(...(actions || []).filter(Boolean));
}

function fileActions() {
  const ro = S.me.read_only;
  return [
    ro ? null : h('button', { class: 'btn ghost', title: 'New folder', onclick: newFolder, html: icon('folderPlus') }),
    ro ? null : h('button', { class: 'btn ghost', title: 'New text file', onclick: newFile, html: icon('fileplus') }),
    h('button', { class: 'btn ghost', title: 'Size of this folder', onclick: () => folderSize(S.path), html: icon('chart') }),
    S.me.shares_on ? h('button', { class: 'btn ghost', title: 'Share this folder', onclick: () => shareDialog(S.path, true), html: icon('share') }) : null,
    ro ? null : h('button', { class: 'btn primary', title: 'Upload', onclick: e => uploadMenu(e), html: icon('upload') + '<span class="lbl hide-sm">Upload</span>' }),
  ];
}

function uploadMenu(ev) {
  ev.stopPropagation();
  closeMenu();
  const m = h('div', { class: 'menu' },
    h('button', { onclick: async () => { closeMenu(); upload(await pickFiles()); }, html: icon('upload') + '<span>Upload Files</span>' }),
    h('button', { onclick: async () => { closeMenu(); upload(await pickFiles(true)); }, html: icon('folder') + '<span>Upload Folder</span>' }));
  if (matchMedia('(max-width: 640px)').matches) document.body.append(h('div', { class: 'menu-bg', onclick: closeMenu }));
  document.body.append(m);
  const r = ev.currentTarget.getBoundingClientRect(), mr = m.getBoundingClientRect();
  m.style.left = Math.max(8, Math.min(r.right - mr.width, innerWidth - mr.width - 8)) + 'px';
  m.style.top = (r.bottom + 6) + 'px';
}

function sortEntries(entries) {
  return [...entries].sort((a, b) => {
    if (a.dir !== b.dir) return a.dir ? -1 : 1;
    const k = S.sort.key;
    const byName = () => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });
    const r = k === 'name' ? byName()
      : k === 'size' ? a.size - b.size
      : k === 'type' ? (typeLabel(a.name, a.dir).localeCompare(typeLabel(b.name, b.dir)) || byName() * S.sort.dir)
      : new Date(a.mtime) - new Date(b.mtime);
    return r * S.sort.dir;
  });
}

function render() {
  if (S.view !== 'files') return;
  const parts = S.path ? S.path.split('/') : [];
  const title = parts.length ? parts[parts.length - 1] : 'Files';
  setNav(title, parts.length ? (parts.length > 1 ? parts[parts.length - 2] : 'Files') : null, fileActions());
  renderSelbar();
  const main = $('#main');
  const crumbs = h('nav', { class: 'crumbs', id: 'crumbs' });
  const head = [crumbs, h('h1', { class: 'large-title' }, title)];
  const search = h('div', { class: 'searchbar' }, iconEl('search'), S.searchInput);
  if (S.search) {
    setMain(...head, search, renderSearch());
    renderCrumbs();
    return;
  }
  const list = sortEntries(S.entries);
  const nd = list.filter(e => e.dir).length, nf = list.length - nd;
  const sub = h('p', { class: 'subtitle' }, [nd ? `${nd} folder${nd > 1 ? 's' : ''}` : '', nf ? `${nf} file${nf > 1 ? 's' : ''}` : ''].filter(Boolean).join(', ') || 'Empty folder');
  if (S.layout === 'columns' && !narrowMQ.matches) {
    setMain(...head, h('div', { class: 'list-tools' }, sub, layoutToggle()), columnView());
    renderCrumbs();
    return;
  }
  if (!list.length) {
    setMain(...head, h('div', { class: 'list-tools' }, sub, narrowMQ.matches ? null : layoutToggle()), h('div', { class: 'empty', html: icon('folder') + '<p>This folder is empty.<br>Drop files here or tap Upload.</p>' }));
    renderCrumbs();
    return;
  }
  const sharedPaths = new Set(S.shares.filter(s => !s.expired).map(s => s.path));
  const allSel = list.every(e => S.sel.has(e.name));
  const setSort = key => { S.sort = { key, dir: S.sort.key === key ? -S.sort.dir : (key === 'name' || key === 'type' ? 1 : -1) }; localStorage.setItem('fn-sort', JSON.stringify(S.sort)); render(); };
  const sortBtn = (key, label) => h('button', { class: S.sort.key === key ? 'on' : null, onclick: () => setSort(key) },
    label + (S.sort.key === key ? (S.sort.dir > 0 ? ' ↑' : ' ↓') : ''));
  const selAll = h('input', { type: 'checkbox', class: 'sel-circle', title: 'Select all', 'aria-label': 'Select all', checked: allSel, onclick: e => { e.stopPropagation(); list.forEach(x => e.target.checked ? S.sel.add(x.name) : S.sel.delete(x.name)); render(); } });
  const view = layoutToggle();
  if (S.layout === 'table' && !narrowMQ.matches) {
    setMain(...head, h('div', { class: 'list-tools' }, sub, view), search, fileTable(list, sharedPaths, selAll, setSort));
  } else {
    setMain(...head, h('div', { class: 'list-tools' }, sub, view), search,
      h('div', { class: 'group-h' }, selAll,
        h('span', { class: 'grow' }), 'Sort:', sortBtn('name', 'Name'), sortBtn('size', 'Size'), sortBtn('type', 'Type'), sortBtn('mtime', 'Date')),
      h('div', { class: 'group' }, list.map(e => row(e, joinPath(S.path, e.name), sharedPaths))));
  }
  renderCrumbs();
  if (S._restore) { S._restore = false; restoreScroll(); }
}

const narrowMQ = matchMedia('(max-width: 760px)');
narrowMQ.addEventListener('change', () => { if (S.view === 'files' && S.loaded && !S.search) render(); });
// ---------- column (Miller) view ----------
// The address (#/a/b/c) is the deepest open folder; every ancestor is a column.
// A file chosen in the last column shows a preview + details column.
const COL = { cache: new Map(), widths: JSON.parse(localStorage.getItem('fn-colw') || '{}'), W: 240, PREVIEW_W: 340 };
const colKey = p => p || '/';
// Parent columns come from this cache, so opening a subfolder never refetches
// (and never flashes) the columns to its left. A folder's list is refreshed
// whenever it becomes the open folder again (load() always fetches S.path).
function colCached(p) {
  if (p === S.path && S.loaded) return S.entries;
  return COL.cache.get(p)?.entries || null;
}
async function colEntries(p) {
  const c = colCached(p);
  if (c) return c;
  const d = await api('GET', '/api/list?path=' + encPath(p), undefined, { quiet: true });
  COL.cache.set(p, { t: Date.now(), entries: d.entries });
  if (COL.cache.size > 200) COL.cache.delete(COL.cache.keys().next().value);
  return d.entries;
}

function columnView() {
  const parts = S.path ? S.path.split('/') : [];
  const paths = [''].concat(parts.map((_, i) => parts.slice(0, i + 1).join('/')));
  // a selected file only counts if it is inside the deepest folder
  if (S.colSel && S.colSel.split('/').slice(0, -1).join('/') !== S.path) S.colSel = null;
  // remember where each existing column was scrolled, and the strip's own scroll
  const old = $('#main .mcols');
  const scrolls = new Map(old ? [...old.querySelectorAll('.mcol[data-path]')].map(c => [c.dataset.path, c.querySelector('.mcol-list')?.scrollTop || 0]) : []);
  const stripLeft = old ? old.parentNode.scrollLeft : 0;
  const strip = h('div', { class: 'mcols', tabindex: '-1' });
  const wrap = h('div', { class: 'mcols-wrap' }, strip);
  const cols = paths.map((p, i) => {
    const listEl = h('div', { class: 'mcol-list' });
    const col = h('div', { class: 'mcol', 'data-path': p, style: { width: (COL.widths[colKey(p)] || COL.W) + 'px' } }, listEl);
    col.append(resizer(col, colKey(p)));
    const active = i === paths.length - 1 ? S.colSel : paths[i + 1];
    const cached = colCached(p);
    if (cached) renderColumn(listEl, p, cached, active, scrolls.get(p));
    else { listEl.append(skeletonRows(5, 'mskel')); fillColumn(listEl, p, active); }
    return col;
  });
  strip.append(...cols);
  if (S.colSel) {
    const e = S.entries.find(x => joinPath(S.path, x.name) === S.colSel);
    if (e) {
      const pv = h('div', { class: 'mcol mcol-preview', style: { width: (COL.widths['#preview'] || COL.PREVIEW_W) + 'px' } }, filePreview(e, S.colSel));
      pv.append(resizer(pv, '#preview'));
      strip.append(pv);
    }
  }
  strip.append(h('div', { class: 'mcol-end' }));
  // keep the newest column in view (starting from where the strip was, so it doesn't jump)
  requestAnimationFrame(() => { wrap.style.scrollBehavior = 'auto'; wrap.scrollLeft = stripLeft; wrap.style.scrollBehavior = ''; wrap.scrollLeft = wrap.scrollWidth; });
  return wrap;
}

async function fillColumn(listEl, p, activePath) {
  let entries;
  try { entries = await colEntries(p); }
  catch (e) { listEl.replaceChildren(h('div', { class: 'mcol-msg' }, e.message)); return; }
  renderColumn(listEl, p, entries, activePath);
}

function renderColumn(listEl, p, entries, activePath, scrollTop) {
  const list = sortEntries(entries);
  if (!list.length) { listEl.replaceChildren(h('div', { class: 'mcol-msg' }, 'Empty folder')); return; }
  const files = list.filter(x => !x.dir).map(x => ({ e: x, full: joinPath(p, x.name) }));
  listEl.replaceChildren(...list.map(e => {
    const full = joinPath(p, e.name);
    const on = full === activePath;
    const it = h('div', {
      class: 'mrow' + (on ? ' on' : '') + (e.dir ? ' dir' : '') + (p === S.path && S.sel.has(e.name) ? ' sel' : ''),
      tabindex: '0', title: e.name, role: 'button',
      onclick: ev => {
        if (p === S.path && (ev.ctrlKey || ev.metaKey)) { toggleSel(e.name); return; }
        if (e.dir) { it.classList.add('opening'); S.colSel = null; go(full); return; }
        // file: select it (and navigate to its folder if it's in an earlier column)
        S.colSel = full;
        if (p !== S.path) { S.sel.clear(); go(p); } else render();
      },
      ondblclick: () => { if (!e.dir) openEntry(e, full, files); },
      oncontextmenu: ev => { ev.preventDefault(); if (p !== S.path) return; contextMenu(ev, e, full); },
      onkeydown: ev => { if (ev.key === 'Enter') it.click(); },
    }, fileThumb(e.name, e.dir), h('span', { class: 'mname' }, e.name),
      e.dir ? h('span', { class: 'mchev', html: icon('right') }) : null,
      e.dir ? h('span', { class: 'acc-spin', 'aria-hidden': 'true' }) : null);
    return it;
  }));
  if (scrollTop != null) requestAnimationFrame(() => { listEl.scrollTop = scrollTop; });
  else requestAnimationFrame(() => {
    const on = listEl.querySelector('.mrow.on');
    if (on) on.scrollIntoView({ block: 'nearest' });
  });
}

// drag the right edge of a column to resize it (remembered per folder)
function resizer(col, key) {
  const r = h('div', { class: 'mcol-rz', title: 'Drag to resize', 'aria-hidden': 'true' });
  r.addEventListener('pointerdown', ev => {
    ev.preventDefault(); r.setPointerCapture(ev.pointerId);
    const x0 = ev.clientX, w0 = col.getBoundingClientRect().width;
    const move = e2 => { col.style.width = Math.max(160, Math.min(720, w0 + e2.clientX - x0)) + 'px'; };
    const up = () => {
      r.removeEventListener('pointermove', move); r.removeEventListener('pointerup', up);
      COL.widths[key] = Math.round(col.getBoundingClientRect().width);
      const ks = Object.keys(COL.widths); if (ks.length > 300) delete COL.widths[ks[0]];
      localStorage.setItem('fn-colw', JSON.stringify(COL.widths));
    };
    r.addEventListener('pointermove', move); r.addEventListener('pointerup', up);
  });
  r.addEventListener('dblclick', () => { delete COL.widths[key]; localStorage.setItem('fn-colw', JSON.stringify(COL.widths)); col.style.width = (key === '#preview' ? COL.PREVIEW_W : COL.W) + 'px'; });
  return r;
}

// preview + details for a file chosen in column view
function filePreview(e, full) {
  const kind = viewKind(e.name);
  const raw = dlURL(full, true);
  const files = sortEntries(S.entries).filter(x => !x.dir).map(x => ({ e: x, full: joinPath(S.path, x.name) }));
  const open = () => openEntry(e, full, files);
  // cached server-side thumbnail (compressed image / first video frame); the
  // version suffix makes the browser cache follow file changes
  const thumb = '/api/thumb?path=' + encPath(full) + '&v=' + e.size + '-' + new Date(e.mtime).getTime();
  let media = null;
  if (kind === 'img' || kind === 'imgdec' || kind === 'vid') {
    const img = h('img', { alt: e.name, decoding: 'async', class: 'mprev-thumb' });
    const box = h('button', { class: 'mprev-pic' + (kind === 'vid' ? ' is-video' : ''), title: kind === 'vid' ? 'Play' : 'Open', onclick: open },
      h('span', { class: 'spin' }), img, kind === 'vid' ? h('span', { class: 'mprev-play', html: icon('play') }) : null);
    img.onload = () => box.classList.add('ready');
    img.onerror = () => {
      // no thumbnail (e.g. no ffmpeg): small browser-friendly images load directly
      if (kind === 'img' && img.src.includes('/api/thumb') && e.size < 15 << 20) { img.src = raw; return; }
      box.replaceWith(Object.assign(bigIcon(), { onclick: open }));
    };
    img.src = thumb;
    media = box;
  }
  else if (kind === 'aud') media = h('audio', { src: raw, controls: true, preload: 'none' });
  else if (kind === 'pdf') media = h('iframe', { src: raw, title: e.name });
  else if ((kind === 'text' || kind === 'md') && e.size < 512 << 10) {
    media = h('pre', { class: 'mprev-text' }, '…');
    api('GET', '/api/text?path=' + encPath(full), undefined, { quiet: true })
      .then(d => { media.textContent = d.content.length > 6000 ? d.content.slice(0, 6000) + '\n…' : d.content || '(empty file)'; })
      .catch(() => media.replaceWith(bigIcon()));
  }
  function bigIcon() { const t = fileThumb(e.name, false); t.classList.add('big'); return t; }
  const kv = (k, v) => v ? h('div', { class: 'kv' }, h('span', {}, k), h('b', {}, v)) : null;
  return h('div', { class: 'mprev' },
    h('div', { class: 'mprev-media' + (media ? ' has-' + kind : '') }, media || bigIcon()),
    h('div', { class: 'mprev-name' }, e.name),
    h('div', { class: 'mprev-kv' },
      kv('Type', typeLabel(e.name, false)), kv('Size', fmtSize(e.size) + (e.size >= 1024 ? ` (${e.size.toLocaleString()} bytes)` : '')),
      kv('Modified', e.mtime ? new Date(e.mtime).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : ''),
      kv('Where', '/' + full.split('/').slice(0, -1).join('/'))),
    h('div', { class: 'mprev-actions' },
      h('button', { class: 'btn primary', onclick: open, html: icon(kind === 'text' || kind === 'md' ? 'edit' : kind === 'vid' ? 'play' : 'eye') + '<span>' + (kind === 'vid' ? 'Play' : 'Open') + '</span>' }),
      h('a', { class: 'btn', href: dlURL(full), html: icon('download') + '<span>Download</span>' }),
      h('button', { class: 'btn ghost', title: 'More', 'aria-label': 'More actions', html: icon('more'), onclick: ev => { ev.stopPropagation(); contextMenu(ev, e, full); } })));
}

// List / Table switch (remembered); the table is used on wide screens only
function layoutToggle() {
  const set = v => { S.layout = v; localStorage.setItem('fn-layout', v); render(); };
  return h('div', { class: 'seg layout-seg hide-sm', role: 'group', 'aria-label': 'Layout' },
    h('button', { class: S.layout === 'list' ? 'on' : null, title: 'List', 'aria-label': 'List', 'aria-pressed': String(S.layout === 'list'), onclick: () => set('list'), html: icon('rows') }),
    h('button', { class: S.layout === 'table' ? 'on' : null, title: 'Table', 'aria-label': 'Table', 'aria-pressed': String(S.layout === 'table'), onclick: () => set('table'), html: icon('table') }),
    h('button', { class: S.layout === 'columns' ? 'on' : null, title: 'Columns', 'aria-label': 'Columns', 'aria-pressed': String(S.layout === 'columns'), onclick: () => set('columns'), html: icon('columns') }));
}

function fileTable(list, sharedPaths, selAll, setSort) {
  const th = (key, label, cls = '') => h('th', { class: cls + (S.sort.key === key ? ' on' : ''), 'aria-sort': S.sort.key === key ? (S.sort.dir > 0 ? 'ascending' : 'descending') : 'none' },
    h('button', { onclick: () => setSort(key) }, h('span', {}, label), h('span', { class: 'sort-ic', html: icon(S.sort.key === key && S.sort.dir < 0 ? 'down' : 'up') })));
  const rows = list.map(e => {
    const full = joinPath(S.path, e.name), sel = S.sel.has(e.name);
    const tr = h('tr', {
      class: (sel ? 'sel' : '') + (e.dir ? ' dir' : ''),
      oncontextmenu: ev => { ev.preventDefault(); contextMenu(ev, e, full); },
      onclick: ev => {
        if (ev.target.closest('input, button')) return;
        if (ev.ctrlKey || ev.metaKey || (S.sel.size && !e.dir)) { toggleSel(e.name); return; }
        if (e.dir) tr.classList.add('opening');
        openEntry(e, full, list.filter(x => !x.dir).map(x => ({ e: x, full: joinPath(S.path, x.name) })));
      },
    },
      h('td', { class: 'c-sel' }, h('input', { type: 'checkbox', class: 'sel-circle', checked: sel, 'aria-label': 'Select ' + e.name, onclick: ev => { ev.stopPropagation(); toggleSel(e.name); } })),
      h('td', { class: 'c-name' }, h('div', { class: 'nm' }, fileThumb(e.name, e.dir),
        h('a', { href: e.dir ? '#/' + full.split('/').map(encodeURIComponent).join('/') : dlURL(full), title: e.name, onclick: ev => ev.preventDefault() }, e.name),
        sharedPaths && sharedPaths.has(full) ? h('span', { class: 'badge share' }, 'Shared') : null,
        e.link ? h('span', { class: 'badge' }, 'Link') : null,
        e.dir ? h('span', { class: 'acc-spin', 'aria-hidden': 'true' }) : null)),
      h('td', { class: 'c-size' }, e.dir ? '—' : fmtSize(e.size)),
      h('td', { class: 'c-type' }, typeLabel(e.name, e.dir)),
      h('td', { class: 'c-date', title: e.mtime ? new Date(e.mtime).toLocaleString() : '' }, fmtDateFull(e.mtime)),
      h('td', { class: 'c-more' }, h('button', { class: 'btn ghost more', 'aria-label': 'More actions', html: icon('more'), onclick: ev => { ev.stopPropagation(); contextMenu(ev, e, full); } })));
    return tr;
  });
  return h('div', { class: 'ftable-wrap' }, h('table', { class: 'ftable' },
    h('thead', {}, h('tr', {}, h('th', { class: 'c-sel' }, selAll), th('name', 'Name', 'c-name'), th('size', 'Size', 'c-size'), th('type', 'Type', 'c-type'), th('mtime', 'Date Modified', 'c-date'), h('th', { class: 'c-more' }))),
    h('tbody', {}, rows)));
}

function row(e, full, sharedPaths, extraLine) {
  const sel = S.sel.has(e.name);
  const cell = h('div', {
    class: 'cell' + (sel ? ' sel' : ''),
    oncontextmenu: ev => { ev.preventDefault(); contextMenu(ev, e, full); },
  },
    h('a', { class: 'open', href: e.dir ? '#/' + full.split('/').map(encodeURIComponent).join('/') : dlURL(full), title: e.name, 'aria-label': e.name,
      onclick: ev => { ev.preventDefault(); if (S.sel.size && !e.dir) { toggleSel(e.name); return; } if (e.dir) cell.classList.add('opening'); openEntry(e, full, cell._list); } }),
    extraLine ? null : h('input', { type: 'checkbox', class: 'sel-circle', checked: sel, 'aria-label': 'Select ' + e.name, onclick: ev => { ev.stopPropagation(); toggleSel(e.name); } }),
    fileThumb(e.name, e.dir),
    h('div', { class: 'txt' },
      h('span', { class: 't1' }, e.name,
        sharedPaths && sharedPaths.has(full) ? h('span', { class: 'badge share' }, 'Shared') : null,
        e.link ? h('span', { class: 'badge' }, 'Link') : null),
      h('span', { class: 't2' }, extraLine || [e.dir ? 'Folder' : fmtSize(e.size), fmtDate(e.mtime)].filter(Boolean).join(' · '))),
    h('button', { class: 'btn ghost more', 'aria-label': 'More actions', html: icon('more'), onclick: ev => { ev.stopPropagation(); contextMenu(ev, e, full); } }),
    e.dir ? h('span', { class: 'acc', html: icon('right') }) : null,
    e.dir ? h('span', { class: 'acc-spin', 'aria-hidden': 'true' }) : null);
  return cell;
}

function toggleSel(name) { S.sel.has(name) ? S.sel.delete(name) : S.sel.add(name); render(); }

function renderSearch() {
  const { results, truncated, q } = S.search;
  const list = results.filter(x => !x.dir).map(x => ({ e: x, full: x.path }));
  return h('div', {},
    h('div', { class: 'group-h' }, `${results.length} result${results.length === 1 ? '' : 's'} for “${q}”` + (truncated ? ' (refine your search)' : '')),
    results.length ? h('div', { class: 'group' }, results.map(r => {
      const c = row(r, r.path, null, '/' + r.path.split('/').slice(0, -1).join('/'));
      c._list = list;
      return c;
    })) : h('div', { class: 'empty', html: icon('search') + '<p>No results.</p>' }));
}

function renderCrumbs() {
  const c = $('#crumbs');
  if (!c) return;
  const parts = S.path ? S.path.split('/') : [];
  if (!parts.length) { c.replaceChildren(); return; }
  const kids = [h('a', { href: '#/', html: icon('home'), title: 'Home' })];
  parts.forEach((p, i) => {
    kids.push(h('span', { class: 's' }, '/'));
    kids.push(h('a', { href: '#/' + parts.slice(0, i + 1).map(encodeURIComponent).join('/'), title: p, class: i === parts.length - 1 ? 'cur' : null }, p));
  });
  c.replaceChildren(...kids);
  scrollCrumbsToEnd(c);
}

function renderSelbar() {
  const bar = $('#selbar');
  const n = S.sel.size;
  if ((!n && !S.clipboard) || S.view !== 'files') { bar.replaceChildren(); bar.className = 'selbar hidden'; document.body.classList.remove('has-selbar'); return; }
  bar.className = 'selbar';
  document.body.classList.add('has-selbar');
  const paths = [...S.sel].map(x => joinPath(S.path, x));
  const kids = [];
  if (n) {
    const size = S.entries.filter(e => S.sel.has(e.name) && !e.dir).reduce((a, e) => a + e.size, 0);
    kids.push(h('div', { class: 'sel-info' }, h('b', {}, `${n} selected`), size ? h('span', { class: 'small dim' }, fmtSize(size)) : null), h('span', { class: 'grow' }),
      h('button', { class: 'btn', title: 'Download', onclick: () => downloadSel(), html: icon('download') + '<span class="lbl">Download</span>' }),
      S.me.read_only ? null : h('button', { class: 'btn', title: 'Cut (move)', onclick: () => { S.clipboard = { op: 'move', paths }; S.sel.clear(); render(); toast('Open a folder, then Paste'); }, html: icon('move') + '<span class="lbl">Move</span>' }),
      S.me.read_only ? null : h('button', { class: 'btn', title: 'Copy', onclick: () => { S.clipboard = { op: 'copy', paths }; S.sel.clear(); render(); toast('Open a folder, then Paste'); }, html: icon('copy') + '<span class="lbl">Copy</span>' }),
      S.me.read_only ? null : h('button', { class: 'btn', title: 'Move / copy to…', onclick: () => moveDialog(paths), html: icon('folder') + '<span class="lbl">Move to…</span>' }),
      S.me.read_only ? null : h('button', { class: 'btn danger', title: 'Delete', onclick: () => del(paths), html: icon('trash') + '<span class="lbl">Delete</span>' }),
      h('button', { class: 'btn ghost', title: 'Clear selection (Esc)', onclick: () => { S.sel.clear(); render(); }, html: icon('x') }));
  } else {
    const c = S.clipboard;
    kids.push(h('div', { class: 'sel-info' }, h('b', {}, `${c.paths.length} item${c.paths.length > 1 ? 's' : ''} to ${c.op}`), h('span', { class: 'small dim' }, 'Open the destination, then paste')), h('span', { class: 'grow' }),
      h('button', { class: 'btn primary', onclick: paste, html: icon('check') + 'Paste Here' }),
      h('button', { class: 'btn ghost', title: 'Cancel', onclick: () => { S.clipboard = null; render(); }, html: icon('x') }));
  }
  bar.replaceChildren(...kids);
}

// ---------- storage analyzer view ----------
const pctLabel = p => p >= 10 ? Math.round(p) + '%' : p >= 1 ? p.toFixed(1) + '%' : p > 0 ? '<1%' : '0%';
const CAT_COLORS = { Videos: 'var(--pink)', Images: 'var(--green)', Audio: 'var(--purple)', Documents: 'var(--teal)', Archives: 'var(--orange)', Code: 'var(--indigo)', Other: 'var(--gray)' };
let storTimer;

let storLast = null;
async function showStorage(rel) {
  clearTimeout(storTimer);
  if (S.view !== 'storage') return;
  let d;
  // navigating (not the 1 s refresh while scanning): loader if slow
  const refresh = storLast === rel;
  storLast = rel;
  const ld = refresh ? null : setTimeout(() => { if (S.view === 'storage') setMain(h('div', { class: 'view-loading' }, h('div', { class: 'spin' }))); }, 150);
  try { d = await api('GET', '/api/analyze?path=' + encPath(rel), undefined, { quiet: refresh }); }
  catch (e) { if (e.status === 401) return location.reload(); toast(e.message, true); return; }
  finally { clearTimeout(ld); }
  if (!refresh) setTimeout(restoreScroll, 0);
  if (S.view !== 'storage' || location.hash.replace(/^#storage\/?/, '') !== encodeURIComponent(rel).replace(/%2F/g, '/') && decodeURIComponent(location.hash.replace(/^#storage\/?/, '')) !== rel) return;
  const parts = rel ? rel.split('/') : [];
  const title = parts.length ? parts[parts.length - 1] : 'Storage';
  const goStor = p => { location.hash = '#storage' + (p ? '/' + p.split('/').map(encodeURIComponent).join('/') : ''); };
  setNav(title, parts.length ? (parts.length > 1 ? parts[parts.length - 2] : 'Storage') : null, [
    d.scanning
      ? h('button', { class: 'btn danger', title: 'Stop the running scan', onclick: () => cancelScan(rel), html: icon('x') + '<span class="lbl hide-sm">Stop Scan</span>' })
      : h('button', { class: 'btn', title: rel ? 'Rescan this folder or the whole drive' : 'Full rescan (normally not needed: the index updates itself)', onclick: ev => rel ? rescanMenu(ev, rel, d) : confirmRescan(rel, d), html: icon('refresh') + '<span class="lbl hide-sm">Rescan</span>' }),
  ]);
  const main = $('#main');
  const crumbs = h('nav', { class: 'crumbs' });
  if (parts.length) {
    const kids = [h('a', { href: '#storage', html: icon('chart'), title: 'Storage' })];
    parts.forEach((p, i) => { kids.push(h('span', { class: 's' }, '/'), h('a', { href: '#storage/' + parts.slice(0, i + 1).map(encodeURIComponent).join('/'), class: i === parts.length - 1 ? 'cur' : null }, p)); });
    crumbs.replaceChildren(...kids);
  }
  const head = [crumbs, h('h1', { class: 'large-title' }, title)];
  if (d.size == null) {
    setMain(...head, h('div', { class: 'stor-hero' }, h('div', { style: { gridColumn: '1 / -1' } },
      h('p', {}, d.scanning ? `Indexing your files… ${d.progress.toLocaleString()} folders so far` : 'Not indexed yet.'), h('div', { class: 'progress-line' }))));
    storTimer = setTimeout(() => showStorage(rel), 800);
    return;
  }
  // ring chart: categories of this folder (root also shows free space)
  const isRoot = !rel;
  const cats = [...(d.categories || [])].sort((a, b) => b.bytes - a.bytes);
  const total = isRoot && d.disk_total ? d.disk_total : d.size || 1;
  const R = 54, C = 2 * Math.PI * R;
  let off = 0;
  const arcs = cats.map(c => {
    const len = c.bytes / total * C;
    const el = `<circle r="${R}" cx="66" cy="66" fill="none" stroke="${CAT_COLORS[c.name]}" stroke-width="16" stroke-dasharray="${Math.max(len - 1.5, 0.01)} ${C}" stroke-dashoffset="${-off}"/>`;
    off += len;
    return el;
  }).join('');
  const ring = h('div', { class: 'ring', html: `<svg viewBox="0 0 132 132"><circle r="${R}" cx="66" cy="66" fill="none" stroke="var(--fill2)" stroke-width="16"/>${arcs}</svg>` },
    h('div', { class: 'c' }, h('b', {}, fmtSize(d.size)), isRoot && d.disk_total ? h('span', {}, 'used of', h('br'), fmtSize(d.disk_total)) : h('span', {}, `${d.files.toLocaleString()} files`)));
  // one row per type: colour · name · bar · size · share — everything for a type on the same line
  const used = d.size || 1;
  const typeRow = (color, name, bytes, pctOf, cls = '') => h('div', { class: 'stor-type ' + cls, title: `${name}: ${fmtSize(bytes)}` },
    h('i', { class: 'dot', style: { background: color } }),
    h('span', { class: 'nm' }, name),
    h('span', { class: 'bar' }, h('b', { style: { width: Math.max(bytes / pctOf * 100, bytes ? 0.8 : 0).toFixed(2) + '%', background: color } })),
    h('span', { class: 'sz' }, fmtSize(bytes)),
    h('span', { class: 'pc' }, pctLabel(bytes / pctOf * 100)));
  const legend = h('div', { class: 'stor-legend' },
    h('div', { class: 'stor-legend-h' }, h('span', {}, isRoot ? 'Used space by type' : 'This folder by type'), h('span', {}, 'Size'), h('span', {}, 'Share')),
    cats.map(c => typeRow(CAT_COLORS[c.name], c.name, c.bytes, used)),
    isRoot && d.disk_free != null ? h('div', { class: 'stor-free' },
      h('i', { class: 'dot', style: { background: 'var(--fill2)' } }),
      h('span', { class: 'nm' }, 'Free space', d.disk_total ? h('small', {}, ` · ${pctLabel(d.disk_free / d.disk_total * 100)} of disk`) : null),
      h('span', { class: 'sz' }, fmtSize(d.disk_free)),
      h('span', { class: 'pc' })) : null);
  const ago = d.last_scan ? fmtAgo(d.last_scan) : '';
  const meta = h('div', { class: 'stor-meta' },
    d.scanning ? h('span', {}, `${d.scan_mode === 'full' ? 'Rescanning' : d.scan_mode === 'folder' ? 'Rescanning a folder' : 'Checking for changes'}… ${d.progress.toLocaleString()} folders · showing previous results`) :
      d.live ? h('span', { class: 'live' }, 'Live: updates automatically') : h('span', {}, 'Checked for changes every 10 minutes'),
    h('span', {}, `${d.files.toLocaleString()} files · ${d.dirs.toLocaleString()} folders`),
    ago ? h('span', {}, `Indexed ${ago}` + (d.scan_ms != null ? ` in ${d.scan_ms < 1000 ? d.scan_ms + ' ms' : (d.scan_ms / 1000).toFixed(1) + ' s'}` : '')) : null,
    d.scanning ? h('div', { class: 'progress-line', style: { flexBasis: '100%' } }) : null);
  const max = Math.max(1, ...(d.entries || []).map(e => e.size));
  const entries = (d.entries || []).map(e => {
    const full = joinPath(rel, e.name);
    const pct = d.size ? e.size / d.size * 100 : 0;
    const c = h('div', { class: 'cell', oncontextmenu: ev => { ev.preventDefault(); contextMenu(ev, { name: e.name, dir: e.dir, size: e.size }, full); } },
      h('div', { class: 'sbar', style: { width: (e.size / max * 100).toFixed(1) + '%' } }),
      h('a', { class: 'open', href: e.dir ? '#storage/' + full.split('/').map(encodeURIComponent).join('/') : dlURL(full), onclick: ev => { ev.preventDefault(); e.dir ? goStor(full) : openEntry({ name: e.name, size: e.size }, full, null); } }),
      fileThumb(e.name, e.dir),
      h('div', { class: 'txt' }, h('span', { class: 't1' }, e.name), h('span', { class: 't2' }, e.dir ? `${(e.files || 0).toLocaleString()} files` : 'File')),
      h('span', { class: 'pct' }, pct >= 0.1 ? pct.toFixed(1) + '%' : ''),
      h('span', { class: 'size' }, fmtSize(e.size)),
      e.dir ? h('span', { class: 'acc', html: icon('right') }) : null);
    return c;
  });
  const largest = (d.largest || []).map(l => {
    const name = l.path.split('/').pop();
    const dir = l.path.split('/').slice(0, -1).join('/');
    return h('div', { class: 'cell', oncontextmenu: ev => { ev.preventDefault(); contextMenu(ev, { name, dir: false, size: l.size }, l.path); } },
      h('a', { class: 'open', href: dlURL(l.path), onclick: ev => { ev.preventDefault(); openEntry({ name, size: l.size }, l.path, null); } }),
      fileThumb(name, false),
      h('div', { class: 'txt' }, h('span', { class: 't1' }, name), h('span', { class: 't2' }, '/' + dir)),
      h('span', { class: 'size' }, fmtSize(l.size)));
  });
  // wide screens: overview card and "By size" side by side; largest files below
  setMain(...head,
    h('div', { class: 'stor-top' },
      h('section', { class: 'stor-col' }, h('div', { class: 'group-h stor-col-h' }, 'Overview'), h('div', { class: 'stor-hero' }, ring, legend, meta)),
      h('section', { class: 'stor-col' },
        h('div', { class: 'group-h stor-col-h' }, 'By size'),
        entries.length ? h('div', { class: 'group stor-bysize' }, entries) : h('div', { class: 'empty', html: icon('folder') + '<p>Nothing here.</p>' }))),
    largest.length ? h('div', { class: 'section-gap' }) : null,
    largest.length ? h('div', { class: 'group-h' }, 'Largest files') : null,
    largest.length ? h('div', { class: 'group' }, largest) : null);
  if (parts.length) scrollCrumbsToEnd(crumbs);
  if (d.scanning) storTimer = setTimeout(() => showStorage(rel), 1000);
}

async function confirmRescan(rel, d) {
  const took = d.scan_ms != null && d.mode === 'full' ? ` The last full scan took ${d.scan_ms < 1000 ? d.scan_ms + ' ms' : (d.scan_ms / 1000).toFixed(1) + ' s'}.` : '';
  const msg = `This re-reads every file and folder on the disk.${took}\n\nYou don't normally need this: the index updates itself when files change. ` +
    `Use it if sizes look wrong. You can cancel the scan while it runs, and the current results stay until it finishes.`;
  if (!await confirm2('Rescan all storage?', msg, 'Rescan', '')) return;
  try { await task('Starting scan…', api('POST', '/api/analyze/rescan')); } catch (e) { toast(e.message, true); }
  showStorage(rel);
}

function rescanMenu(ev, rel, d) {
  ev.stopPropagation(); closeMenu();
  const m = h('div', { class: 'menu' },
    h('button', { onclick: () => { closeMenu(); rescanFolder(rel); }, html: icon('folder') + '<span>Rescan this folder</span>' }),
    h('button', { onclick: () => { closeMenu(); confirmRescan(rel, d); }, html: icon('refresh') + '<span>Rescan whole drive…</span>' }));
  if (matchMedia('(max-width: 640px)').matches) document.body.append(h('div', { class: 'menu-bg', onclick: closeMenu }));
  document.body.append(m);
  const r = ev.currentTarget.getBoundingClientRect(), mr = m.getBoundingClientRect();
  m.style.left = Math.max(8, Math.min(r.right - mr.width, innerWidth - mr.width - 8)) + 'px';
  m.style.top = (r.bottom + 6) + 'px';
}
async function rescanFolder(rel) {
  setTimeout(() => showStorage(rel), 300); // shows "Rescanning a folder… N folders" while it runs
  try { const d = await task(`Rescanning “${rel.split('/').pop()}”…`, api('GET', '/api/size?' + new URLSearchParams({ path: rel, fresh: '1' }))); toast(`Rescanned: ${fmtSize(d.result.size)}`); }
  catch (e) { toast(e.message, true); }
  showStorage(rel);
}
async function cancelScan(rel) {
  if (!await confirm2('Stop the scan?', 'The scan stops now. The previous results are kept.', 'Stop Scan', 'danger', 'Keep Scanning')) return;
  try { await task('Stopping scan…', api('POST', '/api/analyze/cancel')); toast('Scan stopped'); }
  catch (e) { toast(e.message, true); }
  setTimeout(() => showStorage(rel), 300);
}

function fmtAgo(t) {
  const s = (Date.now() - new Date(t)) / 1000;
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + ' min ago';
  if (s < 86400) return Math.floor(s / 3600) + ' h ago';
  return new Date(t).toLocaleDateString();
}

// ---------- actions ----------
const dlURL = (p, inline) => '/api/dl?path=' + encPath(p) + (inline ? '&inline=1' : '');

function viewerOpts(e, full, list) {
  const dir = full.split('/').slice(0, -1).join('/');
  const files = list || S.entries.filter(x => !x.dir).map(x => ({ e: x, full: joinPath(S.path, x.name) }));
  const index = Math.max(0, files.findIndex(f => f.full === full));
  return {
    name: e.name, size: e.size, rel: full, raw: dlURL(full, true), dl: dlURL(full), text: '/api/text?path=' + encPath(full),
    save: S.me.read_only ? null : content => api('PUT', '/api/text', { path: full, content }).then(r => { load(true); return r; }),
    resolve: (rel, isImg) => dlURL(resolveRel(dir, rel), isImg),
    count: files.length, index,
    nav: i => openEntry(files[i].e, files[i].full, files),
    extra: S.me.shares_on ? [h('button', { class: 'btn ghost', html: icon('share'), title: 'Share', onclick: () => shareDialog(full, false) })] : [],
  };
}

function openEntry(e, full, list, as) {
  if (e.dir) { S.search = null; $('input.search').value = ''; return go(full); }
  openViewer({ ...viewerOpts(e, full, list), as });
}

function downloadSel() {
  const names = [...S.sel];
  const one = S.entries.find(e => e.name === names[0]);
  if (names.length === 1 && one && !one.dir) { location.href = dlURL(joinPath(S.path, names[0])); return; }
  const q = new URLSearchParams({ dir: S.path });
  names.forEach(n => q.append('names', n));
  location.href = '/api/zip?' + q;
}

function upload(files) {
  if (!files || !files.length || S.me.read_only) return;
  if (S.me.max_upload) {
    const big = [...files].filter(f => f.size > S.me.max_upload);
    if (big.length) { toast(`${big.length} file(s) exceed the ${fmtSize(S.me.max_upload)} upload limit and were skipped`, true); files = [...files].filter(f => f.size <= S.me.max_upload); if (!files.length) return; }
  }
  uploader.add(files, S.path);
}

// Folder size sheet: instant from the storage index, or a live scan of just that folder.
async function folderSize(rel) {
  const name = rel ? rel.split('/').pop() : 'Home (all files)';
  const body = h('div', { class: 'fsize' });
  let dlg;
  const show = async fresh => {
    const started = Date.now();
    const status = h('div', { class: 'fsize-scan' }, h('div', { class: 'spin' }),
      h('div', {}, h('b', {}, fresh ? 'Scanning this folder…' : 'Calculating…'), h('div', { class: 'small dim', id: 'fs-elapsed' }, 'Reading every file and subfolder')));
    if (fresh || !body.children.length) body.replaceChildren(status);
    const tick = setInterval(() => { const el = $('#fs-elapsed', status); if (el) el.textContent = `Reading every file and subfolder · ${Math.round((Date.now() - started) / 1000)} s`; }, 1000);
    let d;
    try { d = await api('GET', '/api/size?' + new URLSearchParams({ path: rel, ...(fresh ? { fresh: '1' } : {}) })); }
    catch (e) { clearInterval(tick); body.replaceChildren(h('div', { class: 'err', style: { padding: '12px 0' } }, e.message)); return; }
    clearInterval(tick);
    if (d.started) { // whole drive: hand over to the Storage view, which shows scan progress
      dlg.close(); toast('Full storage scan started'); location.hash = '#storage'; return;
    }
    render(d.result, d);
  };
  const render = (r, d) => {
    const total = r.size || 1;
    const bar = h('div', { class: 'fsize-bar' }, r.categories.map(c => h('i', { title: `${c.name}: ${fmtSize(c.bytes)}`, style: { width: (c.bytes / total * 100).toFixed(2) + '%', background: CAT_COLORS[c.name] } })));
    const when = r.source === 'scan'
      ? `Scanned just now${r.took_ms != null ? ` in ${r.took_ms < 1000 ? r.took_ms + ' ms' : (r.took_ms / 1000).toFixed(1) + ' s'}` : ''}`
      : d.live ? 'From the storage index · kept up to date live' : `From the storage index${r.scanned_at && new Date(r.scanned_at).getFullYear() > 1971 ? ' · ' + fmtAgo(r.scanned_at) : ''}`;
    body.replaceChildren(
      h('div', { class: 'fsize-hero' },
        h('div', { class: 'fsize-total' }, fmtSize(r.size)),
        h('div', { class: 'dim' }, `${r.files.toLocaleString()} file${r.files === 1 ? '' : 's'} · ${r.dirs.toLocaleString()} folder${r.dirs === 1 ? '' : 's'}`)),
      r.categories.length ? bar : null,
      r.categories.length ? h('div', { class: 'fsize-legend' }, r.categories.map(c => h('div', {},
        h('i', { style: { background: CAT_COLORS[c.name] } }), h('span', {}, c.name), h('b', {}, fmtSize(c.bytes)),
        h('span', { class: 'dim small' }, Math.round(c.bytes / total * 100) + '%')))) : null,
      r.largest?.length ? h('div', { class: 'fsize-largest' }, h('div', { class: 'group-h' }, 'Largest files'),
        h('div', { class: 'group' }, r.largest.map(f => h('div', { class: 'cell' }, fileThumb(f.path.split('/').pop(), false),
          h('div', { class: 'txt', title: '/' + f.path }, h('span', { class: 't1' }, f.path.split('/').pop()), h('span', { class: 't2' }, '/' + f.path.split('/').slice(0, -1).join('/'))),
          h('span', { class: 'fsize-num' }, fmtSize(f.size)))))) : null,
      h('div', { class: 'small dim fsize-when' }, when));
  };
  const actions = [{ label: 'Rescan', onclick: () => show(true) }];
  if (S.me.analyzer_on) actions.push({ label: 'Open in Storage', cls: 'primary', onclick: c => { c(); location.hash = '#storage/' + rel.split('/').map(encodeURIComponent).join('/'); } });
  else actions.push({ label: 'Close', cls: 'primary', onclick: c => c() });
  dlg = modal(name, body, { buttons: actions });
  show(false);
}

async function newFolder() {
  const name = await prompt2('New folder', null, '', { okLabel: 'Create' });
  if (!name) return;
  try { await task('Creating folder…', api('POST', '/api/mkdir', { path: S.path, name })); load(); } catch (e) { toast(e.message, true); }
}

async function newFile() {
  const name = await prompt2('New file', null, 'notes.txt', { okLabel: 'Create', selectBase: true });
  if (!name) return;
  try { const r = await task('Creating file…', api('POST', '/api/mkdir', { path: S.path, name, file: true })); await load(); openEntry({ name, size: 0 }, r.path); } catch (e) { toast(e.message, true); }
}

async function rename(full, name) {
  const nn = await prompt2('Rename', null, name, { okLabel: 'Rename', selectBase: true });
  if (!nn || nn === name) return;
  try { await task('Renaming…', api('POST', '/api/rename', { path: full, name: nn })); load(); } catch (e) { toast(e.message, true); }
}

async function del(paths) {
  const names = paths.map(p => p.split('/').pop());
  const msg = paths.length === 1 ? `Delete “${names[0]}”? This cannot be undone.` : `Delete ${paths.length} items? This cannot be undone.\n\n${names.slice(0, 8).join('\n')}${names.length > 8 ? '\n…' : ''}`;
  if (!await confirm2('Delete', msg)) return;
  try { await task(paths.length === 1 ? `Deleting “${names[0]}”…` : `Deleting ${paths.length} items…`, api('POST', '/api/delete', { paths })); toast('Deleted'); } catch (e) { toast(e.message, true); }
  load(); loadDisk();
}

async function paste() {
  const c = S.clipboard;
  try {
    const n = c.paths.length === 1 ? '“' + c.paths[0].split('/').pop() + '”' : c.paths.length + ' items';
    await task((c.op === 'move' ? 'Moving ' : 'Copying ') + n + '…', api('POST', c.op === 'move' ? '/api/move' : '/api/copy', { paths: c.paths, dest: S.path }));
    toast(c.op === 'move' ? 'Moved' : 'Copied');
  } catch (e) { toast(e.message, true); }
  S.clipboard = null; load(); loadDisk();
}

async function moveDialog(paths, copy = false) {
  let cur = '';
  const tree = h('div', { class: 'tree' });
  const label = h('div', { class: 'dim small' });
  const draw = async () => {
    label.textContent = 'Destination: /' + cur;
    tree.replaceChildren(skeletonRows(4));
    let d;
    try { d = await api('GET', '/api/list?path=' + encPath(cur)); } catch (e) { toast(e.message, true); return; }
    const kids = [];
    if (cur) kids.push(h('a', { href: '#', onclick: ev => { ev.preventDefault(); cur = cur.split('/').slice(0, -1).join('/'); draw(); } }, iconEl('folder', 'fi dir'), '..'));
    d.entries.filter(e => e.dir).forEach(e => {
      const p = joinPath(cur, e.name);
      if (paths.includes(p)) return;
      kids.push(h('a', { href: '#', onclick: ev => { ev.preventDefault(); cur = p; draw(); } }, iconEl('folder', 'fi dir'), e.name));
    });
    if (kids.length === 0) kids.push(h('div', { class: 'dim small', style: { padding: '10px' } }, 'No sub-folders'));
    tree.replaceChildren(...kids);
  };
  draw();
  modal(copy ? 'Copy to…' : 'Move to…', [label, tree], {
    buttons: [{ label: 'Cancel', onclick: c => c() },
    { label: 'Copy here', onclick: async c => { c(); try { await task(`Copying ${paths.length === 1 ? '“' + paths[0].split('/').pop() + '”' : paths.length + ' items'}…`, api('POST', '/api/copy', { paths, dest: cur })); toast('Copied'); } catch (e) { toast(e.message, true); } load(); } },
    { label: 'Move here', cls: 'primary', onclick: async c => { c(); try { await task(`Moving ${paths.length === 1 ? '“' + paths[0].split('/').pop() + '”' : paths.length + ' items'}…`, api('POST', '/api/move', { paths, dest: cur })); toast('Moved'); } catch (e) { toast(e.message, true); } load(); } }],
  });
}

function closeMenu() { $('.menu')?.remove(); $('.menu-bg')?.remove(); }

function contextMenu(ev, e, full) {
  closeMenu();
  // act on the whole selection only when right-clicking an item that is part of it
  const multi = S.sel.size > 1 && S.sel.has(e.name);
  const item = (ic, label, fn, cls = '') => h('button', { class: cls, onclick: ev2 => { ev2.stopPropagation(); closeMenu(); fn(); }, html: icon(ic) + `<span></span>` }, label);
  const items = multi ? [
    item('download', 'Download as zip', downloadSel),
    S.me.read_only ? null : item('folder', 'Move / copy to…', () => moveDialog([...S.sel].map(n => joinPath(S.path, n)))),
    S.me.read_only ? null : item('trash', `Delete ${S.sel.size} items`, () => del([...S.sel].map(n => joinPath(S.path, n))), 'danger'),
  ] : [
    !e.dir ? item(viewKind(e.name) === 'text' || viewKind(e.name) === 'md' ? 'edit' : 'eye', viewKind(e.name) === 'text' ? 'Open / edit' : 'Open', () => openEntry(e, full)) : null,
    e.dir ? item('folder', 'Open', () => openEntry(e, full)) : null,
    !e.dir ? h('button', { class: 'sub', onclick: ev2 => { ev2.stopPropagation(); m.querySelector('.submenu').classList.toggle('hidden'); }, html: icon('more') + '<span>Open with…</span>' }) : null,
    !e.dir ? h('div', { class: 'submenu hidden' }, OPEN_WITH.map(([k, ic, label]) => item(ic, label, () => openEntry(e, full, null, k)))) : null,
    item('download', e.dir ? 'Download as zip' : 'Download', () => {
      if (e.dir) { const dir = full.split('/').slice(0, -1).join('/'); location.href = '/api/zip?' + new URLSearchParams({ dir, names: e.name }); }
      else location.href = dlURL(full);
    }),
    e.dir ? item('chart', 'Folder size', () => folderSize(full)) : null,
    S.me.shares_on ? item('share', 'Share link…', () => shareDialog(full, e.dir)) : null,
    S.me.read_only ? null : item('edit', 'Rename', () => rename(full, e.name)),
    S.me.read_only ? null : item('folder', 'Move / copy to…', () => moveDialog([full])),
    S.me.read_only ? null : item('trash', 'Delete', () => del([full]), 'danger'),
  ];
  const m = h('div', { class: 'menu' }, h('div', { class: 'small dim hide-lg', style: { padding: '6px 14px 8px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } }, multi ? `${S.sel.size} items` : e.name), items);
  if (matchMedia('(max-width: 640px)').matches) document.body.append(h('div', { class: 'menu-bg', onclick: closeMenu }));
  document.body.append(m);
  const r = m.getBoundingClientRect();
  const x = ev.clientX || ev.target.getBoundingClientRect?.().left || 0;
  m.style.left = Math.max(8, Math.min(x, innerWidth - r.width - 8)) + 'px';
  m.style.top = Math.min(ev.clientY, innerHeight - r.height - 8) + 'px';
}

function onKey(e) {
  if ($('.modal-bg') || /INPUT|TEXTAREA/.test(document.activeElement?.tagName) || S.view !== 'files') return;
  if (S.me.read_only && (e.key === 'Delete' || e.key === 'F2')) return;
  if (e.key === 'Delete' && S.sel.size) del([...S.sel].map(n => joinPath(S.path, n)));
  else if ((e.ctrlKey || e.metaKey) && e.key === 'a') { e.preventDefault(); S.entries.forEach(x => S.sel.add(x.name)); render(); }
  else if (e.key === 'Escape') { S.sel.clear(); closeMenu(); render(); }
  else if (e.key === 'F2' && S.sel.size === 1) { const n = [...S.sel][0]; rename(joinPath(S.path, n), n); }
  else if (e.key === 'Backspace' && S.path) go(S.path.split('/').slice(0, -1).join('/'));
}

// ---------- sharing ----------
async function shareDialog(full, isDir) {
  const name = full ? full.split('/').pop() : 'Home (all files)';
  const existing = h('div', { class: 'list' });
  const drawExisting = async () => {
    const d = await api('GET', '/api/shares?path=' + encPath(full));
    existing.replaceChildren(...(d.shares.length ? [h('h4', {}, 'Existing links')] : []), ...d.shares.map(s => shareItem(s, drawExisting)));
  };
  drawExisting();
  const exp = h('select', { class: 'input' },
    [['0', 'Never expires'], ['1', '1 hour'], ['24', '1 day'], ['168', '7 days'], ['720', '30 days'], ['custom', 'Custom date…']].map(([v, l]) => h('option', { value: v }, l)));
  exp.value = '168';
  const custom = h('input', { class: 'input hidden', type: 'datetime-local' });
  exp.onchange = () => custom.classList.toggle('hidden', exp.value !== 'custom');
  const pw = h('input', { class: 'input', type: 'text', placeholder: 'Optional password', autocomplete: 'off' });
  const maxDl = h('input', { class: 'input', type: 'number', min: 0, placeholder: 'Max downloads (0 = unlimited)' });
  const note = h('textarea', { class: 'input', rows: 2, placeholder: 'Optional message shown to visitors' });
  const mode = h('select', { class: 'input' },
    h('option', { value: 'view' }, 'View & download only'),
    isDir && S.me.share_uploads && !S.me.read_only ? h('option', { value: 'upload' }, 'View, download & upload') : null,
    isDir && S.me.share_uploads && !S.me.read_only ? h('option', { value: 'drop' }, 'Upload only (file drop — visitors can\'t see files)') : null);
  const result = h('div');
  const create = async () => {
    let hours = Number(exp.value);
    if (exp.value === 'custom') {
      if (!custom.value) return toast('Pick a date', true);
      hours = (new Date(custom.value) - Date.now()) / 3600e3;
      if (hours <= 0) return toast('Date must be in the future', true);
    }
    try {
      const s = await api('POST', '/api/shares', {
        path: full, expires_hours: hours, password: pw.value, note: note.value,
        allow_upload: mode.value !== 'view', upload_only: mode.value === 'drop', max_downloads: Number(maxDl.value) || 0,
      });
      copyText(s.url);
      const det = h('details', { class: 'rc-details' }, h('summary', { html: icon('code') + '<span>Use with rclone / WebDAV</span>' }), rclonePanel(s.rclone));
      result.replaceChildren(h('div', { class: 'linkbox' }, h('input', { class: 'input', readonly: true, value: s.url, onclick: e => e.target.select() }),
        h('button', { class: 'btn primary', onclick: () => copyText(s.url), html: icon('copy') })), det);
      drawExisting(); load(true);
    } catch (e) { toast(e.message, true); }
  };
  modal('Share “' + name + '”', [
    h('div', { class: 'row wrap' }, h('div', { class: 'grow', style: { minWidth: '180px' } }, h('div', { class: 'small dim' }, 'Access'), mode)),
    h('div', { class: 'row wrap' },
      h('div', { class: 'grow', style: { minWidth: '160px' } }, h('div', { class: 'small dim' }, 'Expires'), exp, custom),
      h('div', { class: 'grow', style: { minWidth: '160px' } }, h('div', { class: 'small dim' }, 'Password'), pw)),
    !isDir ? h('div', {}, h('div', { class: 'small dim' }, 'Download limit'), maxDl) : null,
    note,
    h('button', { class: 'btn primary', style: { justifyContent: 'center' }, onclick: ev => busy(ev.currentTarget, create), html: icon('link') + 'Create link & copy' }),
    result, existing,
  ]);
}

function shareItem(s, refresh) {
  const flags = [];
  if (s.upload_only) flags.push('upload-only'); else if (s.allow_upload) flags.push('uploads allowed');
  if (s.has_password) flags.push('password');
  flags.push(s.expires && new Date(s.expires).getFullYear() > 1971 ? (s.expired ? 'expired ' : 'expires ') + new Date(s.expires).toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) : 'no expiry');
  flags.push(`${s.views} views`, `${s.downloads}${s.max_downloads ? '/' + s.max_downloads : ''} downloads`);
  if (s.missing) flags.push('⚠ target missing');
  return h('div', { class: 'item' },
    fileIcon(s.path.split('/').pop() || 'home', s.is_dir),
    h('div', { class: 'main', title: '/' + s.path },
      h('div', {}, h('span', { class: 't' }, s.path.split('/').pop() || 'Home (all files)')),
      h('div', { class: 'small dim' }, '/' + s.path.split('/').slice(0, -1).join('/')),
      h('div', { class: 'small dim wrapline' }, flags.join(' · '))),
    h('button', { class: 'btn ghost', title: 'Copy link', html: icon('copy'), onclick: () => copyText(s.url) }),
    h('button', { class: 'btn ghost', title: 'rclone / WebDAV config', html: icon('code'), onclick: ev => busy(ev.currentTarget, async () => {
      try { const rc = await api('GET', '/api/shares/' + s.id + '/rclone'); modal('rclone / WebDAV', rclonePanel(rc), { cls: 'wide' }); }
      catch (e) { toast(e.message, true); }
    }) }),
    h('a', { class: 'btn ghost', title: 'Open', href: s.url, target: '_blank', rel: 'noopener', html: icon('eye') }),
    h('button', { class: 'btn ghost danger', title: 'Revoke', html: icon('trash'), onclick: async ev => {
      const btn = ev.currentTarget;
      if (!await confirm2('Revoke link', 'Anyone with this link will lose access.', 'Revoke')) return;
      await busy(btn, async () => { try { await api('DELETE', '/api/shares/' + s.id); await refresh(); load(true); } catch (e) { toast(e.message, true); } });
    } }));
}

function showShares() { location.hash = '#shares'; }

async function showSharesPage() {
  if (S.view !== 'shares') return;
  setNav('Shared Links', null, []);
  const sub = h('p', { class: 'subtitle' }, 'Loading…');
  const box = h('div', {}, skeletonRows(4, 'group'));
  setMain(h('h1', { class: 'large-title' }, 'Shared Links'), sub, box);
  const draw = async () => {
    let d;
    try { d = await api('GET', '/api/shares'); }
    catch (e) { if (S.view === 'shares') box.replaceChildren(h('div', { class: 'dim' }, e.message)); return; }
    if (S.view !== 'shares') return;
    const active = d.shares.filter(x => !x.expired), expired = d.shares.filter(x => x.expired);
    sub.textContent = d.shares.length ? `${active.length} active${expired.length ? ` · ${expired.length} expired` : ''}` : 'No links yet';
    if (!d.shares.length) {
      box.replaceChildren(h('div', { class: 'empty', html: icon('link') + '<p>No shared links yet.<br>In Files, open a file or folder’s menu and choose “Share link…”.</p>' }));
      return;
    }
    const section = (title, list) => list.length ? [h('div', { class: 'group-h' }, title), h('div', { class: 'group share-group' }, list.map(x => shareRow(x, draw)))] : [];
    box.replaceChildren(...section('Active', active), ...section('Expired', expired));
    setTimeout(restoreScroll, 0);
  };
  await draw();
}

// one shared link as a grouped-list row (same look as file rows)
function shareRow(s, refresh) {
  const name = s.path.split('/').pop() || 'Home (all files)';
  const bits = [];
  if (s.upload_only) bits.push('Upload only'); else if (s.allow_upload) bits.push('Uploads allowed');
  if (s.has_password) bits.push('Password');
  bits.push(s.expires && new Date(s.expires).getFullYear() > 1971 ? (s.expired ? 'Expired ' : 'Expires ') + new Date(s.expires).toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) : 'No expiry');
  bits.push(`${s.views} view${s.views === 1 ? '' : 's'}`, `${s.downloads}${s.max_downloads ? '/' + s.max_downloads : ''} download${s.downloads === 1 ? '' : 's'}`);
  if (s.missing) bits.push('⚠ target missing');
  const openTarget = () => { location.hash = '#/' + (s.is_dir ? s.path : s.path.split('/').slice(0, -1).join('/')).split('/').filter(Boolean).map(encodeURIComponent).join('/'); };
  return h('div', { class: 'cell share-cell' + (s.expired ? ' expired' : '') },
    fileThumb(name, s.is_dir),
    h('div', { class: 'txt', title: '/' + s.path },
      h('span', { class: 't1' }, name),
      h('span', { class: 't2' }, '/' + s.path.split('/').slice(0, -1).join('/')),
      h('span', { class: 't2 share-bits' }, bits.join(' · '))),
    h('div', { class: 'share-actions' },
      h('button', { class: 'btn ghost', title: 'Copy link', 'aria-label': 'Copy link', html: icon('copy'), onclick: () => copyText(s.url) }),
      h('a', { class: 'btn ghost', title: 'Open link', 'aria-label': 'Open link', href: s.url, target: '_blank', rel: 'noopener', html: icon('eye') }),
      h('button', { class: 'btn ghost more', title: 'More', 'aria-label': 'More actions', html: icon('more'), onclick: ev => {
        ev.stopPropagation(); closeMenu();
        const item = (ic, label, fn, cls = '') => h('button', { class: cls, onclick: e2 => { e2.stopPropagation(); closeMenu(); fn(); }, html: icon(ic) + '<span></span>' }, label);
        const m = h('div', { class: 'menu' },
          h('div', { class: 'small dim hide-lg', style: { padding: '6px 14px 8px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } }, name),
          item('copy', 'Copy link', () => copyText(s.url)),
          item('code', 'rclone / WebDAV config', async () => {
            try { const rc = await task('Loading…', api('GET', '/api/shares/' + s.id + '/rclone')); modal('rclone / WebDAV', rclonePanel(rc), { cls: 'wide' }); }
            catch (e) { toast(e.message, true); }
          }),
          s.missing ? null : item('folder', s.is_dir ? 'Show folder in Files' : 'Show in Files', openTarget),
          item('trash', 'Revoke link', async () => {
            if (!await confirm2('Revoke link', `Anyone with the link to “${name}” will lose access.`, 'Revoke')) return;
            try { await task('Revoking…', api('DELETE', '/api/shares/' + s.id)); toast('Link revoked'); await refresh(); } catch (e) { toast(e.message, true); }
          }, 'danger'));
        if (matchMedia('(max-width: 640px)').matches) document.body.append(h('div', { class: 'menu-bg', onclick: closeMenu }));
        document.body.append(m);
        const r = ev.currentTarget.getBoundingClientRect(), mr = m.getBoundingClientRect();
        m.style.left = Math.max(8, Math.min(r.right - mr.width, innerWidth - mr.width - 8)) + 'px';
        m.style.top = Math.min(r.bottom + 6, innerHeight - mr.height - 8) + 'px';
      } })));
}

// ---------- settings ----------
function uaLabel(ua) {
  const b = /Edg\//.test(ua) ? 'Edge' : /OPR\//.test(ua) ? 'Opera' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : /curl/i.test(ua) ? 'curl' : 'Browser';
  const os = /iPhone|iPad/.test(ua) ? 'iOS' : /Android/.test(ua) ? 'Android' : /Windows/.test(ua) ? 'Windows' : /Mac OS/.test(ua) ? 'macOS' : /Linux/.test(ua) ? 'Linux' : '';
  return os ? `${b} on ${os}` : b;
}

// ---------- settings (iOS-style: segmented tabs, inset grouped rows) ----------
const grp = (title, rows, foot) => h('section', { class: 'sg' },
  title ? h('div', { class: 'sg-h' }, title) : null,
  h('div', { class: 'sg-box' }, rows.filter(Boolean)),
  foot ? h('div', { class: 'sg-f' }, foot) : null);
const srow = (label, control, opts = {}) => h(opts.tag || 'div', { class: 'sg-row' + (opts.stack ? ' stack' : '') + (opts.cls ? ' ' + opts.cls : ''), onclick: opts.onclick },
  opts.icon ? h('span', { class: 'sg-ic', style: { background: opts.color || 'var(--acc)' }, html: icon(opts.icon) }) : null,
  h('div', { class: 'sg-l' }, h('span', {}, label), opts.sub ? h('small', {}, opts.sub) : null),
  control || null);
const banner = (kind, text, extra) => h('div', { class: 'banner ' + kind }, h('span', { class: 'bi', html: icon(kind === 'ok' ? 'check' : kind === 'warn' ? 'x' : 'eye') }), h('div', {}, text, extra || null));

// Settings and Shared Links are pages (like Files/Storage), not dialogs.
function showSettings(focus) { location.hash = '#settings/' + (focus === 'movies' && S.me.movies_on ? 'movies' : 'general'); }

function showSettingsPage(tab) {
  if (S.view !== 'settings') return;
  if (tab === 'movies' && !S.me.movies_on) tab = 'general';
  setNav('Settings', null, []);
  const tabs = [{ id: 'general', label: 'General' }];
  if (S.me.movies_on) tabs.push({ id: 'movies', label: 'Movies' });
  const seg = tabs.length > 1 ? h('div', { class: 'seg seg-full page-seg' }, tabs.map(t => h('button', { class: t.id === tab ? 'on' : null, onclick: () => { location.hash = '#settings/' + t.id; } }, t.label))) : null;
  setMain(h('h1', { class: 'large-title' }, 'Settings'),
    h('p', { class: 'subtitle' }, tab === 'movies' ? 'Movie details, subtitles and library folder' : 'Sign-in, passcode and devices'),
    seg, h('div', { class: 'settings page-settings' }, tab === 'movies' ? movieSettings() : generalSettings()));
  setTimeout(restoreScroll, 0);
}

function generalSettings() {
  const pkList = h('div', { class: 'sg-box' });
  const sessList = h('div', { class: 'sg-box' });
  const pkOnly = h('input', { type: 'checkbox', 'aria-label': 'Passkey only' });
  const drawPk = async () => {
    const d = await api('GET', '/api/passkeys');
    S.me = await api('GET', '/api/me');
    pkOnly.checked = S.me.passkey_only;
    pkOnly.disabled = !d.passkeys.length;
    pkList.replaceChildren(
      ...d.passkeys.map(p => srow(p.name, h('button', { class: 'btn ghost danger sm', title: 'Remove', html: icon('trash'), onclick: async ev => {
        const btn = ev.currentTarget;
        if (!await confirm2('Remove passkey', `Remove “${p.name}”?`, 'Remove')) return;
        await busy(btn, async () => { try { await api('DELETE', '/api/passkeys/' + p.id); await drawPk(); } catch (e) { toast(e.message, true); } });
      } }), { icon: 'key', color: 'var(--indigo)', sub: 'Added ' + fmtDate(p.created) + (new Date(p.last_used).getFullYear() > 1971 ? ' · used ' + fmtDate(p.last_used) : '') })),
      srow(h('span', { class: 'link' }, 'Add Passkey'), null, { icon: 'plus', color: 'var(--green)', cls: 'tap', onclick: addPk }),
      h('label', { class: 'sg-row' }, h('div', { class: 'sg-l' }, h('span', {}, 'Passkey only'), h('small', {}, 'Turns off the passcode fallback')), h('span', { class: 'chk' }, pkOnly)));
  };
  const drawSess = async () => {
    const d = await api('GET', '/api/sessions');
    sessList.replaceChildren(...d.sessions.sort((a, b) => b.current - a.current || new Date(b.created) - new Date(a.created)).map(s =>
      srow(uaLabel(s.ua), s.current ? h('span', { class: 'badge share' }, 'This device') :
        h('button', { class: 'btn ghost danger sm', title: 'Log out this device', html: icon('logout'), onclick: ev => busy(ev.currentTarget, async () => { try { await api('DELETE', '/api/sessions/' + s.id); await drawSess(); } catch (e) { toast(e.message, true); } }) }),
        { icon: s.current ? 'check' : 'lock', color: s.current ? 'var(--green)' : 'var(--gray)', sub: `${s.ip} · ${s.method} · ${fmtDate(s.created)}` })),
      srow(h('span', { class: 'link danger' }, 'Log Out Other Devices'), null, { cls: 'tap', onclick: ev => busy(ev.currentTarget.querySelector('.link') || ev.currentTarget, async () => { try { await api('DELETE', '/api/sessions/others'); await drawSess(); toast('Other devices logged out'); } catch (e) { toast(e.message, true); } }) }));
  };
  pkOnly.onchange = async () => {
    pkOnly.disabled = true;
    try { await api('POST', '/api/settings/passkey-only', { enabled: pkOnly.checked }); toast(pkOnly.checked ? 'Passcode fallback off' : 'Passcode fallback on'); }
    catch (e) { toast(e.message, true); pkOnly.checked = !pkOnly.checked; }
    finally { pkOnly.disabled = false; }
  };
  const addPk = async () => {
    if (!window.PublicKeyCredential) return toast('Passkeys need HTTPS and a modern browser', true);
    const name = await prompt2('Name this passkey', 'e.g. “Phone”, “Laptop”, “YubiKey”', guessDevice(), { okLabel: 'Continue' });
    if (name == null) return;
    try {
      const o = await api('GET', '/api/passkey/register/begin');
      const cred = await navigator.credentials.create({ publicKey: { ...o, challenge: b64uToBuf(o.challenge), user: { ...o.user, id: b64uToBuf(o.user.id) }, excludeCredentials: (o.excludeCredentials || []).map(c => ({ ...c, id: b64uToBuf(c.id) })) } });
      await api('POST', '/api/passkey/register/finish', { name, clientDataJSON: bufToB64u(cred.response.clientDataJSON), attestationObject: bufToB64u(cred.response.attestationObject) });
      toast('Passkey added'); drawPk();
    } catch (e) { toast(e.message, true); }
  };
  pkList.replaceChildren(skeletonRows(2)); sessList.replaceChildren(skeletonRows(2));
  if (S.me.passkeys_on) drawPk().catch(e => toast(e.message, true));
  drawSess().catch(e => toast(e.message, true));
  const curF = passwordField({ placeholder: 'Required', autocomplete: 'current-password', 'aria-label': 'Current passcode' });
  const nwF = passwordField({ placeholder: 'At least 4 characters', autocomplete: 'new-password', 'aria-label': 'New passcode' });
  const change = h('button', { class: 'btn primary', onclick: ev => busy(ev.currentTarget, async () => {
    try { await api('POST', '/api/settings/passcode', { current: curF.input.value, new: nwF.input.value }); toast('Passcode changed'); curF.input.value = nwF.input.value = ''; drawSess(); }
    catch (e) { toast(e.message, true); }
  }) }, 'Change Passcode');
  return h('div', { class: 'sg-wrap' },
    S.me.passkeys_on ? h('section', { class: 'sg' }, h('div', { class: 'sg-h' }, 'Passkeys'), pkList, h('div', { class: 'sg-f' }, 'Face ID, fingerprint, Windows Hello or a security key.')) : null,
    grp('Passcode', [srow('Current', curF.el, { stack: true }), srow('New', nwF.el, { stack: true })], 'Changing it logs out all other devices.'),
    h('div', { class: 'sg-actions' }, change),
    h('section', { class: 'sg' }, h('div', { class: 'sg-h' }, 'Devices'), sessList),
    h('div', { class: 'sg-ver' }, `filenav ${S.me.version}`));
}

// Movies: TMDB + OpenSubtitles credentials. Secrets are write-only.
function movieSettings() {
  const tmdb = passwordField({ placeholder: 'Not set', autocomplete: 'off', 'aria-label': 'TMDB API key' });
  const osk = passwordField({ placeholder: 'Not set', autocomplete: 'off', 'aria-label': 'OpenSubtitles API key' });
  const osp = passwordField({ placeholder: 'Not set', autocomplete: 'off', 'aria-label': 'OpenSubtitles password' });
  const osu = h('input', { class: 'input', placeholder: 'Username', autocomplete: 'off', autocapitalize: 'off', 'aria-label': 'OpenSubtitles username' });
  const dir = h('input', { class: 'input', placeholder: 'movies', autocapitalize: 'off', 'aria-label': 'Movies folder' });
  const langs = h('input', { class: 'input', placeholder: 'en', autocapitalize: 'off', 'aria-label': 'Subtitle language', maxlength: 8 });
  const tmdbState = h('span', { class: 'pill-s' }), osState = h('span', { class: 'pill-s' });
  const results = h('div', { class: 'sg-results hidden' });
  const setState = (el, ok) => { el.className = 'pill-s ' + (ok ? 'ok' : ''); el.textContent = ok ? 'Saved' : 'Not set'; };
  const fill = d => {
    setState(tmdbState, d.tmdb_key_set); setState(osState, d.os_key_set && d.os_pass_set && d.os_user);
    tmdb.input.placeholder = d.tmdb_key_set ? '••••••••  saved' : 'Paste API key';
    osk.input.placeholder = d.os_key_set ? '••••••••  saved' : 'Paste API key';
    osp.input.placeholder = d.os_pass_set ? '••••••••  saved' : 'Password';
    osu.value = d.os_user || ''; dir.value = d.movies_dir || 'movies'; langs.value = d.sub_langs || 'en';
  };
  const fields = [tmdb.input, osk.input, osp.input, osu, dir, langs];
  fields.forEach(f => { f.disabled = true; });
  api('GET', '/api/movies/settings').then(fill).catch(e => toast(e.message, true)).finally(() => fields.forEach(f => { f.disabled = false; }));
  const save = async (quiet) => {
    const body = { os_user: osu.value, movies_dir: dir.value || 'movies', sub_langs: langs.value || 'en' };
    if (tmdb.input.value) body.tmdb_key = tmdb.input.value;
    if (osk.input.value) body.os_key = osk.input.value;
    if (osp.input.value) body.os_pass = osp.input.value;
    try {
      const d = await api('PUT', '/api/movies/settings', body);
      tmdb.input.value = osk.input.value = osp.input.value = '';
      fill(d);
      if (!quiet) toast('Saved');
      return true;
    } catch (e) { toast(e.message, true); return false; }
  };
  const resultRow = (name, ok, detail) => h('div', { class: 'res-row' },
    h('span', { class: 'dot ' + (ok === true ? 'ok' : ok === false ? 'bad' : 'warn') }),
    h('div', { class: 'res-t' }, h('b', {}, name), h('small', {}, detail)));
  const test = async () => {
    testBtn.disabled = true; testBtn.textContent = 'Testing…';
    results.classList.remove('hidden'); results.replaceChildren(h('div', { class: 'res-row' }, h('div', { class: 'progress-line', style: { flex: 1 } })));
    try {
      if (!await save(true)) return;
      const r = await api('POST', '/api/movies/test');
      const os = r.opensubtitles;
      results.replaceChildren(
        resultRow('TMDB', r.tmdb === 'ok' ? true : r.tmdb === 'not set' ? null : false, r.tmdb === 'ok' ? 'Connected' : r.tmdb === 'not set' ? 'No key yet' : r.tmdb),
        resultRow('OpenSubtitles', os === 'ok' ? true : os === 'not set' ? null : false,
          os === 'ok' ? (r.os_info || 'Connected').replace(/^(\w+) account · /, (m, k) => `Connected · ${k} · `) : os === 'not set' ? 'No key yet' : os));
    } catch (e) { results.replaceChildren(resultRow('Test', false, e.message)); }
    finally { testBtn.disabled = false; testBtn.textContent = 'Test Connection'; }
  };
  const testBtn = h('button', { class: 'btn', onclick: test }, 'Test Connection');
  return h('div', { class: 'sg-wrap' },
    banner('info', 'Keys are stored on your server only. They are never shown again after saving.'),
    grp(h('span', {}, 'TMDB ', tmdbState), [srow('API Key', tmdb.el, { stack: true })],
      h('span', {}, 'Posters and movie details. ', h('a', { href: 'https://www.themoviedb.org/settings/api', target: '_blank', rel: 'noopener' }, 'Get a key'))),
    grp(h('span', {}, 'OpenSubtitles ', osState), [srow('API Key', osk.el, { stack: true }), srow('Username', osu, { stack: true }), srow('Password', osp.el, { stack: true })],
      h('span', {}, 'Free accounts get about 20 subtitles a day; the rest are fetched on later scans. ', h('a', { href: 'https://www.opensubtitles.com/consumers', target: '_blank', rel: 'noopener' }, 'Get a key'))),
    grp('Library', [srow('Movies folder', dir, { cls: 'inline' }), srow('Subtitle language', langs, { cls: 'inline' })], 'Folder inside your files. Language as a 2-letter code (en, es, fr…).'),
    h('div', { class: 'sg-actions' }, h('button', { class: 'btn primary', onclick: ev => busy(ev.currentTarget, () => save(false)) }, 'Save'), testBtn),
    results);
}

function guessDevice() {
  const ua = navigator.userAgent;
  if (/iPhone/.test(ua)) return 'iPhone';
  if (/iPad/.test(ua)) return 'iPad';
  if (/Android/.test(ua)) return 'Android';
  if (/Mac/.test(ua)) return 'Mac';
  if (/Windows/.test(ua)) return 'Windows PC';
  if (/Linux/.test(ua)) return 'Linux';
  return 'Passkey';
}

boot();

// PWA: register the service worker (app shell only; needs HTTPS or localhost)
if ('serviceWorker' in navigator && (location.protocol === 'https:' || location.hostname === 'localhost' || location.hostname === '127.0.0.1')) {
  addEventListener('load', () => navigator.serviceWorker.register('/sw.js').catch(() => { }));
}
