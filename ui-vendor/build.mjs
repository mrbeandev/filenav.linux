// Bundles third-party libs into ../web/vendor as ES modules with code splitting,
// so each language grammar is a small lazily-loaded chunk.
import { build } from 'esbuild';
import { rmSync, readdirSync, statSync } from 'node:fs';

const out = '../web/vendor';
rmSync(out, { recursive: true, force: true });
await build({
  entryPoints: { editor: 'src/editor.js', markdown: 'src/markdown.js', player: 'src/player.js' },
  bundle: true, splitting: true, format: 'esm', minify: true, target: 'es2020',
  outdir: out, chunkNames: 'c/[hash]', legalComments: 'none', logLevel: 'warning',
});
// HEIC/TIFF decoder: classic script for a sandboxed iframe (heic2any needs eval,
// which we only allow inside that opaque-origin sandbox, never in the main app)
await build({
  entryPoints: { imgdecode: 'src/imgdecode.js' }, bundle: true, format: 'iife', globalName: 'FNImg',
  minify: true, target: 'es2020', outdir: out, legalComments: 'none', logLevel: 'warning',
});
let total = 0, n = 0;
const walk = d => { for (const f of readdirSync(d)) { const p = d + '/' + f; const s = statSync(p); if (s.isDirectory()) walk(p); else { total += s.size; n++; } } };
walk(out);
console.log(`web/vendor: ${n} files, ${(total / 1024).toFixed(0)} KiB`);
