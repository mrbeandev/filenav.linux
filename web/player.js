// Smart video player: plays files directly when the browser can, otherwise
// streams a converted version from the server (audio-only conversion when
// possible, full H.264 conversion only for video the device can't decode).
'use strict';

const CODEC_STR = {
  h264: 'avc1.640028', hevc: 'hvc1.1.6.L120.90', vp9: 'vp09.00.40.08', av1: 'av01.0.08M.08', vp8: 'vp8',
  aac: 'mp4a.40.2', mp3: 'mp4a.69', opus: 'opus', flac: 'flac', vorbis: 'vorbis', ac3: 'ac-3', eac3: 'ec-3',
};
const AUDIO_OK = new Set(['aac', 'mp3', 'opus', 'flac', 'vorbis']);
const VIDEO_LABEL = { h264: 'H.264', hevc: 'HEVC', vp9: 'VP9', av1: 'AV1', mpeg4: 'MPEG-4', vc1: 'VC-1', mpeg2video: 'MPEG-2', msmpeg4v3: 'DivX' };
const AUDIO_LABEL = { aac: 'AAC', ac3: 'Dolby Digital', eac3: 'Dolby Digital+', dts: 'DTS', truehd: 'Dolby TrueHD', mp3: 'MP3', opus: 'Opus', flac: 'FLAC', vorbis: 'Vorbis', pcm_s16le: 'PCM' };

async function canDecodeVideo(codec, w, h) {
  const c = CODEC_STR[codec];
  if (!c) return false;
  if (navigator.mediaCapabilities) {
    try {
      const r = await navigator.mediaCapabilities.decodingInfo({ type: 'file', video: { contentType: `video/mp4; codecs="${c}"`, width: w || 1920, height: h || 1080, bitrate: 8e6, framerate: 24 } });
      return r.supported;
    } catch { }
  }
  return !!document.createElement('video').canPlayType(`video/mp4; codecs="${c}"`);
}

// Decide how to play: direct file, copy video + convert audio, or convert video.
async function playbackPlan(info, ext) {
  const v = info.video, a = info.audio[0];
  const vOk = !v || await canDecodeVideo(v.codec, v.width, v.height);
  const aOk = !a || AUDIO_OK.has(a.codec);
  const containerOk = ['mp4', 'webm', 'mkv'].includes(info.container) && (info.container !== 'mkv' || !!document.createElement('video').canPlayType('video/x-matroska'));
  if (vOk && aOk && containerOk) return { mode: 'direct' };
  if (vOk) return { mode: 'audio', vc: 'copy', ac: a && AUDIO_OK.has(a.codec) && a.codec !== 'flac' ? 'copy' : 'aac' };
  return { mode: 'video', vc: 'h264', ac: 'aac' };
}

function formatLabel(info) {
  const v = info.video, a = info.audio[0];
  return [v && (VIDEO_LABEL[v.codec] || v.codec.toUpperCase()) + (v.height ? ` ${v.height >= 2000 ? '4K' : v.height + 'p'}` : ''),
    a && (AUDIO_LABEL[a.codec] || a.codec.toUpperCase()) + (a.channels > 2 ? ` ${a.channels === 6 ? '5.1' : a.channels === 8 ? '7.1' : a.channels + 'ch'}` : '')].filter(Boolean).join(' · ');
}

/* Creates a player for `rel` (root-relative path) inside `container`.
   subs: [{ src, lang, label }] (src = /api/vtt?path=...). Returns { video, destroy }. */
async function mountPlayer(container, rel, { subs = [], autoplay = true, onInfo } = {}) {
  const v = h('video', { controls: true, playsinline: true, preload: 'metadata', autoplay });
  const status = h('div', { class: 'pl-status hidden' });
  const wrap = h('div', { class: 'pl' }, v, status);
  container.replaceChildren(wrap);
  const direct = dlURL(rel, true);
  let probe = null;
  try { probe = await api('GET', '/api/media/probe?path=' + encPath(rel)); } catch { }
  const info = probe?.info;
  const plan = info ? await playbackPlan(info, extOf(rel)) : { mode: 'direct' };
  if (onInfo) onInfo(info, plan, probe);

  let offset = 0; // seconds into the source where the current stream starts
  const duration = info?.duration || 0;
  const addSubs = shift => {
    [...v.querySelectorAll('track')].forEach(t => t.remove());
    subs.forEach((s, i) => v.append(h('track', { kind: 'subtitles', src: s.src + (shift ? `&shift=${shift.toFixed(2)}` : ''), srclang: s.lang, label: s.label, default: i === 0 })));
    if (subs.length) setTimeout(() => { if (v.textTracks[0]) v.textTracks[0].mode = 'showing'; }, 50);
  };
  const showStatus = (txt, kind = '') => { status.className = 'pl-status ' + kind; status.textContent = txt; status.classList.toggle('hidden', !txt); };

  const fail = msg => {
    showStatus('');
    wrap.replaceChildren(h('div', { class: 'viewer-msg' },
      h('div', { class: 'vm-name' }, 'Can\u2019t play this video'),
      h('div', { class: 'dim', style: { maxWidth: '460px' } }, msg),
      info ? h('div', { class: 'dim small' }, formatLabel(info)) : null,
      h('a', { class: 'btn primary', href: dlURL(rel), html: icon('download') + 'Download' })));
  };

  if (plan.mode === 'direct') {
    v.src = direct;
    addSubs(0);
    v.addEventListener('error', () => {
      // direct play failed after all (e.g. codec mismatch): try a converted stream once
      if (probe?.ffmpeg && !v.dataset.retried) { v.dataset.retried = 1; startStream({ mode: 'video', vc: 'h264', ac: 'aac' }, v.currentTime || 0); }
      else fail(probe?.ffmpeg === false ? 'Your browser doesn\u2019t support this format, and the server has no ffmpeg to convert it (install ffmpeg on the server).' : 'Your browser doesn\u2019t support this format.');
    });
    return { video: v, info, plan };
  }
  if (!probe?.ffmpeg) { fail('This format needs conversion, but ffmpeg isn\u2019t installed on the server. Install it (e.g. apt install ffmpeg) or download the file.'); return { video: v, info, plan }; }

  // converted stream: the browser sees a live stream starting at `offset`,
  // so the seek bar is rebuilt on top of the native controls.
  let cur = plan;
  function startStream(p, at) {
    cur = p;
    offset = Math.max(0, Math.min(at || 0, duration ? duration - 1 : at));
    const q = new URLSearchParams({ path: rel, t: offset.toFixed(2), vc: p.vc, ac: p.ac });
    v.src = '/api/media/stream?' + q;
    addSubs(offset);
    showStatus(p.mode === 'video' ? 'Converting video for this device…' : 'Converting audio…', 'busy');
    v.play().catch(() => { });
  }
  v.addEventListener('playing', () => showStatus(''));
  v.addEventListener('waiting', () => { if (cur.mode === 'video') showStatus('Buffering…', 'busy'); });
  v.addEventListener('error', () => fail('The server couldn\u2019t convert this file.'));
  // custom seek bar (native one only covers the converted part)
  const seek = h('input', { type: 'range', class: 'pl-seek', min: 0, max: duration || 1, step: 1, value: 0 });
  const time = h('span', { class: 'pl-time' }, '0:00');
  const fmt = s => { s = Math.max(0, Math.floor(s)); const hh = Math.floor(s / 3600), mm = Math.floor(s / 60) % 60, ss = s % 60; return (hh ? hh + ':' + String(mm).padStart(2, '0') : mm) + ':' + String(ss).padStart(2, '0'); };
  let dragging = false;
  v.addEventListener('timeupdate', () => { if (!dragging) { seek.value = offset + v.currentTime; } time.textContent = `${fmt(offset + v.currentTime)} / ${fmt(duration)}`; });
  seek.addEventListener('input', () => { dragging = true; time.textContent = `${fmt(+seek.value)} / ${fmt(duration)}`; });
  seek.addEventListener('change', () => { dragging = false; startStream(cur, +seek.value); });
  wrap.append(h('div', { class: 'pl-bar' }, h('span', { class: 'pl-badge' }, cur.mode === 'video' ? 'Converted' : 'Audio converted'), seek, time));
  startStream(plan, 0);
  return { video: v, info, plan };
}
