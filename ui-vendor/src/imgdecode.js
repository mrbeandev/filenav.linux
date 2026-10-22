// Decoders for image formats browsers can't show natively (HEIC/HEIF, TIFF).
import heic2any from 'heic2any';
import UTIF from 'utif';

async function decode(buf, ext) {
  if (ext === 'heic' || ext === 'heif') {
    const out = await heic2any({ blob: new Blob([buf]), toType: 'image/jpeg', quality: 0.9 });
    return Array.isArray(out) ? out[0] : out;
  }
  if (ext === 'tif' || ext === 'tiff') {
    const ifds = UTIF.decode(buf);
    UTIF.decodeImage(buf, ifds[0]);
    const rgba = UTIF.toRGBA8(ifds[0]);
    const c = document.createElement('canvas');
    c.width = ifds[0].width; c.height = ifds[0].height;
    c.getContext('2d').putImageData(new ImageData(new Uint8ClampedArray(rgba.buffer), c.width, c.height), 0, 0);
    const blob = await new Promise(r => c.toBlob(r, 'image/png'));
    return blob;
  }
  throw new Error('unsupported format');
}

// runs inside the sandboxed iframe: parent posts {id, buf, ext}, we reply {id, blob} or {id, error}
self.addEventListener('message', async e => {
  const { id, buf, ext } = e.data || {};
  if (!id) return;
  try { const blob = await decode(buf, ext); e.source.postMessage({ id, blob }, '*'); }
  catch (err) { e.source.postMessage({ id, error: String(err && err.message || err) }, '*'); }
});
parent.postMessage({ ready: true }, '*');
