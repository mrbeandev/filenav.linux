// Markdown → sanitized HTML (marked + DOMPurify), loaded on demand.
import { marked } from 'marked';
import DOMPurify from 'dompurify';

marked.setOptions({ gfm: true, breaks: false });

// rewrite relative links/images so they resolve against the file's folder via a URL builder
export function render(src, { resolve } = {}) {
  const html = marked.parse(src, { async: false });
  const clean = DOMPurify.sanitize(html, { USE_PROFILES: { html: true }, FORBID_TAGS: ['style', 'form'], FORBID_ATTR: ['style'] });
  const tpl = document.createElement('template');
  tpl.innerHTML = clean;
  for (const a of tpl.content.querySelectorAll('a[href]')) {
    const h = a.getAttribute('href');
    if (/^(https?:|mailto:)/i.test(h)) { a.target = '_blank'; a.rel = 'noopener noreferrer'; }
    else if (h.startsWith('#')) { /* in-page anchor */ }
    else if (resolve) { a.href = resolve(h, false); a.target = '_blank'; }
  }
  for (const inp of tpl.content.querySelectorAll('input')) {
    if (inp.type === 'checkbox') inp.disabled = true; else inp.remove();
  }
  for (const img of tpl.content.querySelectorAll('img[src]')) {
    const s = img.getAttribute('src');
    if (!/^(https?:|data:)/i.test(s) && resolve) img.src = resolve(s, true);
    img.loading = 'lazy';
  }
  return tpl.content;
}
