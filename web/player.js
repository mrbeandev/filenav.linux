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

let vjsLoad;
const loadVJS = () => vjsLoad || (vjsLoad = import('/assets/vendor/player.js'));

/* Creates a Video.js player for `rel` (root-relative path) inside `container`.
   Plays the file directly when the browser can; otherwise asks the server for
   an on-demand HLS stream (only the incompatible part is converted).
   subs: [{ src, lang, label }]. Returns { video, info, plan }. */
async function mountPlayer(container, rel, { subs = [], autoplay = true, poster, onInfo } = {}) {
  container.replaceChildren(h('div', { class: 'pl-loading' }, h('div', { class: 'spinner' })));
  const [, probe] = await Promise.all([
    loadVJS().catch(() => null),
    api('GET', '/api/media/probe?path=' + encPath(rel)).catch(() => null),
  ]);
  const info = probe?.info;
  let plan = info ? await playbackPlan(info, extOf(rel)) : { mode: 'direct' };
  if (arguments[2]?.force && plan.mode === 'direct') plan = { mode: 'video', vc: 'h264', ac: 'aac' };
  if (onInfo) onInfo(info, plan, probe);

  const fail = msg => container.replaceChildren(h('div', { class: 'viewer-msg' },
    h('div', { class: 'vm-name' }, 'Can\u2019t play this video'),
    h('div', { class: 'dim', style: { maxWidth: '460px' } }, msg),
    info ? h('div', { class: 'dim small' }, formatLabel(info)) : null,
    h('a', { class: 'btn primary', href: dlURL(rel), html: icon('download') + 'Download' })));

  let src = dlURL(rel, true), hls = false;
  if (plan.mode !== 'direct') {
    if (!probe?.ffmpeg) { fail('This format needs conversion, but ffmpeg isn\u2019t installed on the server (e.g. apt install ffmpeg). You can still download it.'); return { info, plan }; }
    try {
      const st = await api('GET', '/api/media/hls?' + new URLSearchParams({ path: rel, vc: plan.vc, ac: plan.ac }));
      src = st.playlist; hls = true;
    } catch (e) { fail(e.message); return { info, plan }; }
  }
  const tracks = subs.map((s, i) => h('track', { kind: 'subtitles', src: s.src, srclang: s.lang, label: s.label, default: i === 0 }));
  const media = hls
    ? h('hlsjs-video', { src, playsinline: true, autoplay, crossorigin: 'use-credentials', preload: 'auto' }, ...tracks)
    : h('video', { src, playsinline: true, autoplay, crossorigin: 'use-credentials', preload: 'metadata' }, ...tracks);
  const skin = h('video-skin', { class: 'fn-skin' }, media);
  const player = h('video-player', poster ? { poster } : {}, skin);
  const badge = hls ? h('div', { class: 'pl-badge' }, plan.mode === 'video' ? 'Converted for this device' : 'Audio converted') : null;
  const box = h('div', { class: 'pl' }, player, badge);
  // aspect ratio from the probe (or the video once loaded) sizes the box
  const setAR = (w, hh) => { if (w > 0 && hh > 0) box.style.setProperty('--ar', `${w} / ${hh}`); };
  if (info?.video) setAR(info.video.width, info.video.height);
  media.addEventListener('loadedmetadata', () => setAR(media.videoWidth, media.videoHeight));
  container.replaceChildren(box);
  if (badge) setTimeout(() => badge.classList.add('fade'), 4000);
  let retried = false;
  media.addEventListener('error', () => {
    // direct play failed after all (e.g. codec the probe didn't predict): convert once
    if (!hls && probe?.ffmpeg && !retried) {
      retried = true;
      mountPlayer(container, rel, { subs, autoplay, poster, onInfo, force: true });
    }
  });
  if (subs.length) media.addEventListener('loadedmetadata', () => {
    const t = [...(media.textTracks || [])].find(x => x.kind === 'subtitles');
    if (t) t.mode = 'showing';
  }, { once: true });
  return { video: media, info, plan };
}
