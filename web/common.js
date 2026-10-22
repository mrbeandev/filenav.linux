// Shared helpers for the admin UI and the public share page. No frameworks.
'use strict';

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];

function h(tag, attrs = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'html') el.innerHTML = v; // only used with trusted static strings (icons)
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
    else if (v === true) el.setAttribute(k, '');
    else el.setAttribute(k, v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

// ---------- icons (inline SVG, lucide-style) ----------
const P = {
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" fill="currentColor" fill-opacity=".25"/>',
  file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>',
  img: '<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-5-5L5 21"/>',
  vid: '<rect x="2" y="5" width="15" height="14" rx="2"/><path d="m17 10 5-3v10l-5-3z"/>',
  aud: '<path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/>',
  arc: '<path d="M21 8v13H3V8M1 3h22v5H1zM10 12h4"/>',
  code: '<path d="m16 18 6-6-6-6M8 6l-6 6 6 6"/>',
  doc: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h6"/>',
  upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12"/>',
  download: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  folderPlus: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2zM12 10v6M9 13h6"/>',
  share: '<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><path d="m8.6 13.5 6.8 4M15.4 6.5l-6.8 4"/>',
  link: '<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/>',
  edit: '<path d="M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
  move: '<path d="M5 9l-3 3 3 3M9 5l3-3 3 3M15 19l-3 3-3-3M19 9l3 3-3 3M2 12h20M12 2v20"/>',
  copy: '<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  more: '<circle cx="12" cy="5" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="12" cy="19" r="1"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  eyeOff: '<path d="M10.7 5.1A10 10 0 0 1 12 5c6.5 0 10 7 10 7a17 17 0 0 1-2.9 3.9M6.6 6.6A17 17 0 0 0 2 12s3.5 7 10 7a9.7 9.7 0 0 0 5.4-1.6"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2M2 2l20 20"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  gear: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
  key: '<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6M15.5 7.5l3 3L22 7l-3-3"/>',
  lock: '<rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>',
  refresh: '<path d="M21 12a9 9 0 0 1-15 6.7L3 16M3 12a9 9 0 0 1 15-6.7L21 8M21 3v5h-5M3 21v-5h5"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  home: '<path d="m3 10 9-7 9 7v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M9 22V12h6v10"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
  pause: '<path d="M6 4h4v16H6zM14 4h4v16h-4z"/>',
  play: '<path d="m6 3 14 9-14 9z"/>',
  fileplus: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M12 18v-6M9 15h6"/>',
  left: '<path d="m15 18-6-6 6-6"/>',
  chart: '<path d="M21 12A9 9 0 1 1 12 3v9z"/><path d="M21 12a9 9 0 0 0-9-9" opacity=".45"/>',
  disk: '<rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 14h.01M10 14h.01"/>',
  sort: '<path d="M3 6h18M6 12h12M10 18h4"/>',
  right: '<path d="m9 18 6-6-6-6"/>',
  wrap: '<path d="M3 6h18M3 12h15a3 3 0 1 1 0 6h-4M3 18h7"/><path d="m16 16-2 2 2 2"/>',
  logo: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" fill="currentColor" fill-opacity=".2"/><path d="m10 13 2-2 2 2M12 11v5"/>',
};
function icon(name, cls = '') {
  return `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${P[name] || P.file}</svg>`;
}
function iconEl(name, cls = '') {
  const s = h('span', { class: 'ico', html: icon(name, cls) });
  return s.firstChild;
}

const EXT = {
  img: 'jpg jpeg png gif webp bmp svg ico avif heic tif tiff',
  vid: 'mp4 webm mkv mov avi m4v ogv 3gp flv wmv m2ts mts',
  aud: 'mp3 wav ogg oga flac m4a aac opus wma',
  arc: 'zip tar gz tgz bz2 xz 7z rar zst iso deb rpm apk dmg',
  code: 'js mjs cjs ts mts cts tsx jsx json go py rs c h cpp hpp java kt rb php sh bash zsh yml yaml toml ini conf cfg xml html htm css scss sql lua pl swift dockerfile makefile env',
  doc: 'txt md pdf doc docx odt rtf csv xls xlsx ods ppt pptx odp log epub',
};
const extOf = n => { const i = n.lastIndexOf('.'); return i > 0 ? n.slice(i + 1).toLowerCase() : n.toLowerCase(); };
function kindOf(name, isDir) {
  if (isDir) return 'dir';
  const e = extOf(name);
  for (const [k, v] of Object.entries(EXT)) if (v.split(' ').includes(e)) return k;
  return 'file';
}
function fileIcon(name, isDir) {
  const k = kindOf(name, isDir);
  const map = { dir: 'folder', img: 'img', vid: 'vid', aud: 'aud', arc: 'arc', code: 'code', doc: 'doc', file: 'file' };
  return iconEl(map[k], 'fi ' + k);
}
// rounded tinted tile with the file-type glyph (list rows)
function fileThumb(name, isDir) {
  const k = kindOf(name, isDir);
  const map = { dir: 'folder', img: 'img', vid: 'vid', aud: 'aud', arc: 'arc', code: 'code', doc: 'doc', file: 'file' };
  return h('span', { class: 'thumb ' + k, html: icon(map[k]) });
}
const extSet = str => new Set(str.split(' '));
const K_IMG = extSet('jpg jpeg jfif pjpeg png apng gif webp avif bmp ico svg');
const K_IMGDEC = extSet('heic heif tif tiff');
const K_VID = extSet('mp4 m4v webm ogv mov mkv avi wmv flv 3gp m2ts');
const K_AUD = extSet('mp3 wav ogg oga flac m4a aac opus weba');
const K_OFFICE = extSet('doc docx odt rtf xls xlsx xlsm xlsb ods ppt pptx odp key numbers pages');
const K_BIN = extSet('zip tar gz tgz bz2 xz 7z rar zst iso dmg img exe dll so dylib bin deb rpm apk msi jar class pyc o a ' +
  'woff woff2 ttf otf eot psd ai sqlite db mdb cr2 nef arw dng raf orf epub mobi');

// viewKind decides how the viewer opens a file; everything not known to be
// binary is tried as text (the server checks the actual content).
function viewKind(name) {
  const e = extOf(name);
  if (K_IMG.has(e)) return 'img';
  if (K_IMGDEC.has(e)) return 'imgdec';
  if (K_VID.has(e)) return 'vid';
  if (K_AUD.has(e)) return 'aud';
  if (e === 'pdf') return 'pdf';
  if (e === 'md' || e === 'markdown' || e === 'mdx') return 'md';
  if (K_OFFICE.has(e)) return 'office';
  if (K_BIN.has(e)) return 'bin';
  return 'text';
}
const OPEN_WITH = [
  ['text', 'code', 'Code editor'], ['plain', 'edit', 'Plain text editor'], ['md', 'doc', 'Markdown viewer'],
  ['img', 'img', 'Image viewer'], ['vid', 'vid', 'Video player'], ['aud', 'aud', 'Audio player'], ['pdf', 'doc', 'PDF viewer'],
];
// kept for inline rendering on the public single-file page
function previewKind(name) {
  const k = viewKind(name);
  return k === 'imgdec' ? 'img' : ['img', 'vid', 'aud', 'pdf'].includes(k) ? k : null;
}

function fmtSize(n) {
  if (n == null) return '';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + u[i];
}
function fmtDate(s) {
  if (!s) return '';
  const d = new Date(s);
  if (d.getFullYear() < 1971) return '';
  const now = new Date();
  const sameDay = d.toDateString() === now.toDateString();
  return sameDay ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString([], { year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric', month: 'short', day: 'numeric' });
}
function fmtDuration(sec) {
  if (!isFinite(sec) || sec < 0) return '';
  if (sec < 60) return Math.ceil(sec) + 's';
  if (sec < 3600) return Math.floor(sec / 60) + 'm ' + Math.floor(sec % 60) + 's';
  return Math.floor(sec / 3600) + 'h ' + Math.floor(sec % 3600 / 60) + 'm';
}

// ---------- API ----------
async function api(method, url, body) {
  const opt = { method, headers: { 'X-Filenav': '1' }, credentials: 'same-origin' };
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  const r = await fetch(url, opt);
  let data = null;
  try { data = await r.json(); } catch { }
  if (!r.ok) {
    const e = new Error((data && data.error) || r.statusText || 'request failed');
    e.status = r.status;
    throw e;
  }
  return data;
}

let toastTimer;
function toast(msg, bad = false) {
  $('.toast')?.remove();
  const t = h('div', { class: 'toast' + (bad ? ' bad' : '') }, msg);
  document.body.append(t);
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.remove(), bad ? 5000 : 2500);
}

async function copyText(text) {
  try { await navigator.clipboard.writeText(text); toast('Link copied'); }
  catch {
    const ta = h('textarea', { style: { position: 'fixed', opacity: 0 } }, text);
    document.body.append(ta); ta.select();
    try { document.execCommand('copy'); toast('Link copied'); } catch { toast('Copy failed', true); }
    ta.remove();
  }
}

// ---------- modal ----------
function modal(title, body, { buttons = [], cls = '', onClose, beforeClose } = {}) {
  let closed = false;
  const forceClose = () => { if (closed) return; closed = true; bg.remove(); document.removeEventListener('keydown', onKey); onClose && onClose(); };
  const close = () => { if (beforeClose && !beforeClose()) return; forceClose(); };
  const onKey = e => { if (e.key === 'Escape' && bg === $$('.modal-bg').pop()) close(); };
  const footer = buttons.length ? h('footer', {}, buttons.map(b =>
    h('button', { class: 'btn ' + (b.cls || ''), type: b.submit ? 'submit' : 'button', onclick: b.submit ? null : () => b.onclick(close) }, b.label))) : null;
  const head = h('header', {}, h('h3', {}, title), h('button', { class: 'btn ghost', onclick: close, html: icon('x'), title: 'Close' }));
  const box = h('div', { class: 'modal ' + cls }, head, h('div', { class: 'body' }, body), footer);
  const bg = h('div', { class: 'modal-bg', onmousedown: e => { if (e.target === bg) close(); } }, box);
  document.body.append(bg);
  document.addEventListener('keydown', onKey);
  setTimeout(() => $('input:not([type=checkbox]),textarea', box)?.focus(), 30);
  return { close, forceClose, box };
}

function prompt2(title, label, value = '', { okLabel = 'OK', selectBase = false } = {}) {
  return new Promise(resolve => {
    const input = h('input', { class: 'input', value });
    let done = false;
    const m = modal(title, [label ? h('div', { class: 'dim' }, label) : null, input], {
      buttons: [{ label: 'Cancel', onclick: c => c() }, { label: okLabel, cls: 'primary', onclick: c => { done = true; c(); resolve(input.value.trim()); } }],
      onClose: () => { if (!done) resolve(null); },
    });
    input.addEventListener('keydown', e => { if (e.key === 'Enter') { done = true; m.close(); resolve(input.value.trim()); } });
    setTimeout(() => {
      input.focus();
      const dot = value.lastIndexOf('.');
      if (selectBase && dot > 0) input.setSelectionRange(0, dot); else input.select();
    }, 40);
  });
}

function confirm2(title, msg, okLabel = 'Delete', cls = 'danger', cancelLabel = 'Cancel') {
  return new Promise(resolve => {
    let done = false;
    modal(title, h('div', { style: { whiteSpace: 'pre-wrap' } }, msg), {
      buttons: [{ label: cancelLabel, onclick: c => c() }, { label: okLabel, cls: 'primary ' + cls, onclick: c => { done = true; c(); resolve(true); } }],
      onClose: () => { if (!done) resolve(false); },
    });
  });
}

// ---------- resumable chunked uploader ----------
// Uploads files sequentially in chunks (default 16 MiB) with automatic retry
// and resume. Survives network drops and even page reloads (server keeps the
// partial file; re-adding the same file resumes where it left off).
class Uploader {
  constructor({ initURL, chunkURL, chunkSize, onChange, onFileDone, onAllDone }) {
    Object.assign(this, { initURL, chunkURL, chunkSize: chunkSize || 16 << 20, onChange, onFileDone, onAllDone });
    this.queue = [];
    this.running = false;
  }
  add(files, dir) {
    for (const f of files) {
      const rel = f.relPath || f.webkitRelativePath || f.name;
      this.queue.push({ file: f, dir, name: rel, size: f.size, sent: 0, state: 'queued', error: '', speed: 0, uid: null, ctrl: null });
    }
    this.onChange && this.onChange();
    this.run();
  }
  get active() { return this.queue.filter(u => u.state === 'queued' || u.state === 'uploading').length; }
  cancel(u) {
    u.state = 'cancelled';
    u.ctrl && u.ctrl.abort();
    if (u.uid) fetch(this.chunkURL(u.uid), { method: 'DELETE', headers: { 'X-Filenav': '1' } }).catch(() => { });
    this.onChange && this.onChange();
  }
  retry(u) { if (u.state === 'error') { u.state = 'queued'; u.error = ''; this.onChange && this.onChange(); this.run(); } }
  clearFinished() { this.queue = this.queue.filter(u => u.state === 'queued' || u.state === 'uploading'); this.onChange && this.onChange(); }
  async run() {
    if (this.running) return;
    this.running = true;
    try {
      let u;
      while ((u = this.queue.find(x => x.state === 'queued'))) {
        await this.one(u);
        this.onChange && this.onChange();
      }
    } finally {
      this.running = false;
      this.onAllDone && this.onAllDone();
    }
  }
  async one(u) {
    u.state = 'uploading';
    const f = u.file;
    let attempts = 0;
    while (u.state === 'uploading') {
      try {
        if (!u.uid) {
          const r = await api('POST', this.initURL, { dir: u.dir, name: u.name, size: f.size, key: `${f.lastModified}`, overwrite: !!u.overwrite });
          u.uid = r.uid; u.sent = r.offset; if (r.chunk_size) this.chunkSize = r.chunk_size;
          if (r.done) { u.state = 'done'; u.path = r.path; this.onFileDone && this.onFileDone(u); return; }
        }
        while (u.sent < f.size && u.state === 'uploading') {
          const end = Math.min(f.size, u.sent + this.chunkSize);
          const t0 = performance.now();
          const res = await this.put(u, f.slice(u.sent, end), u.sent);
          if (res.status === 409 && res.offset != null) { u.sent = res.offset; continue; }
          if (res.status === 404) { u.uid = null; throw new Error('upload expired, restarting'); }
          if (res.error) throw new Error(res.error);
          const dt = (performance.now() - t0) / 1000;
          const inst = (res.offset - u.sent) / Math.max(dt, 0.001);
          u.speed = u.speed ? u.speed * 0.6 + inst * 0.4 : inst;
          u.sent = res.offset; attempts = 0;
          this.onChange && this.onChange();
          if (res.done) { u.state = 'done'; u.path = res.path; this.onFileDone && this.onFileDone(u); return; }
        }
      } catch (e) {
        if (u.state !== 'uploading') return;
        if (e.status && e.status < 500 && e.status !== 408 && e.status !== 429 && e.status !== 404) { u.state = 'error'; u.error = e.message; return; }
        attempts++;
        if (attempts > 8) { u.state = 'error'; u.error = e.message; return; }
        u.error = `retrying (${attempts})… ${e.message}`;
        this.onChange && this.onChange();
        await new Promise(r => setTimeout(r, Math.min(30000, 1000 * 2 ** attempts)));
        u.error = '';
        // re-sync offset after an error
        if (u.uid) {
          try {
            const r = await api('POST', this.initURL, { dir: u.dir, name: u.name, size: f.size, key: `${f.lastModified}`, overwrite: !!u.overwrite });
            u.uid = r.uid; u.sent = r.offset;
          } catch { }
        }
      }
    }
  }
  put(u, blob, offset) {
    // XHR (not fetch) so we get upload progress events.
    return new Promise((resolve, reject) => {
      const x = new XMLHttpRequest();
      u.ctrl = { abort: () => x.abort() };
      x.open('PUT', this.chunkURL(u.uid));
      x.setRequestHeader('X-Filenav', '1');
      x.setRequestHeader('Upload-Offset', String(offset));
      x.setRequestHeader('Content-Type', 'application/octet-stream');
      let last = 0;
      x.upload.onprogress = e => {
        const now = performance.now();
        if (now - last > 250) { last = now; u.live = offset + e.loaded; this.onChange && this.onChange(); }
      };
      x.onload = () => {
        u.live = 0;
        let d = {};
        try { d = JSON.parse(x.responseText); } catch { }
        if (x.status === 409) return resolve({ status: 409, offset: d.offset });
        if (x.status === 404) return resolve({ status: 404 });
        if (x.status >= 200 && x.status < 300) return resolve(d);
        const e = new Error(d.error || `HTTP ${x.status}`); e.status = x.status; reject(e);
      };
      x.onerror = () => { u.live = 0; reject(new Error('network error')); };
      x.onabort = () => { u.live = 0; reject(new Error('aborted')); };
      x.timeout = 0;
      x.send(blob);
    });
  }
}

// Renders the floating upload progress panel.
function renderUploads(up, container) {
  if (!up.queue.length) { container.classList.add('hidden'); return; }
  container.classList.remove('hidden');
  const total = up.queue.length, done = up.queue.filter(u => u.state === 'done').length;
  container.replaceChildren(
    h('header', {}, h('span', { class: 'grow' }, up.active ? `Uploading ${done}/${total}` : `Uploads finished (${done}/${total})`),
      !up.active ? h('button', { class: 'btn ghost small', onclick: () => up.clearFinished() }, 'Clear') : null),
    h('div', { class: 'ul' }, up.queue.map(u => {
      const sent = u.live || u.sent;
      const pct = u.size ? Math.min(100, sent / u.size * 100) : (u.state === 'done' ? 100 : 0);
      let status = '';
      if (u.state === 'uploading') status = `${fmtSize(sent)} / ${fmtSize(u.size)}` + (u.speed ? ` · ${fmtSize(u.speed)}/s · ${fmtDuration((u.size - sent) / u.speed)} left` : '');
      else if (u.state === 'done') status = 'Done · ' + fmtSize(u.size);
      else if (u.state === 'queued') status = 'Waiting · ' + fmtSize(u.size);
      else if (u.state === 'cancelled') status = 'Cancelled';
      else if (u.state === 'error') status = 'Failed: ' + u.error;
      if (u.error && u.state === 'uploading') status = u.error;
      return h('div', { class: 'up ' + u.state },
        h('div', { class: 'row' }, h('span', { class: 'nm', title: u.name }, u.name),
          u.state === 'error' ? h('button', { class: 'btn ghost', title: 'Retry', html: icon('refresh'), onclick: () => up.retry(u) }) : null,
          (u.state === 'uploading' || u.state === 'queued') ? h('button', { class: 'btn ghost', title: 'Cancel', html: icon('x'), onclick: () => up.cancel(u) }) : null),
        h('div', { class: 'bar' }, h('i', { style: { width: pct + '%' } })),
        h('div', { class: 'small dim', style: { marginTop: '4px' } }, status));
    })));
}

// Collect files from a drag & drop event, including whole folders.
async function filesFromDrop(dt) {
  const items = [...(dt.items || [])].filter(i => i.kind === 'file');
  const entries = items.map(i => i.webkitGetAsEntry && i.webkitGetAsEntry()).filter(Boolean);
  if (!entries.length) return [...dt.files];
  const out = [];
  const walk = async (entry, prefix) => {
    if (entry.isFile) {
      const f = await new Promise((res, rej) => entry.file(res, rej));
      f.relPath = prefix + f.name;
      out.push(f);
    } else if (entry.isDirectory) {
      const reader = entry.createReader();
      let batch;
      do {
        batch = await new Promise((res, rej) => reader.readEntries(res, rej));
        for (const e of batch) await walk(e, prefix + entry.name + '/');
      } while (batch.length);
    }
  };
  for (const e of entries) await walk(e, '');
  return out;
}

function setupDropTarget(onFiles, enabled = () => true) {
  let depth = 0, overlay = null;
  const show = on => {
    if (on && !overlay) { overlay = h('div', { class: 'drop' }, 'Drop files or folders to upload'); document.body.append(overlay); }
    if (!on && overlay) { overlay.remove(); overlay = null; }
  };
  const hasFiles = e => e.dataTransfer && [...e.dataTransfer.types].includes('Files');
  document.addEventListener('dragenter', e => { if (!hasFiles(e) || !enabled()) return; e.preventDefault(); depth++; show(true); });
  document.addEventListener('dragleave', e => { if (!hasFiles(e) || !enabled()) return; depth = Math.max(0, depth - 1); if (!depth) show(false); });
  document.addEventListener('dragover', e => { if (hasFiles(e) && enabled()) e.preventDefault(); });
  document.addEventListener('drop', async e => {
    if (!hasFiles(e) || !enabled()) return;
    e.preventDefault(); depth = 0; show(false);
    onFiles(await filesFromDrop(e.dataTransfer));
  });
}

function pickFiles(folder = false) {
  return new Promise(resolve => {
    const inp = h('input', { type: 'file', multiple: true, style: { display: 'none' } });
    if (folder) inp.webkitdirectory = true;
    inp.onchange = () => { resolve([...inp.files]); inp.remove(); };
    document.body.append(inp);
    inp.click();
  });
}

// breadcrumbs: show the current folder (scroll to the end) and fade the left edge when earlier crumbs are hidden
function scrollCrumbsToEnd(c) {
  requestAnimationFrame(() => {
    c.style.scrollBehavior = 'auto';
    c.scrollLeft = c.scrollWidth;
    c.style.scrollBehavior = '';
    const fade = () => c.classList.toggle('fade-l', c.scrollLeft > 2);
    c.onscroll = fade;
    fade();
  });
}

// password input with a show/hide toggle; returns { el, input }
function passwordField(attrs = {}) {
  const input = h('input', { class: 'input', type: 'password', autocapitalize: 'off', spellcheck: 'false', ...attrs });
  const btn = h('button', { type: 'button', class: 'pw-eye', title: 'Show', 'aria-label': 'Show password', 'aria-pressed': 'false', html: icon('eye') });
  btn.addEventListener('mousedown', e => e.preventDefault()); // keep focus in the field
  btn.onclick = () => {
    const show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    btn.innerHTML = icon(show ? 'eyeOff' : 'eye');
    btn.title = show ? 'Hide' : 'Show';
    btn.setAttribute('aria-label', show ? 'Hide password' : 'Show password');
    btn.setAttribute('aria-pressed', String(show));
  };
  return { el: h('div', { class: 'pw-wrap' }, input, btn), input };
}

const joinPath = (a, b) => (a ? a + '/' : '') + b;
const encPath = p => encodeURIComponent(p);

// ---------- HEIC/TIFF decoding in a sandboxed iframe ----------
let _decReady = null, _decSeq = 0;
const _decPending = new Map();
function decodeImage(buf, ext) {
  if (!_decReady) _decReady = new Promise((resolve, reject) => {
    const f = h('iframe', { src: '/assets/imgdecode.html', sandbox: 'allow-scripts', style: { display: 'none' }, title: 'decoder' });
    window.addEventListener('message', e => {
      if (e.source !== f.contentWindow) return;
      const d = e.data || {};
      if (d.ready) return resolve(f);
      const p = _decPending.get(d.id);
      if (!p) return;
      _decPending.delete(d.id);
      d.error ? p.reject(new Error(d.error)) : p.resolve(d.blob);
    });
    document.body.append(f);
    setTimeout(() => reject(new Error('image decoder failed to load')), 20000);
  });
  return _decReady.then(f => new Promise((resolve, reject) => {
    const id = ++_decSeq;
    _decPending.set(id, { resolve, reject });
    f.contentWindow.postMessage({ id, buf, ext }, '*', [buf]);
  }));
}

// resolve a relative link (from a markdown file) against its folder
function resolveRel(dir, rel) {
  rel = rel.split(/[?#]/)[0];
  try { rel = decodeURIComponent(rel); } catch { }
  const parts = rel.startsWith('/') ? [] : (dir ? dir.split('/') : []);
  for (const p of rel.split('/')) {
    if (!p || p === '.') continue;
    if (p === '..') parts.pop(); else parts.push(p);
  }
  return parts.join('/');
}

const prefersDark = () => matchMedia('(prefers-color-scheme: dark)').matches;

/* Universal file viewer.
   o = { name, size, raw (inline URL), dl (download URL), text (text API URL),
         save?(content) → Promise, resolve?(relLink, isImage) → URL,
         count?, index?, nav?(i), extra?: [elements] } */
function openViewer(o) {
  const kind = o.as || viewKind(o.name);
  const body = h('div', { class: 'viewer' }, h('div', { class: 'viewer-msg dim' }, 'Loading…'));
  let dirty = false, editor = null, blobURL = null, mdMode = 'view', mdSrc = '';
  const cleanup = [];
  const m = modal(o.name, body, {
    cls: 'full',
    beforeClose: () => !dirty || window.confirm('Discard unsaved changes?'),
    onClose: () => { editor && editor.destroy(); blobURL && URL.revokeObjectURL(blobURL); cleanup.forEach(f => f()); },
  });
  const header = $('header', m.box);
  const closeBtn = header.lastChild;
  const add = el => { if (el) header.insertBefore(el, closeBtn); return el; };
  const langLabel = add(h('span', { class: 'small dim hide-sm' }));
  const wrapBtn = add(h('button', { class: 'btn ghost hidden', title: 'Toggle line wrap', onclick: () => { editor.setWrap(!editor.wrapped); wrapBtn.classList.toggle('on', editor.wrapped); }, html: icon('wrap') }));
  const mdBtn = add(h('button', { class: 'btn hidden', onclick: () => toggleMd() }));
  const saveBtn = o.save ? add(h('button', { class: 'btn primary hidden', disabled: true, onclick: () => save(), html: icon('check') + '<span class="lbl">Save</span>' })) : null;
  // prev/next only for media; editors don't get it (and arrow keys belong to the text)
  const hasNav = o.nav && o.count > 1 && !o.as && !['text', 'plain', 'md'].includes(kind);
  if (hasNav) {
    add(h('button', { class: 'btn ghost', title: 'Previous (←)', onclick: () => go(-1), html: icon('left') }));
    add(h('span', { class: 'small dim hide-sm' }, `${o.index + 1}/${o.count}`));
    add(h('button', { class: 'btn ghost', title: 'Next (→)', onclick: () => go(1), html: icon('right') }));
  }
  add(h('a', { class: 'btn ghost', href: o.dl, title: 'Download', html: icon('download') }));
  (o.extra || []).forEach(add);

  const go = d => {
    if (dirty && !window.confirm('Discard unsaved changes?')) return;
    dirty = false;
    m.forceClose();
    o.nav((o.index + d + o.count) % o.count);
  };
  const onKey = e => {
    if (!hasNav || e.target.closest?.('.cm-editor, input, textarea') || $$('.modal-bg').pop() !== m.box.parentNode) return;
    if (e.key === 'ArrowLeft') go(-1);
    if (e.key === 'ArrowRight') go(1);
  };
  document.addEventListener('keydown', onKey);
  cleanup.push(() => document.removeEventListener('keydown', onKey));
  // swipe left/right on touch screens (media only; editors need horizontal scroll)
  if (['img', 'imgdec', 'pdf', 'office', 'bin'].includes(kind)) {
    let x0 = null;
    body.addEventListener('touchstart', e => { x0 = e.touches.length === 1 ? e.touches[0].clientX : null; }, { passive: true });
    body.addEventListener('touchend', e => {
      if (x0 == null || !hasNav) return;
      const dx = e.changedTouches[0].clientX - x0;
      if (Math.abs(dx) > 70) go(dx < 0 ? 1 : -1);
    });
  }

  const markDirty = () => { if (!dirty) { dirty = true; if (saveBtn) saveBtn.disabled = false; } };
  async function save() {
    if (!o.save || !editor) return;
    try { await o.save(editor.getValue()); dirty = false; saveBtn.disabled = true; toast('Saved'); }
    catch (e) { toast(e.message, true); }
  }
  function noPreview(msg) {
    body.replaceChildren(h('div', { class: 'viewer-msg' },
      fileIcon(o.name, false), h('div', { class: 'vm-name' }, o.name),
      o.size != null ? h('div', { class: 'dim small' }, fmtSize(o.size)) : null,
      h('div', { class: 'dim', style: { maxWidth: '460px' } }, msg),
      h('div', { class: 'row wrap', style: { justifyContent: 'center' } },
        h('a', { class: 'btn primary', href: o.dl, html: icon('download') + 'Download' }),
        kind !== 'text' ? h('button', { class: 'btn', onclick: () => reopen('text'), html: icon('code') + 'Open as text' }) : null)));
  }
  function reopen(as) { if (dirty && !window.confirm('Discard unsaved changes?')) return; dirty = false; m.forceClose(); openViewer({ ...o, as }); }
  async function mountEditor(content) {
    const mod = await import('/assets/vendor/editor.js');
    const host = h('div', { class: 'cm-host' });
    body.replaceChildren(host);
    editor && editor.destroy();
    editor = await mod.create(host, { doc: content, name: kind === 'plain' ? 'file.txt' : o.name, dark: prefersDark(), readOnly: !o.save, onChange: markDirty, onSave: save });
    langLabel.textContent = editor.language;
    wrapBtn.classList.remove('hidden');
    if (saveBtn) saveBtn.classList.remove('hidden');
    if (kind !== 'md') editor.focus();
  }
  async function renderMd(src) {
    const mod = await import('/assets/vendor/markdown.js');
    const frag = mod.render(src, { resolve: o.resolve });
    body.replaceChildren(h('div', { class: 'md-scroll' }, h('article', { class: 'md-body' }, frag)));
  }
  async function toggleMd() {
    if (mdMode === 'view') {
      mdMode = 'edit'; await mountEditor(mdSrc);
      mdBtn.innerHTML = icon('eye') + '<span class="lbl">Preview</span>';
    } else {
      if (editor) mdSrc = editor.getValue();
      editor && editor.destroy(); editor = null;
      wrapBtn.classList.add('hidden'); langLabel.textContent = 'Markdown';
      mdMode = 'view'; await renderMd(mdSrc);
      mdBtn.innerHTML = icon(o.save ? 'edit' : 'code') + `<span class="lbl">${o.save ? 'Edit' : 'Source'}</span>`;
    }
  }
  async function loadText() {
    if (o.size != null && o.size > 10 << 20) { noPreview('This file is too large to open in the editor (max 10 MB).'); return null; }
    try { return (await api('GET', o.text)).content; }
    catch (e) {
      if (e.status === 415) noPreview('This looks like a binary file, so it can\u2019t be shown as text.');
      else noPreview(e.message);
      return null;
    }
  }

  (async () => {
    try {
      if (kind === 'img') {
        const img = h('img', { src: o.raw, alt: o.name, onclick: () => img.classList.toggle('zoom') });
        img.onerror = () => noPreview('This image could not be displayed.');
        body.replaceChildren(h('div', { class: 'preview' }, img));
      } else if (kind === 'imgdec') {
        body.replaceChildren(h('div', { class: 'viewer-msg dim' }, 'Decoding image…'));
        const buf = await (await fetch(o.raw, { credentials: 'same-origin' })).arrayBuffer();
        const blob = await decodeImage(buf, extOf(o.name));
        blobURL = URL.createObjectURL(blob);
        const img = h('img', { src: blobURL, alt: o.name, onclick: () => img.classList.toggle('zoom') });
        body.replaceChildren(h('div', { class: 'preview' }, img));
      } else if (kind === 'vid') {
        const v = h('video', { src: o.raw, controls: true, autoplay: true, playsinline: true, preload: 'metadata' });
        v.addEventListener('error', () => noPreview('Your browser can\u2019t play this video\u2019s format or codec (common with AVI, WMV, and some MKV/HEVC files). Download it and play it in VLC or mpv.'));
        body.replaceChildren(h('div', { class: 'preview' }, v));
      } else if (kind === 'aud') {
        const a = h('audio', { src: o.raw, controls: true, autoplay: true });
        a.addEventListener('error', () => noPreview('Your browser can\u2019t play this audio format.'));
        body.replaceChildren(h('div', { class: 'preview audio' }, fileIcon(o.name, false), a));
      } else if (kind === 'pdf') {
        body.replaceChildren(h('div', { class: 'preview' }, h('iframe', { src: o.raw, title: o.name })));
      } else if (kind === 'office') {
        noPreview('Office documents can\u2019t be previewed here yet. Download the file to open it.');
      } else if (kind === 'bin') {
        noPreview('No preview is available for this file type.');
      } else if (kind === 'md') {
        const src = await loadText();
        if (src == null) return;
        mdSrc = src; langLabel.textContent = 'Markdown';
        mdBtn.classList.remove('hidden');
        mdMode = 'edit'; await toggleMd();
      } else { // text, plain
        const src = await loadText();
        if (src == null) return;
        await mountEditor(src);
      }
    } catch (e) { noPreview('Preview failed: ' + e.message); }
  })();
  return m;
}
