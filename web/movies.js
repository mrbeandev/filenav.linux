// Movies section: poster library, scan & organise, unknown files, movie page.
'use strict';

const MV = { data: null, poll: null, filter: '' };
const posterURL = (m, size = 'w342') => m.dir ? dlURL(m.dir + '/poster.jpg', true) : (m.poster ? `/api/tmdb-img?s=${size}&p=${encodeURIComponent(m.poster)}` : '');
const backdropURL = m => m.dir ? dlURL(m.dir + '/backdrop.jpg', true) : '';
const fmtRuntime = min => min ? `${Math.floor(min / 60)}h ${min % 60}m` : '';

async function showMovies(sub) {
  clearTimeout(MV.poll);
  if (S.view !== 'movies') return;
  let d;
  // first visit (or switching page) shows a loader if the request is slow; polling stays quiet
  const polling = MV.lastSub === sub && MV.data;
  MV.lastSub = sub;
  const ld = polling ? null : setTimeout(() => { if (S.view === 'movies') setMain(h('div', { class: 'view-loading' }, h('div', { class: 'spin' }))); }, 150);
  try { d = await api('GET', '/api/movies', undefined, { quiet: !!polling }); }
  catch (e) { if (e.status === 401) return location.reload(); toast(e.message, true); return; }
  finally { clearTimeout(ld); }
  if (S.view !== 'movies') return;
  MV.data = d;
  if (sub && sub.startsWith('m/')) return renderMovie(d, decodeURIComponent(sub.slice(2)));
  if (sub === 'review') return renderReview(d);
  if (sub === 'unknown') return renderUnknown(d);
  renderLibrary(d);
  if (d.job) MV.poll = setTimeout(() => showMovies(sub), 1200);
}

function moviesNav(title, back, actions) { setNav(title, back, actions); }

function jobBanner(d, sub) {
  if (!d.job) return null;
  const pct = d.job_all ? Math.round(d.job_done / d.job_all * 100) : 0;
  return h('div', { class: 'mv-job' },
    h('div', { class: 'row' }, h('b', {}, d.job === 'scan' ? 'Scanning for new movies…' : 'Organising movies…'),
      h('span', { class: 'grow' }), h('span', { class: 'dim small' }, d.job_all ? `${d.job_done} / ${d.job_all}` : ''),
      h('button', { class: 'btn ghost danger', onclick: ev => busy(ev.currentTarget, async () => { await api('POST', '/api/movies/cancel').catch(e => toast(e.message, true)); await showMovies(sub); }) }, 'Stop')),
    h('div', { class: 'bar' }, h('i', { style: { width: pct + '%' } })),
    d.job_msg ? h('div', { class: 'dim small', style: { marginTop: '6px' } }, d.job_msg) : null);
}

async function startScan() {
  try { await api('POST', '/api/movies/scan'); }
  catch (e) { toast(e.message, true); return; }
  // wait for the scan, then open the review page if there is something to do
  const wait = async () => {
    const d = await api('GET', '/api/movies', undefined, { quiet: true }).catch(() => null);
    if (!d) return;
    if (d.job) { if (S.view === 'movies') showMovies(location.hash.replace(/^#movies\/?/, '')); setTimeout(wait, 1000); return; }
    if (d.plan.length) location.hash = '#movies/review';
    else if (d.unknown.length) location.hash = '#movies/unknown';
    else { toast(d.job_msg && /found 0/.test(d.job_msg) ? 'No new movies found' : 'Library is up to date'); showMovies(''); }
  };
  showMovies('');
  setTimeout(wait, 700);
}

function renderLibrary(d) {
  const main = $('#main');
  const scanBtn = h('button', { class: 'btn primary', disabled: !!d.job || !d.configured, onclick: ev => busy(ev.currentTarget, startScan), html: icon('refresh') + '<span class="lbl">Scan</span>' });
  moviesNav('Movies', null, [scanBtn]);
  const head = [h('h1', { class: 'large-title' }, 'Movies')];
  if (!d.configured) {
    setMain(...head, h('div', { class: 'mv-empty' },
      h('div', { class: 'mv-empty-ic', html: icon('vid') }),
      h('h2', {}, 'Set up your movie library'),
      h('p', { class: 'dim' }, 'Add your TMDB API key (for posters and details) and your OpenSubtitles account (for subtitles) in Settings.'),
      h('button', { class: 'btn primary', onclick: () => showSettings('movies') }, 'Open Settings')));
    return;
  }
  const stats = [`${d.movies.length} movie${d.movies.length === 1 ? '' : 's'}`, `in /${d.dir}`];
  const pills = [];
  if (d.plan.length) pills.push(h('a', { class: 'pill acc', href: '#movies/review' }, `${d.plan.length} ready to organise`));
  if (d.unknown.length) pills.push(h('a', { class: 'pill warn', href: '#movies/unknown' }, `${d.unknown.length} unknown`));
  if (d.subs_pending) pills.push(h('span', { class: 'pill' }, `${d.subs_pending} waiting for subtitles`));
  if (!d.subs_on) pills.push(h('a', { class: 'pill', href: '#', onclick: e => { e.preventDefault(); showSettings('movies'); } }, 'Subtitles off: add OpenSubtitles login'));
  const q = MV.filter.toLowerCase();
  const list = d.movies.filter(m => !q || m.title.toLowerCase().includes(q) || String(m.year).includes(q) || (m.genres || []).some(g => g.toLowerCase().includes(q)));
  const search = h('input', { class: 'input', type: 'search', placeholder: 'Search movies', value: MV.filter, oninput: e => { MV.filter = e.target.value; renderGrid(); } });
  const grid = h('div', { class: 'mv-grid' });
  const renderGrid = () => {
    const qq = MV.filter.toLowerCase();
    const items = d.movies.filter(m => !qq || m.title.toLowerCase().includes(qq) || String(m.year).includes(qq) || (m.genres || []).some(g => g.toLowerCase().includes(qq)));
    grid.replaceChildren(...items.map(m => h('a', { class: 'mv-card', href: '#movies/m/' + encodeURIComponent(m.dir), title: `${m.title} (${m.year})` },
      h('div', { class: 'mv-poster' }, h('img', { src: posterURL(m), alt: '', loading: 'lazy', onerror: e => e.target.replaceWith(h('div', { class: 'mv-noposter' }, m.title)) }),
        m.sub_status === 'pending' ? h('span', { class: 'mv-badge' }, 'CC…') : (m.subtitles || []).length ? h('span', { class: 'mv-badge ok' }, 'CC') : null),
      h('div', { class: 'mv-t' }, m.title), h('div', { class: 'mv-y' }, [m.year, m.rating ? '★ ' + m.rating.toFixed(1) : ''].filter(Boolean).join(' · ')))));
  };
  renderGrid();
  setMain(...head,
    h('p', { class: 'subtitle' }, stats.join(' · ')),
    jobBanner(d, ''),
    pills.length ? h('div', { class: 'pills' }, pills) : null,
    d.movies.length ? h('div', { class: 'searchbar' }, iconEl('search'), search) : null,
    d.movies.length ? grid : h('div', { class: 'mv-empty' },
      h('div', { class: 'mv-empty-ic', html: icon('vid') }),
      h('h2', {}, 'No movies yet'),
      h('p', { class: 'dim' }, `Copy video files into /${d.dir} (any name like "Interstellar.2014.1080p.mkv"), then press Scan.`),
      h('div', { class: 'row', style: { justifyContent: 'center' } },
        h('button', { class: 'btn', onclick: () => go(d.dir) }, 'Open folder'),
        h('button', { class: 'btn primary', disabled: !!d.job, onclick: ev => busy(ev.currentTarget, startScan) }, 'Scan'))));
}

// ---------- review (plan) ----------
function renderReview(d) {
  const main = $('#main');
  const items = d.plan;
  const sel = new Set(items.filter(i => i.status === 'ready').map(i => i.id));
  const apply = h('button', { class: 'btn primary', disabled: !!d.job }, '');
  const upd = () => { apply.textContent = `Organise ${sel.size}`; apply.disabled = !sel.size || !!d.job; };
  apply.onclick = () => busy(apply, async () => {
    try { await api('POST', '/api/movies/apply', { ids: [...sel] }); toast('Organising…'); location.hash = '#movies'; }
    catch (e) { toast(e.message, true); }
  });
  upd();
  moviesNav('Review', 'Movies', [apply]);
  const rows = items.map(it => {
    const cb = h('input', { type: 'checkbox', class: 'sel-circle', checked: sel.has(it.id), disabled: it.status !== 'ready', onclick: e => { e.target.checked ? sel.add(it.id) : sel.delete(it.id); upd(); } });
    return h('div', { class: 'mv-row' }, cb,
      h('img', { class: 'mv-thumb', src: it.match?.poster ? posterURL({ poster: it.match.poster }, 'w92') : '', alt: '' }),
      h('div', { class: 'txt' },
        h('div', { class: 't1' }, `${it.match.title}${it.match.year ? ` (${it.match.year})` : ''}`),
        h('div', { class: 't2' }, h('span', { class: 'mono' }, it.src.split('/').pop())),
        h('div', { class: 't2' }, it.status === 'exists' ? h('span', { class: 'warn' }, 'Already in your library: skipped') : ['→ /' + it.dest, it.subs_from?.length ? `keeps ${it.subs_from.length} subtitle file(s)` : ''].filter(Boolean).join(' · '))),
      h('button', { class: 'btn ghost', title: 'Wrong movie? Pick another', onclick: () => pickMatch(it), html: icon('edit') }));
  });
  setMain(h('h1', { class: 'large-title' }, 'Review'),
    h('p', { class: 'subtitle' }, 'These files were identified. Organising moves each into its own "Title (Year)" folder and downloads the poster and English subtitles.'),
    jobBanner(d, 'review'),
    items.length ? h('div', { class: 'group' }, rows) : h('div', { class: 'empty' }, h('p', {}, 'Nothing to review. Press Scan after adding files.')),
    d.unknown.length ? h('p', { class: 'subtitle', style: { marginTop: '16px' } }, h('a', { href: '#movies/unknown' }, `${d.unknown.length} file(s) could not be identified →`)) : null);
}

// ---------- unknown ----------
function renderUnknown(d) {
  const main = $('#main');
  moviesNav('Unknown', 'Movies', []);
  setMain(h('h1', { class: 'large-title' }, 'Unknown'),
    h('p', { class: 'subtitle' }, 'These files could not be matched automatically. Tap one to search by name or paste an IMDb ID (tt…).'),
    d.unknown.length ? h('div', { class: 'group' }, d.unknown.map(it => h('div', { class: 'cell', onclick: () => pickMatch(it) },
      h('span', { class: 'thumb vid', html: icon('vid') }),
      h('div', { class: 'txt' }, h('span', { class: 't1' }, it.src.split('/').pop()),
        h('span', { class: 't2' }, it.error ? it.error : `${fmtSize(it.size)} · guessed “${it.guess || '?'}”`)),
      h('span', { class: 'acc', html: icon('right') })))) : h('div', { class: 'empty' }, h('p', {}, 'Nothing unknown. 🎉')));
}

// search TMDB / IMDb id and assign to a plan item
function pickMatch(it) {
  const input = h('input', { class: 'input', type: 'search', placeholder: 'Movie name, IMDb ID (tt0816692) or TMDB link', value: it.guess || '' });
  const results = h('div', { class: 'mv-results' });
  const search = async () => {
    const q = input.value.trim();
    if (!q) return;
    results.replaceChildren(skeletonRows(3));
    try {
      const r = await api('GET', '/api/movies/search?q=' + encodeURIComponent(q));
      results.replaceChildren(...(r.results.length ? r.results.map(m => h('button', { class: 'mv-result', onclick: ev => busy(ev.currentTarget, async () => {
        try { await api('POST', '/api/movies/match', { id: it.id, tmdb_id: m.tmdb_id }); dlg.close(); toast(`Matched to ${m.title}`); location.hash = '#movies/review'; showMovies('review'); }
        catch (e) { toast(e.message, true); }
      }) },
        h('img', { src: m.poster ? posterURL({ poster: m.poster }, 'w92') : '', alt: '' }),
        h('div', {}, h('b', {}, m.title), h('span', { class: 'dim' }, m.year ? ` (${m.year})` : ''),
          h('div', { class: 'dim small mv-over' }, m.overview || '')))) : [h('div', { class: 'dim small', style: { padding: '12px' } }, 'No results. Try another name or paste the IMDb ID.')]));
    } catch (e) { results.replaceChildren(h('div', { class: 'err', style: { padding: '12px' } }, e.message)); }
  };
  input.addEventListener('keydown', e => { if (e.key === 'Enter') search(); });
  if (it.candidates?.length) {
    results.replaceChildren(...it.candidates.map(m => h('button', { class: 'mv-result', onclick: ev => busy(ev.currentTarget, async () => {
      try { await api('POST', '/api/movies/match', { id: it.id, tmdb_id: m.tmdb_id }); dlg.close(); location.hash = '#movies/review'; showMovies('review'); }
      catch (e) { toast(e.message, true); }
    }) }, h('img', { src: m.poster ? posterURL({ poster: m.poster }, 'w92') : '', alt: '' }),
      h('div', {}, h('b', {}, m.title), h('span', { class: 'dim' }, m.year ? ` (${m.year})` : ''), h('div', { class: 'dim small mv-over' }, m.overview || '')))));
  }
  const dlg = modal('Identify movie', [
    h('div', { class: 'dim small mono', style: { wordBreak: 'break-all' } }, it.src.split('/').pop()),
    h('div', { class: 'row' }, input, h('button', { class: 'btn primary', onclick: ev => busy(ev.currentTarget, search) }, 'Search')),
    results]);
  if (!it.candidates?.length && it.guess) search();
}

// ---------- movie page ----------
function renderMovie(d, dir) {
  const m = d.movies.find(x => x.dir === dir);
  const main = $('#main');
  if (!m) { moviesNav('Movie', 'Movies', []); setMain(h('div', { class: 'empty' }, h('p', {}, 'Movie not found.'))); return; }
  const video = m.dir + '/' + m.video;
  moviesNav(m.title, 'Movies', [h('button', { class: 'btn ghost', title: 'Open folder', onclick: () => go(m.dir), html: icon('folder') })]);
  const subs = (m.subtitles || []);
  const play = () => playMovie(m);
  setMain(
    h('div', { class: 'mv-hero', style: { backgroundImage: `url("${backdropURL(m)}")` } },
      h('div', { class: 'mv-hero-in' },
        h('img', { class: 'mv-hero-poster', src: posterURL(m, 'w500'), alt: '' }),
        h('div', { class: 'mv-hero-txt' },
          h('h1', {}, m.title, m.year ? h('span', { class: 'dim' }, ` (${m.year})`) : null),
          m.tagline ? h('p', { class: 'mv-tag' }, m.tagline) : null,
          h('div', { class: 'mv-facts' }, [m.rating ? '★ ' + m.rating.toFixed(1) : '', fmtRuntime(m.runtime), (m.genres || []).join(', ')].filter(Boolean).join('  ·  ')),
          h('div', { class: 'row wrap', style: { marginTop: '14px' } },
            h('button', { class: 'btn primary mv-play', onclick: play, html: icon('play') + 'Play' }),
            m.tmdb_id ? h('button', { class: 'btn', onclick: e => playTrailer(m, e.currentTarget), html: icon('vid') + '<span>Trailer</span>' }) : null,
            h('a', { class: 'btn', href: dlURL(video), html: icon('download') + '<span class="lbl">Download</span>' }),
            m.imdb_id ? h('a', { class: 'btn', href: 'https://www.imdb.com/title/' + m.imdb_id + '/', target: '_blank', rel: 'noopener noreferrer' }, 'IMDb') : null)))),
    h('div', { class: 'mv-body' },
      m.overview ? h('p', { class: 'mv-overview' }, m.overview) : null,
      m.director ? h('p', { class: 'dim' }, 'Directed by ', h('b', { style: { color: 'var(--label)' } }, m.director)) : null,
      m.cast?.length ? h('div', {}, h('div', { class: 'group-h' }, 'Cast'),
        h('div', { class: 'mv-cast' }, m.cast.map(c => h('div', { class: 'mv-actor' },
          c.profile_path ? h('img', { src: posterURL({ poster: c.profile_path }, 'w185'), alt: '', loading: 'lazy' }) : h('div', { class: 'mv-noface', html: icon('logo') }),
          h('b', {}, c.name), h('span', { class: 'dim small' }, c.character || ''))))) : null,
      h('div', { class: 'group-h' }, 'Files'),
      h('div', { class: 'group' },
        h('div', { class: 'cell' }, h('span', { class: 'thumb vid', html: icon('vid') }), h('div', { class: 'txt' }, h('span', { class: 't1' }, m.video), h('span', { class: 't2' }, '/' + m.dir))),
        ...subs.map(s => h('div', { class: 'cell' }, h('span', { class: 'thumb doc', html: icon('doc') }), h('div', { class: 'txt' }, h('span', { class: 't1' }, s), h('span', { class: 't2' }, 'Subtitle')))),
        h('div', { class: 'cell' }, h('span', { class: 'thumb file', html: icon('doc') }),
          h('div', { class: 'txt' }, h('span', { class: 't1' }, subs.length ? 'Get subtitles again' : 'No subtitles yet'),
            h('span', { class: 't2' }, m.sub_status === 'pending' ? 'Waiting: daily download limit or service error. Retried on next Scan.' : m.sub_status === 'none' ? 'None found on OpenSubtitles' : 'Downloads the best English subtitle')),
          h('button', { class: 'btn', onclick: e => busy(e.currentTarget, async () => {
            try { const r = await api('POST', '/api/movies/subs', { dir: m.dir }); toast(r.status === 'ok' ? 'Subtitle downloaded' : r.status === 'pending' ? 'Not available right now (limit reached?)' : 'No subtitle found'); await showMovies('m/' + encodeURIComponent(m.dir)); }
            catch (x) { toast(x.message, true); }
          }) }, 'Fetch')))));
}

async function playTrailer(m, btn) {
  if (btn.classList.contains('is-busy')) return;
  btn.classList.add('is-busy'); btn.disabled = true;
  try {
    const t = await api('GET', '/api/movies/trailer?tmdb_id=' + m.tmdb_id);
    const src = `https://www.youtube-nocookie.com/embed/${encodeURIComponent(t.youtube)}?autoplay=1&rel=0&modestbranding=1&playsinline=1`;
    const frame = h('iframe', { src, title: t.name || 'Trailer', allow: 'autoplay; encrypted-media; picture-in-picture; fullscreen', allowfullscreen: true, referrerpolicy: 'strict-origin' });
    modal(`${m.title}: ${t.official ? 'Official ' : ''}${t.type || 'Trailer'}`, h('div', { class: 'mv-trailer' }, frame), { cls: 'full trailer' });
  } catch (e) { toast(e.status === 404 ? 'No trailer found for this movie' : e.message, true); }
  finally { btn.disabled = false; btn.classList.remove('is-busy'); }
}

function playMovie(m) {
  const video = m.dir + '/' + m.video;
  const subs = (m.subtitles || []).map((s, i) => {
    const lang = (s.match(/\.([a-z]{2,3})\.(srt|vtt)$/i) || [])[1] || 'en';
    return { src: '/api/vtt?path=' + encPath(m.dir + '/' + s), lang, label: lang.toUpperCase() + (i ? ' ' + (i + 1) : '') };
  });
  const box = h('div', { class: 'preview' });
  const fmt = h('span', { class: 'small dim hide-sm' });
  const dlg = modal(m.title + (m.year ? ` (${m.year})` : ''), box, { cls: 'full' });
  const head = $('header', dlg.box);
  head.insertBefore(fmt, head.lastChild);
  mountPlayer(box, video, { subs, onInfo: info => { if (info) fmt.textContent = formatLabel(info); } });
  return dlg;
}
