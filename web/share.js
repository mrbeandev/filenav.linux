// Public share page
'use strict';

const shareID = location.pathname.split('/')[2];
const base = '/s/' + encodeURIComponent(shareID);
const V = { info: null, path: '', entries: [], sel: new Set() };
let uploader;

async function boot() {
  try { V.info = await api('GET', base + '/api/info'); }
  catch (e) { return fatal(e.message); }
  document.title = V.info.name + ' — ' + V.info.title;
  if (V.info.needs_pass) return renderPassword();
  if (V.info.allow_upload) {
    uploader = new Uploader({
      initURL: base + '/api/upload/init', chunkURL: uid => base + '/api/upload/' + uid, chunkSize: V.info.chunk_size,
      onChange: () => renderUploads(uploader, $('#uploads')),
      onAllDone: () => { if (!V.info.upload_only) load(); },
    });
    setupDropTarget(files => upload(files));
    window.addEventListener('beforeunload', e => { if (uploader.active) { e.preventDefault(); e.returnValue = ''; } });
  }
  window.addEventListener('hashchange', () => load());
  render();
  if (V.info.is_dir && !V.info.upload_only) load();
}

function fatal(msg) {
  $('#root').replaceChildren(h('div', { class: 'center' }, h('div', { class: 'card', style: { textAlign: 'center' } },
    h('div', { class: 'dim', html: icon('link') }), h('h1', { style: { justifyContent: 'center' } }, 'Link unavailable'), h('p', { class: 'dim' }, msg))));
}

function renderPassword() {
  const err = h('div', { class: 'err' });
  const pw = h('input', { class: 'input', type: 'password', placeholder: 'Password', autofocus: true });
  $('#root').replaceChildren(h('div', { class: 'center' }, h('div', { class: 'card' },
    h('h1', {}, iconEl('lock', 'logo'), V.info.name),
    h('div', { class: 'dim' }, 'This link is password protected.'),
    h('form', {
      onsubmit: async e => {
        e.preventDefault(); err.textContent = '';
        try { await api('POST', base + '/api/unlock', { password: pw.value }); location.reload(); }
        catch (x) { err.textContent = x.message; pw.select(); }
      }
    }, pw, h('button', { class: 'btn primary', type: 'submit', style: { justifyContent: 'center' } }, 'Open'), err))));
}

const subFromHash = () => decodeURIComponent(location.hash.replace(/^#\/?/, '')).replace(/\/+$/, '');
const dl = (p, inline) => base + '/dl?path=' + encPath(p) + (inline ? '&inline=1' : '');

async function load() {
  V.path = subFromHash();
  V.sel.clear();
  try { const d = await api('GET', base + '/api/list?path=' + encPath(V.path)); V.entries = d.entries; }
  catch (e) { toast(e.message, true); V.entries = []; }
  renderList();
}

function upload(files) {
  if (!files || !files.length) return;
  uploader.add(files, V.info.upload_only ? '' : V.path);
}

function render() {
  const i = V.info;
  const hero = h('div', { class: 'share-hero' },
    i.is_dir ? fileIcon('', true) : fileIcon(i.name, false),
    h('h2', {}, i.name),
    h('div', { class: 'dim small' },
      !i.is_dir ? fmtSize(i.size) + ' · ' + fmtDate(i.mtime) : (i.upload_only ? 'Upload files to share them with the owner' : 'Shared folder'),
      i.expires ? ' · link expires ' + new Date(i.expires).toLocaleString() : ''),
    i.note ? h('div', { style: { marginTop: '14px' } }, h('div', { class: 'note' }, i.note)) : null);

  const kids = [hero];
  if (!i.is_dir) {
    const pk = previewKind(i.name);
    kids.push(h('div', { class: 'row', style: { justifyContent: 'center', margin: '16px' } },
      h('a', { class: 'btn primary', href: base + '/dl', html: icon('download') + 'Download' }),
      pk && pk !== 'text' ? h('button', { class: 'btn', onclick: () => openPreview(i.name, base + '/dl?inline=1', base + '/dl'), html: icon('eye') + 'Preview' }) : null));
    if (pk === 'img') kids.push(h('div', { class: 'container', style: { textAlign: 'center', padding: '0 16px 30px' } },
      h('img', { src: base + '/dl?inline=1', style: { maxWidth: '100%', maxHeight: '70vh', borderRadius: '10px' } })));
    if (pk === 'vid') kids.push(h('div', { class: 'container', style: { textAlign: 'center', padding: '0 16px 30px' } },
      h('video', { src: base + '/dl?inline=1', controls: true, playsinline: true, style: { maxWidth: '100%', maxHeight: '70vh', borderRadius: '10px' } })));
    if (pk === 'aud') kids.push(h('div', { class: 'container', style: { textAlign: 'center' } }, h('audio', { src: base + '/dl?inline=1', controls: true })));
  }
  if (i.allow_upload) {
    kids.push(h('div', { class: 'container' }, h('div', {
      class: 'dropzone', onclick: async () => upload(await pickFiles()),
      html: icon('upload') + '<div style="font-weight:600;margin-top:8px">Click or drag files here to upload</div><div class="small">Large files are uploaded in resumable chunks</div>'
    }), h('div', { class: 'row', style: { justifyContent: 'center', marginTop: '-6px' } },
      h('button', { class: 'btn ghost small', onclick: async () => upload(await pickFiles(true)) }, 'or upload a whole folder'))));
  }
  if (i.is_dir && !i.upload_only) kids.push(h('div', { class: 'container' }, h('div', { class: 'toolbar', id: 'bar' }), h('div', { id: 'list' })));
  $('#root').replaceChildren(h('div', {}, ...kids));
}

function renderList() {
  const bar = $('#bar'), list = $('#list');
  if (!bar) return;
  const parts = V.path ? V.path.split('/') : [];
  const crumbs = [h('a', { href: '#/' }, V.info.name)];
  parts.forEach((p, i) => { crumbs.push(h('span', { class: 's' }, '/'), h('a', { href: '#/' + parts.slice(0, i + 1).map(encodeURIComponent).join('/') }, p)); });
  const zipAll = () => {
    const q = new URLSearchParams({ dir: V.path });
    if (V.sel.size) V.sel.forEach(n => q.append('names', n));
    else if (V.path) { /* whole current folder */ }
    location.href = base + '/zip?' + q;
  };
  bar.replaceChildren(h('nav', { class: 'crumbs' }, crumbs),
    h('button', { class: 'btn' + (V.sel.size ? ' primary' : ''), onclick: zipAll, html: icon('download') + `<span class="lbl">${V.sel.size ? `Download ${V.sel.size} selected` : 'Download all'}</span>` }));
  if (!V.entries.length) { list.replaceChildren(h('div', { class: 'empty', html: icon('folder') + '<p>This folder is empty.</p>' })); return; }
  list.replaceChildren(h('table', { class: 'files' }, h('tbody', {}, V.entries.map(e => {
    const full = joinPath(V.path, e.name);
    const sel = V.sel.has(e.name);
    return h('tr', { class: 'f' + (sel ? ' sel' : '') },
      h('td', { class: 'c' }, h('input', { type: 'checkbox', checked: sel, onclick: ev => { ev.target.checked ? V.sel.add(e.name) : V.sel.delete(e.name); renderList(); } })),
      h('td', { class: 'name' }, h('a', {
        href: e.dir ? '#/' + full.split('/').map(encodeURIComponent).join('/') : dl(full),
        onclick: ev => {
          if (e.dir) return;
          const pk = previewKind(e.name);
          if (pk && pk !== 'text') { ev.preventDefault(); openPreview(e.name, dl(full, true), dl(full)); }
        }
      }, fileIcon(e.name, e.dir), h('span', {}, e.name))),
      h('td', { class: 'num hide-sm' }, e.dir ? '—' : fmtSize(e.size)),
      h('td', { class: 'num hide-sm' }, fmtDate(e.mtime)),
      h('td', { class: 'act' }, e.dir
        ? h('a', { class: 'btn ghost', title: 'Download folder', href: base + '/zip?' + new URLSearchParams({ dir: V.path, names: e.name }), html: icon('download') })
        : h('a', { class: 'btn ghost', title: 'Download', href: dl(full), html: icon('download') })));
  }))));
}

boot();
