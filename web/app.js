// Admin UI
'use strict';

const S = {
  me: null,
  path: '',
  entries: [],
  sel: new Set(),
  sort: { key: 'name', dir: 1 },
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
  const passForm = h('form', {
    class: 'pass-form' + (hasPasskeys ? ' hidden' : ''),
    onsubmit: async e => {
      e.preventDefault(); err.textContent = '';
      try { await api('POST', '/api/login', { passcode: pass.value }); location.reload(); }
      catch (x) { err.textContent = x.message; pass.select(); }
    }
  }, pf.el, h('button', { class: 'btn primary', type: 'submit', style: { justifyContent: 'center' } }, 'Unlock'));
  const showPass = () => { passForm.classList.remove('hidden'); fallback.classList.add('hidden'); pass.focus(); };
  const fallback = h('button', { class: 'btn ghost small', type: 'button', style: { justifyContent: 'center' }, onclick: showPass }, 'Use passcode instead');
  const pkBtn = h('button', { class: 'btn primary', type: 'button', style: { justifyContent: 'center' }, onclick: () => passkeyLogin(err, false, onPkFail), html: icon('key') + 'Sign in with passkey' });
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
  const goFiles = () => { S.search = null; searchInput.value = ''; location.hash.startsWith('#/') ? go('') : (location.hash = '#/'); };
  const goStorage = () => { location.hash = '#storage'; };
  const logout = async () => { await api('POST', '/api/logout'); location.reload(); };
  $('#root').replaceChildren(h('div', { class: 'app' },
    h('aside', { class: 'sidebar' },
      h('a', { class: 'brand', href: '#/', onclick: e => { e.preventDefault(); goFiles(); } }, iconEl('logo', 'logo'), h('span', {}, S.me.title)),
      h('div', { class: 'side-h' }, 'Library'),
      nav('files', 'folder', 'Files', goFiles),
      S.me.analyzer_on ? nav('storage', 'chart', 'Storage', goStorage) : null,
      S.me.shares_on ? nav('shares', 'link', 'Shared Links', showShares) : null,
      h('div', { class: 'side-h' }, 'Account'),
      S.me.auth ? nav('settings', 'gear', 'Settings', showSettings) : null,
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
      S.me.analyzer_on ? tab('storage', 'chart', 'Storage', goStorage) : null,
      S.me.shares_on ? tab('shares', 'link', 'Shared', showShares) : null,
      S.me.auth ? tab('settings', 'gear', 'Settings', showSettings) : null)));

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
function route() {
  const hsh = location.hash;
  const view = hsh.startsWith('#storage') ? 'storage' : 'files';
  S.view = view;
  $$('[data-view]').forEach(b => b.classList.toggle('active', b.dataset.view === view));
  $('#navbar').classList.remove('scrolled');
  $('#main').scrollTop = 0;
  if (view === 'storage') { S.sel.clear(); S.clipboard = null; renderSelbar(); return showStorage(decodeURIComponent(hsh.replace(/^#storage\/?/, ''))); }
  const p = pathFromHash();
  if (p !== S.path || !S.loaded) { S.search = null; if (S.searchInput) S.searchInput.value = ''; }
  load();
}

const pathFromHash = () => decodeURIComponent(location.hash.replace(/^#\/?/, '')).replace(/\/+$/, '');
function go(p) { location.hash = '#/' + p.split('/').map(encodeURIComponent).join('/'); }

async function load(keepSel) {
  if (S.view === 'storage') { setTimeout(route, 900); return; } // analyzer applies changes within ~1 s
  S.path = pathFromHash();
  if (!keepSel) S.sel.clear();
  try {
    const [d, sh] = await Promise.all([api('GET', '/api/list?path=' + encPath(S.path)), S.me.shares_on ? api('GET', '/api/shares') : { shares: [] }]);
    S.entries = d.entries; S.shares = sh.shares; S.loaded = true;
    for (const n of [...S.sel]) if (!S.entries.some(e => e.name === n)) S.sel.delete(n);
  } catch (e) {
    if (e.status === 401) return location.reload();
    toast(e.message, true);
    if (e.status === 404 && S.path) return go(S.path.split('/').slice(0, -1).join('/'));
    S.entries = [];
  }
  render();
}

async function loadDisk() {
  try {
    const d = await api('GET', '/api/disk');
    $('#disk').textContent = `${fmtSize(d.free)} available of ${fmtSize(d.total)}`;
    const used = 1 - d.free / d.total;
    $('#disk-meter').replaceChildren(h('i', { style: { width: (used * 100).toFixed(1) + '%', background: used > .9 ? 'var(--red)' : used > .75 ? 'var(--orange)' : 'var(--acc)' } }));
  } catch { }
}

async function doSearch(q) {
  try {
    const r = await api('GET', `/api/search?path=${encPath(S.path)}&q=${encodeURIComponent(q)}`);
    S.search = { q, results: r.results, truncated: r.truncated };
    S.sel.clear();
    render();
  } catch (e) { toast(e.message, true); }
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
    main.replaceChildren(...head, search, renderSearch());
    renderCrumbs();
    return;
  }
  const list = [...S.entries].sort((a, b) => {
    if (a.dir !== b.dir) return a.dir ? -1 : 1;
    const k = S.sort.key;
    const r = k === 'name' ? a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' })
      : k === 'size' ? a.size - b.size : new Date(a.mtime) - new Date(b.mtime);
    return r * S.sort.dir;
  });
  const nd = list.filter(e => e.dir).length, nf = list.length - nd;
  const sub = h('p', { class: 'subtitle' }, [nd ? `${nd} folder${nd > 1 ? 's' : ''}` : '', nf ? `${nf} file${nf > 1 ? 's' : ''}` : ''].filter(Boolean).join(', ') || 'Empty folder');
  if (!list.length) {
    main.replaceChildren(...head, sub, h('div', { class: 'empty', html: icon('folder') + '<p>This folder is empty.<br>Drop files here or tap Upload.</p>' }));
    renderCrumbs();
    return;
  }
  const sharedPaths = new Set(S.shares.filter(s => !s.expired).map(s => s.path));
  const allSel = list.every(e => S.sel.has(e.name));
  const sortBtn = (key, label) => h('button', { class: S.sort.key === key ? 'on' : null, onclick: () => { S.sort = { key, dir: S.sort.key === key ? -S.sort.dir : (key === 'name' ? 1 : -1) }; render(); } },
    label + (S.sort.key === key ? (S.sort.dir > 0 ? ' ↑' : ' ↓') : ''));
  main.replaceChildren(...head, sub, search,
    h('div', { class: 'group-h' },
      h('input', { type: 'checkbox', class: 'sel-circle', title: 'Select all', checked: allSel, onclick: e => { list.forEach(x => e.target.checked ? S.sel.add(x.name) : S.sel.delete(x.name)); render(); } }),
      h('span', { class: 'grow' }), 'Sort:', sortBtn('name', 'Name'), sortBtn('size', 'Size'), sortBtn('mtime', 'Date')),
    h('div', { class: 'group' }, list.map(e => row(e, joinPath(S.path, e.name), sharedPaths))));
  renderCrumbs();
}

function row(e, full, sharedPaths, extraLine) {
  const sel = S.sel.has(e.name);
  const cell = h('div', {
    class: 'cell' + (sel ? ' sel' : ''),
    oncontextmenu: ev => { ev.preventDefault(); contextMenu(ev, e, full); },
  },
    h('a', { class: 'open', href: e.dir ? '#/' + full.split('/').map(encodeURIComponent).join('/') : dlURL(full), title: e.name, 'aria-label': e.name,
      onclick: ev => { ev.preventDefault(); if (S.sel.size && !e.dir) { toggleSel(e.name); return; } openEntry(e, full, cell._list); } }),
    extraLine ? null : h('input', { type: 'checkbox', class: 'sel-circle', checked: sel, 'aria-label': 'Select ' + e.name, onclick: ev => { ev.stopPropagation(); toggleSel(e.name); } }),
    fileThumb(e.name, e.dir),
    h('div', { class: 'txt' },
      h('span', { class: 't1' }, e.name,
        sharedPaths && sharedPaths.has(full) ? h('span', { class: 'badge share' }, 'Shared') : null,
        e.link ? h('span', { class: 'badge' }, 'Link') : null),
      h('span', { class: 't2' }, extraLine || [e.dir ? 'Folder' : fmtSize(e.size), fmtDate(e.mtime)].filter(Boolean).join(' · '))),
    h('button', { class: 'btn ghost more', 'aria-label': 'More actions', html: icon('more'), onclick: ev => { ev.stopPropagation(); contextMenu(ev, e, full); } }),
    e.dir ? h('span', { class: 'acc', html: icon('right') }) : null);
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
const CAT_COLORS = { Videos: 'var(--pink)', Images: 'var(--green)', Audio: 'var(--purple)', Documents: 'var(--teal)', Archives: 'var(--orange)', Code: 'var(--indigo)', Other: 'var(--gray)' };
let storTimer;

async function showStorage(rel) {
  clearTimeout(storTimer);
  if (S.view !== 'storage') return;
  let d;
  try { d = await api('GET', '/api/analyze?path=' + encPath(rel)); }
  catch (e) { if (e.status === 401) return location.reload(); toast(e.message, true); return; }
  if (S.view !== 'storage' || location.hash.replace(/^#storage\/?/, '') !== encodeURIComponent(rel).replace(/%2F/g, '/') && decodeURIComponent(location.hash.replace(/^#storage\/?/, '')) !== rel) return;
  const parts = rel ? rel.split('/') : [];
  const title = parts.length ? parts[parts.length - 1] : 'Storage';
  const goStor = p => { location.hash = '#storage' + (p ? '/' + p.split('/').map(encodeURIComponent).join('/') : ''); };
  setNav(title, parts.length ? (parts.length > 1 ? parts[parts.length - 2] : 'Storage') : null, [
    d.scanning
      ? h('button', { class: 'btn danger', title: 'Stop the running scan', onclick: () => cancelScan(rel), html: icon('x') + '<span class="lbl hide-sm">Stop Scan</span>' })
      : h('button', { class: 'btn', title: 'Full rescan (normally not needed: the index updates itself)', onclick: () => confirmRescan(rel, d), html: icon('refresh') + '<span class="lbl hide-sm">Rescan</span>' }),
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
    main.replaceChildren(...head, h('div', { class: 'stor-hero' }, h('div', { style: { gridColumn: '1 / -1' } },
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
    h('div', { class: 'c' }, h('b', {}, fmtSize(d.size)), h('span', {}, isRoot && d.disk_total ? `of ${fmtSize(d.disk_total)} disk` : `${d.files.toLocaleString()} files`)));
  const legend = h('div', { class: 'stor-legend' },
    cats.map(c => h('div', {}, h('i', { style: { background: CAT_COLORS[c.name] } }), c.name, h('span', {}, fmtSize(c.bytes)))),
    isRoot && d.disk_free != null ? h('div', {}, h('i', { style: { background: 'var(--fill2)' } }), 'Free', h('span', {}, fmtSize(d.disk_free))) : null);
  const ago = d.last_scan ? fmtAgo(d.last_scan) : '';
  const meta = h('div', { class: 'stor-meta' },
    d.scanning ? h('span', {}, `${d.scan_mode === 'full' ? 'Rescanning' : 'Checking for changes'}… ${d.progress.toLocaleString()} folders · showing previous results`) :
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
  main.replaceChildren(...head,
    h('div', { class: 'stor-hero' }, ring, legend, meta),
    entries.length ? h('div', { class: 'group-h' }, 'By size') : null,
    entries.length ? h('div', { class: 'group' }, entries) : h('div', { class: 'empty', html: icon('folder') + '<p>Nothing here.</p>' }),
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
  try { await api('POST', '/api/analyze/rescan'); } catch (e) { toast(e.message, true); }
  showStorage(rel);
}

async function cancelScan(rel) {
  if (!await confirm2('Stop the scan?', 'The scan stops now. The previous results are kept.', 'Stop Scan', 'danger', 'Keep Scanning')) return;
  try { await api('POST', '/api/analyze/cancel'); toast('Scan stopped'); }
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
    name: e.name, size: e.size, raw: dlURL(full, true), dl: dlURL(full), text: '/api/text?path=' + encPath(full),
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

async function newFolder() {
  const name = await prompt2('New folder', null, '', { okLabel: 'Create' });
  if (!name) return;
  try { await api('POST', '/api/mkdir', { path: S.path, name }); load(); } catch (e) { toast(e.message, true); }
}

async function newFile() {
  const name = await prompt2('New file', null, 'notes.txt', { okLabel: 'Create', selectBase: true });
  if (!name) return;
  try { const r = await api('POST', '/api/mkdir', { path: S.path, name, file: true }); await load(); openEntry({ name, size: 0 }, r.path); } catch (e) { toast(e.message, true); }
}

async function rename(full, name) {
  const nn = await prompt2('Rename', null, name, { okLabel: 'Rename', selectBase: true });
  if (!nn || nn === name) return;
  try { await api('POST', '/api/rename', { path: full, name: nn }); load(); } catch (e) { toast(e.message, true); }
}

async function del(paths) {
  const names = paths.map(p => p.split('/').pop());
  const msg = paths.length === 1 ? `Delete “${names[0]}”? This cannot be undone.` : `Delete ${paths.length} items? This cannot be undone.\n\n${names.slice(0, 8).join('\n')}${names.length > 8 ? '\n…' : ''}`;
  if (!await confirm2('Delete', msg)) return;
  try { await api('POST', '/api/delete', { paths }); toast('Deleted'); } catch (e) { toast(e.message, true); }
  load(); loadDisk();
}

async function paste() {
  const c = S.clipboard;
  try {
    await api('POST', c.op === 'move' ? '/api/move' : '/api/copy', { paths: c.paths, dest: S.path });
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
    { label: 'Copy here', onclick: async c => { c(); try { await api('POST', '/api/copy', { paths, dest: cur }); toast('Copied'); } catch (e) { toast(e.message, true); } load(); } },
    { label: 'Move here', cls: 'primary', onclick: async c => { c(); try { await api('POST', '/api/move', { paths, dest: cur }); toast('Moved'); } catch (e) { toast(e.message, true); } load(); } }],
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
      result.replaceChildren(h('div', { class: 'linkbox' }, h('input', { class: 'input', readonly: true, value: s.url, onclick: e => e.target.select() }),
        h('button', { class: 'btn primary', onclick: () => copyText(s.url), html: icon('copy') })));
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
    h('button', { class: 'btn primary', style: { justifyContent: 'center' }, onclick: create, html: icon('link') + 'Create link & copy' }),
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
    h('a', { class: 'btn ghost', title: 'Open', href: s.url, target: '_blank', rel: 'noopener', html: icon('eye') }),
    h('button', { class: 'btn ghost danger', title: 'Revoke', html: icon('trash'), onclick: async () => {
      if (!await confirm2('Revoke link', 'Anyone with this link will lose access.', 'Revoke')) return;
      await api('DELETE', '/api/shares/' + s.id); refresh(); load(true);
    } }));
}

async function showShares() {
  const list = h('div', { class: 'list' });
  const draw = async () => {
    const d = await api('GET', '/api/shares');
    list.replaceChildren(...(d.shares.length ? d.shares.map(s => shareItem(s, draw)) : [h('div', { class: 'dim' }, 'No shared links yet. Right-click a file or folder → Share link.')]));
  };
  draw();
  modal('Shared links', list, { cls: 'wide' });
}

// ---------- settings ----------
function uaLabel(ua) {
  const b = /Edg\//.test(ua) ? 'Edge' : /OPR\//.test(ua) ? 'Opera' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : /curl/i.test(ua) ? 'curl' : 'Browser';
  const os = /iPhone|iPad/.test(ua) ? 'iOS' : /Android/.test(ua) ? 'Android' : /Windows/.test(ua) ? 'Windows' : /Mac OS/.test(ua) ? 'macOS' : /Linux/.test(ua) ? 'Linux' : '';
  return os ? `${b} on ${os}` : b;
}

async function showSettings() {
  const pkList = h('div', { class: 'list' });
  const sessList = h('div', { class: 'list' });
  const pkOnly = h('input', { type: 'checkbox' });
  const section = (title, desc, ...kids) => h('section', { class: 'set-card' }, h('h4', {}, title), desc ? h('p', { class: 'small dim' }, desc) : null, ...kids);
  const drawPk = async () => {
    const d = await api('GET', '/api/passkeys');
    S.me = await api('GET', '/api/me');
    pkOnly.checked = S.me.passkey_only;
    pkOnly.disabled = !d.passkeys.length;
    pkList.replaceChildren(...(d.passkeys.length ? d.passkeys.map(p => h('div', { class: 'item' }, iconEl('key'),
      h('div', { class: 'main' }, h('div', {}, h('span', { class: 't' }, p.name)), h('div', { class: 'small dim' }, 'Added ' + fmtDate(p.created) + (new Date(p.last_used).getFullYear() > 1971 ? ' · used ' + fmtDate(p.last_used) : ''))),
      h('button', { class: 'btn ghost danger', title: 'Remove', html: icon('trash'), onclick: async () => {
        if (!await confirm2('Remove passkey', `Remove “${p.name}”?`, 'Remove')) return;
        await api('DELETE', '/api/passkeys/' + p.id); drawPk();
      } }))) : [h('div', { class: 'small dim' }, 'No passkeys yet.')]));
  };
  const drawSess = async () => {
    const d = await api('GET', '/api/sessions');
    sessList.replaceChildren(...d.sessions.sort((a, b) => b.current - a.current || new Date(b.created) - new Date(a.created)).map(s => h('div', { class: 'item' },
      iconEl(s.current ? 'check' : 'lock'),
      h('div', { class: 'main' },
        h('div', { title: s.ua }, h('span', { class: 't' }, uaLabel(s.ua)), s.current ? h('span', { class: 'badge share' }, 'this device') : null),
        h('div', { class: 'small dim' }, `${s.ip} · ${s.method} · ${fmtDate(s.created)}`)),
      !s.current ? h('button', { class: 'btn ghost danger', title: 'Log out this device', html: icon('logout'), onclick: async () => { await api('DELETE', '/api/sessions/' + s.id); drawSess(); } }) : null)));
  };
  pkOnly.onchange = async () => {
    try { await api('POST', '/api/settings/passkey-only', { enabled: pkOnly.checked }); toast(pkOnly.checked ? 'Passcode fallback disabled' : 'Passcode fallback enabled'); }
    catch (e) { toast(e.message, true); pkOnly.checked = !pkOnly.checked; }
  };
  if (S.me.passkeys_on) drawPk();
  drawSess();
  const curF = passwordField({ placeholder: 'Current passcode', autocomplete: 'current-password', 'aria-label': 'Current passcode' });
  const nwF = passwordField({ placeholder: 'New passcode (min 4)', autocomplete: 'new-password', 'aria-label': 'New passcode' });
  const cur = curF.input, nw = nwF.input;
  const addPk = async () => {
    if (!window.PublicKeyCredential) return toast('Passkeys need HTTPS (or localhost) and a modern browser', true);
    const name = await prompt2('Name this passkey', 'e.g. “Phone”, “Laptop”, “YubiKey”', guessDevice(), { okLabel: 'Continue' });
    if (name == null) return;
    try {
      const o = await api('GET', '/api/passkey/register/begin');
      const cred = await navigator.credentials.create({
        publicKey: {
          ...o, challenge: b64uToBuf(o.challenge), user: { ...o.user, id: b64uToBuf(o.user.id) },
          excludeCredentials: (o.excludeCredentials || []).map(c => ({ ...c, id: b64uToBuf(c.id) })),
        }
      });
      await api('POST', '/api/passkey/register/finish', {
        name, clientDataJSON: bufToB64u(cred.response.clientDataJSON), attestationObject: bufToB64u(cred.response.attestationObject),
      });
      toast('Passkey added'); drawPk();
    } catch (e) { toast(e.message, true); }
  };
  modal('Settings', h('div', { class: 'settings' },
    !S.me.passkeys_on ? null : section('Passkeys', 'Primary sign-in: Face ID, fingerprint, Windows Hello, your phone or a security key. The passcode is only a fallback.',
      pkList,
      h('button', { class: 'btn', onclick: addPk, html: icon('plus') + 'Add passkey' }),
      h('label', { class: 'chk' }, pkOnly, h('span', {}, 'Disable passcode fallback',
        h('div', { class: 'small dim' }, 'Passkey only. Recovery: filenav reset-passkeys')))),
    section('Passcode', 'Changing it logs out all other devices.',
      h('div', { class: 'form-grid' }, curF.el, nwF.el,
        h('button', {
          class: 'btn', onclick: async () => {
            try { await api('POST', '/api/settings/passcode', { current: cur.value, new: nw.value }); toast('Passcode changed'); cur.value = nw.value = ''; drawSess(); }
            catch (e) { toast(e.message, true); }
          }
        }, 'Change'))),
    section('Active sessions', null, sessList,
      h('button', { class: 'btn danger', onclick: async () => { await api('DELETE', '/api/sessions/others'); drawSess(); toast('Other sessions logged out'); }, html: icon('logout') + 'Log out other devices' })),
    h('div', { class: 'small dim', style: { textAlign: 'center' } }, `filenav ${S.me.version}`),
  ), { cls: 'wide' });
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
