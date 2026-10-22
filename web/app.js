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
  const pass = h('input', { class: 'input', type: 'password', placeholder: 'Passcode', autocomplete: 'current-password' });
  const passForm = h('form', {
    class: 'pass-form' + (hasPasskeys ? ' hidden' : ''),
    onsubmit: async e => {
      e.preventDefault(); err.textContent = '';
      try { await api('POST', '/api/login', { passcode: pass.value }); location.reload(); }
      catch (x) { err.textContent = x.message; pass.select(); }
    }
  }, pass, h('button', { class: 'btn primary', type: 'submit', style: { justifyContent: 'center' } }, 'Unlock'));
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
  const searchInput = h('input', { class: 'input search', type: 'search', placeholder: 'Search in this folder…' });
  let st;
  searchInput.addEventListener('input', () => {
    clearTimeout(st);
    const q = searchInput.value.trim();
    st = setTimeout(() => q ? doSearch(q) : (S.search = null, render()), 350);
  });
  $('#root').replaceChildren(h('div', { class: 'app' },
    h('header', { class: 'top' },
      h('a', { class: 'brand', href: '#/', onclick: e => { e.preventDefault(); searchInput.value = ''; S.search = null; go(''); } }, iconEl('logo', 'logo'), h('span', { class: 'hide-sm' }, S.me.title)),
      h('div', { class: 'grow' }, searchInput),
      h('span', { class: 'disk hide-md', id: 'disk' }),
      S.me.shares_on ? h('button', { class: 'btn ghost', title: 'Shared links', onclick: showShares, html: icon('link') + '<span class="lbl">Shares</span>' }) : null,
      S.me.auth ? h('button', { class: 'btn ghost', title: 'Settings', onclick: showSettings, html: icon('gear') }) : null,
      S.me.read_only ? h('span', { class: 'badge', title: 'Read-only mode (config)' }, 'read-only') : null,
      S.me.auth ? h('button', { class: 'btn ghost', title: 'Log out', onclick: async () => { await api('POST', '/api/logout'); location.reload(); }, html: icon('logout') }) : null),
    h('div', { class: 'toolbar' },
      h('nav', { class: 'crumbs', id: 'crumbs' }),
      h('div', { class: 'tools' },
        S.me.read_only ? null : h('button', { class: 'btn primary', title: 'Upload files', onclick: async () => upload(await pickFiles()), html: icon('upload') + '<span class="lbl">Upload</span>' }),
        S.me.read_only ? null : h('button', { class: 'btn', title: 'Upload folder', onclick: async () => upload(await pickFiles(true)), html: icon('folder') + '<span class="lbl">Folder</span>' }),
        S.me.read_only ? null : h('button', { class: 'btn', title: 'New folder', onclick: newFolder, html: icon('folderPlus') }),
        S.me.read_only ? null : h('button', { class: 'btn', title: 'New text file', onclick: newFile, html: icon('fileplus') }),
        !S.me.shares_on ? null : h('button', { class: 'btn', title: 'Share this folder', onclick: () => shareDialog(S.path, true), html: icon('share') }),
        h('button', { class: 'btn ghost', title: 'Refresh', onclick: () => load(), html: icon('refresh') }))),
    h('div', { id: 'selbar' }),
    h('main', { id: 'main' })));

  uploader = new Uploader({
    initURL: '/api/upload/init', chunkURL: uid => '/api/upload/' + uid, chunkSize: S.me.chunk_size,
    onChange: () => renderUploads(uploader, $('#uploads')),
    onFileDone: () => { clearTimeout(S._rt); S._rt = setTimeout(() => load(true), 400); },
    onAllDone: () => { load(true); loadDisk(); },
  });
  setupDropTarget(files => upload(files), () => !S.me.read_only);
  window.addEventListener('beforeunload', e => { if (uploader.active) { e.preventDefault(); e.returnValue = ''; } });
  window.addEventListener('hashchange', () => { const p = pathFromHash(); if (p !== S.path) { S.search = null; searchInput.value = ''; load(); } });
  document.addEventListener('keydown', onKey);
  document.addEventListener('click', e => { if (!e.target.closest('.menu')) closeMenu(); });
  load();
  loadDisk();
}

const pathFromHash = () => decodeURIComponent(location.hash.replace(/^#\/?/, '')).replace(/\/+$/, '');
function go(p) { location.hash = '#/' + p.split('/').map(encodeURIComponent).join('/'); }

async function load(keepSel) {
  S.path = pathFromHash();
  if (!keepSel) S.sel.clear();
  try {
    const [d, sh] = await Promise.all([api('GET', '/api/list?path=' + encPath(S.path)), api('GET', '/api/shares')]);
    S.entries = d.entries; S.shares = sh.shares;
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
  try { const d = await api('GET', '/api/disk'); $('#disk').textContent = `${fmtSize(d.free)} free of ${fmtSize(d.total)}`; } catch { }
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
function render() {
  renderCrumbs();
  renderSelbar();
  const main = $('#main');
  if (S.search) return renderSearch(main);
  const list = [...S.entries].sort((a, b) => {
    if (a.dir !== b.dir) return a.dir ? -1 : 1;
    const k = S.sort.key;
    let r = k === 'name' ? a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' })
      : k === 'size' ? a.size - b.size : new Date(a.mtime) - new Date(b.mtime);
    return r * S.sort.dir;
  });
  if (!list.length) {
    main.replaceChildren(h('div', { class: 'empty', html: icon('folder') + '<p>This folder is empty.<br>Drag &amp; drop files here or use Upload.</p>' }));
    return;
  }
  const sharedPaths = new Set(S.shares.filter(s => !s.expired).map(s => s.path));
  const allSel = list.length && list.every(e => S.sel.has(e.name));
  const th = (key, label, cls = '') => h('th', { class: cls, onclick: () => { S.sort = { key, dir: S.sort.key === key ? -S.sort.dir : 1 }; render(); } },
    label + (S.sort.key === key ? (S.sort.dir > 0 ? ' ↑' : ' ↓') : ''));
  main.replaceChildren(h('table', { class: 'files' },
    h('thead', {}, h('tr', {},
      h('th', { class: 'c' }, h('input', { type: 'checkbox', checked: allSel, onclick: e => { e.stopPropagation(); list.forEach(x => e.target.checked ? S.sel.add(x.name) : S.sel.delete(x.name)); render(); } })),
      th('name', 'Name'), th('size', 'Size', 'num hide-sm'), th('mtime', 'Modified', 'num hide-sm'), h('th', {}))),
    h('tbody', {}, list.map(e => row(e, joinPath(S.path, e.name), sharedPaths)))));
}

function row(e, full, sharedPaths) {
  const sel = S.sel.has(e.name);
  const open = ev => { ev.preventDefault(); openEntry(e, full, tr._list); };
  const tr = h('tr', {
    class: 'f' + (sel ? ' sel' : ''),
    oncontextmenu: ev => { ev.preventDefault(); if (!S.sel.has(e.name)) { S.sel.clear(); S.sel.add(e.name); render(); } contextMenu(ev, e, full); },
  },
    h('td', { class: 'c' }, h('input', { type: 'checkbox', checked: sel, onclick: ev => { ev.stopPropagation(); ev.target.checked ? S.sel.add(e.name) : S.sel.delete(e.name); render(); } })),
    h('td', { class: 'name' }, h('a', { href: e.dir ? '#/' + full.split('/').map(encodeURIComponent).join('/') : dlURL(full), onclick: open, title: e.name },
      fileIcon(e.name, e.dir), h('span', { class: 'nm' }, h('span', {}, e.name),
        h('span', { class: 'meta' }, [e.dir ? 'Folder' : fmtSize(e.size), fmtDate(e.mtime)].filter(Boolean).join(' · ')))),
      sharedPaths && sharedPaths.has(full) ? h('span', { class: 'badge share' }, 'shared') : null,
      e.link ? h('span', { class: 'badge' }, 'link') : null),
    h('td', { class: 'num hide-sm' }, e.dir ? '—' : fmtSize(e.size)),
    h('td', { class: 'num hide-sm' }, fmtDate(e.mtime)),
    h('td', { class: 'act' }, h('button', { class: 'btn ghost', html: icon('more'), onclick: ev => { ev.stopPropagation(); contextMenu(ev, e, full); } })));
  return tr;
}

function renderSearch(main) {
  const { results, truncated, q } = S.search;
  main.replaceChildren(h('div', { class: 'small dim', style: { padding: '10px 16px' } },
    `${results.length} result(s) for “${q}”` + (truncated ? ' (truncated — refine your search)' : '')),
    results.length ? h('table', { class: 'files' }, h('tbody', {}, results.map(r => {
      const tr = row(r, r.path, null);
      tr._list = results.filter(x => !x.dir).map(x => ({ e: x, full: x.path }));
      tr.firstChild.replaceChildren();
      const dir = r.path.split('/').slice(0, -1).join('/');
      tr.children[1].append(h('div', { class: 'small dim' }, '/' + dir));
      return tr;
    }))) : null);
}

function renderCrumbs() {
  const parts = S.path ? S.path.split('/') : [];
  const c = $('#crumbs');
  const kids = [h('a', { href: '#/', html: icon('home'), title: 'Home' })];
  parts.forEach((p, i) => {
    kids.push(h('span', { class: 's' }, '/'));
    kids.push(h('a', { href: '#/' + parts.slice(0, i + 1).map(encodeURIComponent).join('/') }, p));
  });
  c.replaceChildren(...kids);
}

function renderSelbar() {
  const bar = $('#selbar');
  const n = S.sel.size;
  if (!n && !S.clipboard) { bar.replaceChildren(); bar.className = ''; return; }
  bar.className = 'selbar';
  const paths = [...S.sel].map(x => joinPath(S.path, x));
  const kids = [];
  if (n) {
    kids.push(h('b', { style: { whiteSpace: 'nowrap' } }, `${n} selected`), h('span', { class: 'grow' }),
      h('button', { class: 'btn', title: 'Download', onclick: () => downloadSel(), html: icon('download') + '<span class="lbl">Download</span>' }),
      S.me.read_only ? null : h('button', { class: 'btn', title: 'Cut (move)', onclick: () => { S.clipboard = { op: 'move', paths }; S.sel.clear(); render(); toast('Navigate to a folder and Paste'); }, html: icon('move') + '<span class="lbl">Move</span>' }),
      S.me.read_only ? null : h('button', { class: 'btn', title: 'Copy', onclick: () => { S.clipboard = { op: 'copy', paths }; S.sel.clear(); render(); toast('Navigate to a folder and Paste'); }, html: icon('copy') + '<span class="lbl">Copy</span>' }),
      S.me.read_only ? null : h('button', { class: 'btn', title: 'Move / copy to…', onclick: () => moveDialog(paths), html: icon('folder') + '<span class="lbl">Move to…</span>' }),
      S.me.read_only ? null : h('button', { class: 'btn danger', title: 'Delete', onclick: () => del(paths), html: icon('trash') + '<span class="lbl">Delete</span>' }),
      h('button', { class: 'btn ghost', onclick: () => { S.sel.clear(); render(); }, html: icon('x') }));
  } else {
    const c = S.clipboard;
    kids.push(h('span', { class: 'grow' }, `${c.paths.length} item(s) ready to ${c.op}`),
      h('button', { class: 'btn primary', onclick: paste, html: icon('check') + `Paste here` }),
      h('button', { class: 'btn ghost', onclick: () => { S.clipboard = null; render(); }, html: icon('x') }));
  }
  bar.replaceChildren(...kids);
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
  const multi = S.sel.size > 1;
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
  m.style.left = Math.min(ev.clientX, innerWidth - r.width - 8) + 'px';
  m.style.top = Math.min(ev.clientY, innerHeight - r.height - 8) + 'px';
}

function onKey(e) {
  if ($('.modal-bg') || /INPUT|TEXTAREA/.test(document.activeElement?.tagName)) return;
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
  flags.push(s.expires && new Date(s.expires).getFullYear() > 1971 ? (s.expired ? 'EXPIRED ' : 'expires ') + new Date(s.expires).toLocaleString() : 'no expiry');
  flags.push(`${s.views} views · ${s.downloads}${s.max_downloads ? '/' + s.max_downloads : ''} downloads`);
  if (s.missing) flags.push('⚠ target missing');
  return h('div', { class: 'item' },
    fileIcon(s.path.split('/').pop() || 'home', s.is_dir),
    h('div', { class: 'main' }, h('div', {}, '/' + s.path), h('div', { class: 'small dim' }, flags.join(' · '))),
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
      h('div', { class: 'main' }, h('div', {}, p.name), h('div', { class: 'small dim' }, 'Added ' + fmtDate(p.created) + (new Date(p.last_used).getFullYear() > 1971 ? ' · used ' + fmtDate(p.last_used) : ''))),
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
        h('div', { title: s.ua }, uaLabel(s.ua), s.current ? h('span', { class: 'badge share' }, 'this device') : null),
        h('div', { class: 'small dim' }, `${s.ip} · ${s.method} · ${fmtDate(s.created)}`)),
      !s.current ? h('button', { class: 'btn ghost danger', title: 'Log out this device', html: icon('logout'), onclick: async () => { await api('DELETE', '/api/sessions/' + s.id); drawSess(); } }) : null)));
  };
  pkOnly.onchange = async () => {
    try { await api('POST', '/api/settings/passkey-only', { enabled: pkOnly.checked }); toast(pkOnly.checked ? 'Passcode fallback disabled' : 'Passcode fallback enabled'); }
    catch (e) { toast(e.message, true); pkOnly.checked = !pkOnly.checked; }
  };
  if (S.me.passkeys_on) drawPk();
  drawSess();
  const cur = h('input', { class: 'input', type: 'password', placeholder: 'Current passcode', autocomplete: 'current-password' });
  const nw = h('input', { class: 'input', type: 'password', placeholder: 'New passcode (min 4)', autocomplete: 'new-password' });
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
      h('div', { class: 'form-grid' }, cur, nw,
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
