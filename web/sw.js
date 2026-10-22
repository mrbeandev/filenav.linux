// filenav service worker: caches only the app shell (HTML/CSS/JS/icons) so the
// UI opens instantly and works as an installed app. Files and API responses are
// never cached; they always go to the network.
const VERSION = '__VERSION__';
const CACHE = 'filenav-shell-' + VERSION;
const SHELL = ['/', '/assets/style.css', '/assets/common.js', '/assets/app.js', '/assets/share.js',
  '/assets/favicon.svg', '/assets/icons/icon-192.png', '/manifest.webmanifest'];

self.addEventListener('install', e => {
  e.waitUntil(caches.open(CACHE).then(c => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', e => {
  e.waitUntil(caches.keys()
    .then(keys => Promise.all(keys.filter(k => k.startsWith('filenav-shell-') && k !== CACHE).map(k => caches.delete(k))))
    .then(() => self.clients.claim()));
});

self.addEventListener('fetch', e => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== location.origin) return;
  const p = url.pathname;
  // never intercept data: API, downloads, share links, uploads
  if (p.startsWith('/api/') || p.startsWith('/s/')) return;

  // navigations: network first (fresh login state), fall back to the cached shell offline
  if (req.mode === 'navigate') {
    e.respondWith(fetch(req).catch(() => caches.match('/')));
    return;
  }
  // content-hashed vendor chunks: cache first, they never change
  if (p.startsWith('/assets/vendor/c/')) {
    e.respondWith(caches.open(CACHE).then(async c => {
      const hit = await c.match(req);
      if (hit) return hit;
      const res = await fetch(req);
      if (res.ok) c.put(req, res.clone());
      return res;
    }));
    return;
  }
  // other shell assets: stale-while-revalidate
  if (p.startsWith('/assets/') || p === '/manifest.webmanifest') {
    e.respondWith(caches.open(CACHE).then(async c => {
      const hit = await c.match(req);
      const net = fetch(req).then(res => { if (res.ok) c.put(req, res.clone()); return res; }).catch(() => hit);
      return hit || net;
    }));
  }
});
