// Dev helper: screenshot the UI at desktop + phone sizes via Chrome DevTools Protocol.
// usage: node shots/shot.mjs <chrome-debug-port> <app-url> <outdir>
import { writeFileSync } from 'node:fs';
const [port, app, out] = process.argv.slice(2);
const sleep = ms => new Promise(r => setTimeout(r, ms));

const tab = await (await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' })).json();
const ws = new WebSocket(tab.webSocketDebuggerUrl);
await new Promise(r => ws.addEventListener('open', r, { once: true }));
let id = 0; const pending = new Map();
ws.addEventListener('message', e => { const m = JSON.parse(e.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } });
const cdp = (method, params = {}) => new Promise(r => { const i = ++id; pending.set(i, r); ws.send(JSON.stringify({ id: i, method, params })); });
const js = async expr => (await cdp('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })).result?.result?.value;
const shot = async name => { const r = await cdp('Page.captureScreenshot', { format: 'png' }); writeFileSync(`${out}/${name}.png`, Buffer.from(r.result.data, 'base64')); };
await cdp('Page.enable'); await cdp('Runtime.enable');
await cdp('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] });

for (const [name, w, hgt, mobile] of [['desktop', 1280, 720, false], ['phone', 390, 800, true]]) {
  await cdp('Emulation.setDeviceMetricsOverride', { width: w, height: hgt, deviceScaleFactor: mobile ? 2 : 1, mobile });
  await cdp('Emulation.setTouchEmulationEnabled', { enabled: mobile });
  await cdp('Network.clearBrowserCookies');
  await cdp('Page.navigate', { url: app }); await sleep(800);
  await shot(`${name}-login`);
  await js(`fetch('/api/login',{method:'POST',headers:{'X-Filenav':'1','Content-Type':'application/json'},body:JSON.stringify({passcode:'test1234'})}).then(r=>r.status)`);
  await cdp('Page.navigate', { url: app }); await sleep(900);
  await shot(`${name}-list`);
  await js(`document.querySelectorAll('table.files tbody input[type=checkbox]')[4].click(); document.querySelectorAll('table.files tbody input[type=checkbox]')[5].click(); 1`);
  await sleep(200); await shot(`${name}-select`);
  await js(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape'})); document.querySelector("button[title='Share this folder']").click(); 1`);
  await sleep(500); await shot(`${name}-share`);
  await js(`document.querySelector('.modal-bg')?.remove(); 1`);
  await js(`document.querySelectorAll('td.act .btn')[3].click(); 1`);
  await sleep(200); await shot(`${name}-menu`);
  await js(`document.querySelector('.menu-bg')?.remove(); document.querySelector('.menu')?.remove(); document.querySelector("button[title='Settings']").click(); 1`);
  await sleep(700); await shot(`${name}-settings`);
  await js(`document.querySelector('.modal-bg')?.remove(); 1`);
  for (const f of ['.gitignore', 'README.md', 'main.go']) {
    await js(`[...document.querySelectorAll('td.name a')].find(a => a.textContent.startsWith(${JSON.stringify(f)}))?.click(); 1`);
    await sleep(1500); await shot(`${name}-view-${f.replace(/^\./, '')}`);
    await js(`document.querySelector('.modal-bg')?.remove(); 1`);
  }
  const url = await js(`fetch('/api/shares',{method:'POST',headers:{'X-Filenav':'1','Content-Type':'application/json'},body:JSON.stringify({path:'',allow_upload:true,note:'Drop your holiday photos here please!'})}).then(r=>r.json()).then(j=>j.url)`);
  await cdp('Network.clearBrowserCookies');
  await cdp('Page.navigate', { url }); await sleep(900);
  await shot(`${name}-public`);
}
ws.close();
await fetch(`http://127.0.0.1:${port}/json/close/${tab.id}`);
console.log('done');
