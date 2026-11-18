// Música: las vistas. Inicio, Buscar y Tu biblioteca, y las páginas de álbum, artista y playlist.
import { $, el, icon, json, plural, joinText, fmtLong, typeBadge, toast, ask, menu, empty } from './lib.js';
import { href, go, render, route } from './nav.js';
import { api, userName } from './session.js';
import { playAt, isShuffle } from './player.js';
import { library, ensureLib, liked, createPlaylist, cover, item, card, libRow, trackList, libItems, saveButton } from './music.js';

const OFFLINE = 'Sin conexión con el servidor.';
const frag = (...kids) => { const f = document.createDocumentFragment(); f.append(...kids.flat().filter(Boolean)); return f; };
const heading = (text, count) => el('h2', { className: 'section-title' }, text, count != null && el('span', { textContent: count }));
const note = (text, cls = '') => el('p', { className: `muted note ${cls}`, textContent: text });
const linkBtn = (text, to, glyph) => el('a', { className: 'btn primary', href: to }, icon(glyph), text);
const fold = (s) => s.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
const getJSON = async (url, opts) => {
  const r = await api(url, opts);
  if (r.status === 404) return null;
  if (!r.ok) throw new Error('Intenta de nuevo en un momento.');
  return r.json();
};

export function greeting() {
  const h = new Date().getHours();
  return h >= 5 && h < 12 ? 'Buenos días' : h >= 12 && h < 19 ? 'Buenas tardes' : 'Buenas noches';
}

// musicView reparte según la ruta: #/musica, /buscar, /biblioteca, /album/…, /artista/…, /playlist/id, /gustan.
export function musicView(path) {
  const [kind, ...rest] = path.split('/');
  const arg = rest.join('/');
  switch (kind) {
    case '': return homeView();
    case 'buscar': return searchView();
    case 'biblioteca': return libraryView();
    case 'album': return albumView(arg);
    case 'artista': return artistView(arg);
    case 'playlist': return playlistView(arg);
    case 'gustan': return likedView();
  }
  return Promise.resolve(gone('Esta página no existe'));
}
const gone = (what) => empty('alert', what, 'Puede que se haya movido o borrado de la biblioteca.', linkBtn('Ir a Inicio', href('musica'), 'home'));

// Inicio

const shelf = (text, cards) => cards.length > 0 && frag(heading(text), scroller(el('div', { className: 'shelf' }, cards)));

// scroller pone flechas ‹ › a los lados de una fila horizontal, para el mouse (sin barra de desplazamiento no hay cómo arrastrarla).
// Cada flecha se esconde cuando ya no hay más hacia ese lado; en pantallas táctiles el CSS las oculta.
function scroller(row) {
  const arrow = (dir, glyph, label) => {
    const b = el('button', { className: 'icon-btn shelf-arrow ' + dir, tabIndex: -1 }, icon(glyph)); // el teclado ya recorre las tarjetas con Tab
    b.setAttribute('aria-label', label);
    b.onclick = () => row.scrollBy({ left: (dir === 'left' ? -1 : 1) * row.clientWidth * 0.8, behavior: 'smooth' });
    return b;
  };
  const left = arrow('left', 'back', 'Anterior'), right = arrow('right', 'next', 'Siguiente');
  const update = () => {
    left.hidden = row.scrollLeft < 4;
    right.hidden = row.scrollLeft + row.clientWidth > row.scrollWidth - 4;
  };
  row.addEventListener('scroll', update, { passive: true });
  new ResizeObserver(update).observe(row); // también corre al montarse la vista
  return el('div', { className: 'shelf-wrap' }, row, left, right);
}

async function homeView() {
  const d = await library();
  if (!d.albums.length) {
    return empty('music', 'Tu música empieza aquí', 'La biblioteca está vacía. En Buscar puedes pegar un enlace de Spotify; aparece aquí cuando termine de descargar.',
      linkBtn('Buscar música', href('musica', 'buscar'), 'search'));
  }
  const heard = d.albums.filter((a) => a.played).sort((a, b) => b.played - a.played);
  // Accesos rápidos: lo último que tocaste, sea playlist o álbum.
  const recent = [...d.playlists.map(item.playlist), ...heard.map(item.album)].sort((a, b) => b.when - a.when);
  const quick = [liked.size > 0 && item.liked(), ...recent].filter(Boolean).slice(0, 8);
  return frag(
    quick.length > 0 && el('div', { className: 'quick' }, quick.map((it) => el('a', { className: 'quick-tile', href: it.href }, it.art(true), el('span', { textContent: it.name })))),
    shelves(d));
}
// shelves: lo escuchado, lo más escuchado, lo nuevo y los artistas. Inicio y las sugerencias de Buscar.
function shelves(d) {
  const trackCard = (t, i) => {
    const b = el('button', { className: 'card' }, cover(t.albumPath), el('p', { textContent: t.title }), el('p', { className: 'muted', textContent: t.artist }));
    b.setAttribute('aria-label', `Reproducir ${joinText(t.title, t.artist)}`);
    b.onclick = () => playAt(d.top, i);
    return b;
  };
  return frag(
    shelf('Escuchado recientemente', d.albums.filter((a) => a.played).sort((a, b) => b.played - a.played).slice(0, 12).map((a) => card(item.album(a), a.artist))),
    shelf('Lo que más escuchas', d.top.map(trackCard)),
    shelf('Agregado recientemente', [...d.albums].sort((a, b) => b.added - a.added).slice(0, 12).map((a) => card(item.album(a), a.artist))),
    shelf('Tus artistas', [...d.artists].sort((a, b) => b.plays - a.plays).slice(0, 12).map((a) => card(item.artist(a)))));
}

// Buscar: un solo campo. Un enlace de Spotify se ofrece para descargar; cualquier otro texto busca en la biblioteca.

let query = ''; // texto del buscador
let searchTimer;
let searching; // petición de búsqueda en curso: escribir otra letra la cancela
function setQuery(value) {
  clearTimeout(searchTimer);
  query = value.trim();
  if (route().path === 'buscar') render();
}
$('#search').oninput = (e) => {
  clearTimeout(searchTimer);
  $('#search-clear').hidden = !e.target.value;
  // Espera a que deje de escribir; un enlace pegado no tiene nada que autocompletar.
  searchTimer = setTimeout(() => setQuery(e.target.value), spotifyLink(e.target.value) ? 0 : 200);
};
$('#searchbox').onsubmit = (e) => {
  e.preventDefault();
  const v = $('#search').value.trim();
  if (spotifyLink(v)) {
    if (v === query) return $('#view .dl-go')?.click(); // Enter sobre el enlace ya mostrado: descargar
  } else remember(v);
  $('#search').blur();
  setQuery(v);
};
$('#search-clear').onclick = () => { $('#search').value = ''; setQuery(''); $('#search').focus(); };
export function syncSearchBox() {
  const inp = $('#search');
  if (inp.value.trim() !== query) inp.value = query;
  $('#search-clear').hidden = !inp.value;
}

// spotifyLink reconoce enlaces de open.spotify.com (con intl-xx y ?si=) y URIs spotify:, y los deja canónicos.
const SPOTIFY = /open\.spotify\.com\/(?:intl-[A-Za-z-]{2,10}\/)?(track|album|playlist|artist)\/([A-Za-z0-9]{16,40})|spotify:(track|album|playlist|artist):([A-Za-z0-9]{16,40})/;
const KINDS = { track: 'canción', album: 'álbum', playlist: 'playlist', artist: 'artista' };
function spotifyLink(text) {
  const m = SPOTIFY.exec(text);
  if (!m) return null;
  const kind = m[1] || m[3];
  return { kind, url: `https://open.spotify.com/${kind}/${m[2] || m[4]}` };
}

// Búsquedas recientes: solo en este navegador y por usuario.
const recentKey = () => `palobox.searches.${userName}`;
function recentSearches() {
  try {
    const v = JSON.parse(localStorage.getItem(recentKey()));
    return Array.isArray(v) ? v.filter((x) => typeof x === 'string').slice(0, 8) : [];
  } catch { return []; }
}
function saveSearches(list) {
  try { localStorage.setItem(recentKey(), JSON.stringify(list.slice(0, 8))); } catch {} // modo privado o cupo lleno: no se recuerdan
}
function remember(q) {
  if (q.length >= 2) saveSearches([q, ...recentSearches().filter((x) => fold(x) !== fold(q))]);
}

function searchView() {
  searching?.abort();
  const link = spotifyLink(query);
  if (link) return linkView(link);
  return query.length < 2 ? suggestView() : resultsView(query);
}

// Con el campo vacío: búsquedas recientes y sugerencias de la biblioteca. Las descargas se ven en Tu biblioteca.
async function suggestView() {
  const d = await ensureLib().catch(() => null);
  const recents = recentSearches();
  const recentRow = (q) => {
    const b = el('button', { className: 'row' }, el('span', { className: 'type', style: '--tc: var(--muted)' }, icon('search')),
      el('div', { className: 'row-text' }, el('p', { className: 'ellipsis', textContent: q })));
    b.setAttribute('aria-label', `Buscar ${q}`);
    b.onclick = () => { remember(q); setQuery(q); };
    const x = el('button', { className: 'icon-btn' }, icon('x'));
    x.setAttribute('aria-label', `Quitar «${q}» de las búsquedas recientes`);
    x.onclick = () => { saveSearches(recentSearches().filter((r) => r !== q)); render(); $('#search').focus(); };
    return el('li', { className: 'row-wrap' }, b, x);
  };
  return frag(
    recents.length > 0 && frag(heading('Búsquedas recientes'), el('ul', { className: 'list' }, recents.map(recentRow))),
    d?.albums.length ? shelves(d) : empty('search', 'Busca en tu música', 'Artistas, álbumes, canciones o tus playlists. Pega un enlace de Spotify para descargar lo que falta.'));
}

async function resultsView(q) {
  const { signal } = (searching = new AbortController());
  const [found] = await Promise.all([getJSON(`/api/music/search?q=${encodeURIComponent(q)}`, { signal }), ensureLib().catch(() => {})]);
  const { tracks, albums, artists, playlists } = found;
  const out = [];
  const starts = (x) => ` ${fold(x.name)}`.includes(` ${fold(q)}`);
  // Resultado principal: un artista o álbum con una palabra que empiece por lo escrito; si no, la primera canción.
  const best = artists.find(starts) ? item.artist(artists.find(starts)) : albums.find(starts) ? item.album(albums.find(starts))
    : !tracks.length && (artists[0] ? item.artist(artists[0]) : albums[0] ? item.album(albums[0]) : playlists[0] && item.playlist(playlists[0]));
  let top = best && el('a', { className: 'top-result', href: best.href }, best.art(false), el('p', { textContent: best.name }), el('p', { className: 'muted', textContent: best.sub }));
  if (!best && tracks.length) {
    const t = tracks[0];
    top = el('button', { className: 'top-result' }, cover(t.albumPath), el('p', { textContent: t.title }), el('p', { className: 'muted', textContent: joinText('Canción', t.artist) }));
    top.setAttribute('aria-label', `Reproducir ${joinText(t.title, t.artist)}`);
    top.onclick = () => playAt(tracks, 0);
  }
  if (top) {
    out.push(el('div', { className: 'search-top' },
      el('section', {}, heading('Resultado principal'), top),
      tracks.length > 0 && el('section', {}, heading('Canciones'), trackList(tracks.slice(0, 5), { covers: true, sub: (t) => joinText(t.artist, t.album) }))));
  } else out.push(note('Aún no está en PaloBox. Si lo tienes en Spotify, pega aquí el enlace para descargarlo.', 'first'));
  out.push(shelf('Artistas', artists.map((a) => card(item.artist(a)))),
    shelf('Álbumes', albums.map((a) => card(item.album(a), joinText(a.artist, a.year)))),
    shelf('Tus playlists', playlists.map((p) => card(item.playlist(p)))));
  if (tracks.length > 5) out.push(heading('Más canciones'), trackList(tracks.slice(5), { covers: true, sub: (t) => joinText(t.artist, t.album) }));
  // Lo que se abre o reproduce desde aquí queda en las búsquedas recientes.
  return el('div', { onclick: (e) => e.target.closest('a, button') && remember(q) }, out);
}

// Un enlace pegado: la acción destacada es descargarlo, no buscarlo. downloads() solo dice si el servicio está
// activo; la lista con el avance queda en Tu biblioteca.
async function linkView(link) {
  const dl = await downloads();
  const what = KINDS[link.kind];
  if (!dl) {
    return empty('alert', 'No se puede descargar ahora', dl === false
      ? 'Las descargas de Spotify no están activadas en este servidor. Pídele al administrador que las active.'
      : 'El servicio de descargas no responde. Intenta de nuevo en un momento.');
  }
  const btn = el('button', { className: 'btn primary dl-go' }, icon('download'), `Descargar ${what}`);
  btn.onclick = async () => {
    btn.disabled = true;
    const r = await api('/api/spotdl', json('POST', { url: link.url })).catch(() => null);
    btn.disabled = false;
    if (!r?.ok) return toast(r ? await r.text() : OFFLINE, { kind: 'warn' });
    toast('Quedó pedido. El avance lo ves aquí, en Tu biblioteca.', { kind: 'ok' });
    setQuery('');
    go('musica', 'biblioteca'); // el avance de las descargas solo se muestra ahí
  };
  return frag(el('section', { className: 'dl-card' },
    el('span', { className: 'type', style: '--tc: var(--t-audio)' }, icon('download')),
    el('div', { className: 'row-text' }, el('p', { textContent: `Enlace de Spotify: ${what}` }),
      el('p', { className: 'muted', textContent: 'El avance se ve en Tu biblioteca; al terminar aparece en Inicio.' })),
    btn));
}

// Descargas de Spotify (/api/spotdl): el avance se ve solo en Tu biblioteca.
// downloads devuelve { node, paint }, false si el servicio no está activado o null si no responde.
// El servicio decide los estados: queued/downloading corren, error se puede reintentar y
// finished ya no es una fila sino un aviso que se muestra una vez y se borra.
const ACTIVE = { queued: 'En cola', downloading: 'Descargando' };
function jobRow(j, repaint) {
  const active = j.status in ACTIVE;
  const again = j.attempts > 1; // un reintento: solo las canciones que fallaron
  const title = j.title || 'Descarga de Spotify';
  const pct = j.progress == null ? null : Math.round(j.progress * 100);
  const state = j.status === 'downloading' ? joinText(j.total ? `${again ? 'Reintentando' : 'Descargando'} ${j.done} de ${j.total}` : again ? 'Reintentando' : 'Descargando', j.current)
    : j.status === 'queued' ? (again ? 'Reintento en cola' : 'En cola') : j.error;
  const row = el('div', { className: 'row static' }, typeBadge('audio'),
    el('div', { className: 'row-text' }, el('p', { textContent: title }),
      el('p', { className: 'muted', textContent: joinText(state, !j.mine && j.owner && `pedida por ${j.owner}`) }),
      active && pct != null && el('div', { className: 'bar' }, el('span', { style: `width: ${pct}%` }))));
  // Solo quien la pidió (o el admin) la cancela, quita o reintenta; el servicio lo vuelve a validar.
  if (!j.mine) return el('li', { className: 'row-wrap' }, row);
  const send = async (url, opts) => {
    const r = await api(url, opts).catch(() => null);
    if (!r?.ok) return toast(r ? await r.text() : OFFLINE, { kind: 'warn' });
    repaint();
  };
  const del = el('button', { className: 'icon-btn' }, icon(active ? 'x' : 'trash'));
  del.setAttribute('aria-label', `${active ? 'Cancelar la descarga de' : 'Quitar de la lista'} ${title}`);
  del.onclick = async () => {
    if (active && !(await ask({ heading: `¿Cancelar «${j.title || 'esta descarga'}»?`, text: 'Lo que ya bajó se queda en la biblioteca.', ok: 'Cancelar descarga', danger: true }))) return;
    send(`/api/spotdl/${j.id}`, { method: 'DELETE' });
  };
  if (j.status !== 'error') return el('li', { className: 'row-wrap' }, row, del);
  // En celular solo el ícono (con aria-label), para que el mensaje de la fila tenga espacio.
  const retry = el('button', { className: 'btn secondary' }, icon('repeat'), el('span', { className: 'wide', textContent: 'Reintentar' }));
  retry.setAttribute('aria-label', `Reintentar lo que falló de ${title}`);
  retry.onclick = () => send(`/api/spotdl/${j.id}/retry`, { method: 'POST' });
  return el('li', { className: 'row-wrap' }, row, retry, del);
}

let dlGen = 0; // solo la última sección de descargas pintada sigue consultando
let dlTimer;
const shown = new Set(); // avisos ya mostrados en esta pestaña, por si el borrado tarda
async function downloads() {
  const jobs = el('section', {});
  const gen = ++dlGen;
  let busy = false; // había descargas activas en la pasada anterior
  const paint = async () => {
    clearTimeout(dlTimer);
    const r = await api('/api/spotdl').catch(() => null);
    if (gen !== dlGen) return null;
    if (!r?.ok) return null; // las descargas son un extra: si fallan, la vista se muestra igual
    const { enabled, items } = await r.json();
    if (!enabled) return false;
    const rows = items.filter((j) => j.status !== 'finished');
    const news = items.filter((j) => j.status === 'finished' && !shown.has(j.id));
    const live = rows.some((j) => j.status in ACTIVE);
    jobs.replaceChildren(...(rows.length ? [heading('Descargas', rows.length), el('ul', { className: 'list' }, rows.map((j) => jobRow(j, paint)))] : []));
    if (news.length) {
      // El servicio guarda el aviso hasta que se ve; al mostrarlo se borra.
      for (const n of news) { shown.add(n.id); api(`/api/spotdl/${n.id}`, { method: 'DELETE' }).catch(() => {}); }
      toast(news.map((n) => n.notice).join(' '), { kind: news.some((n) => n.error) ? 'warn' : 'ok' });
    }
    if ((busy && !live) || news.length) {
      // Terminó algo: la barra lateral muestra lo nuevo y Tu biblioteca se vuelve a pintar.
      library().then(() => news.length && jobs.isConnected && route().path === 'biblioteca' && render()).catch(() => {});
    }
    busy = live;
    if (live) tick();
    return true;
  };
  // Mientras haya descargas activas se consulta cada 4 s, aunque se cambie de vista (así llega el
  // aviso); con el foco en la lista se espera, para no quitárselo al teclado.
  const tick = () => {
    dlTimer = setTimeout(() => gen === dlGen && (jobs.contains(document.activeElement) ? tick() : paint()), 4000);
  };
  const ok = await paint();
  return ok && { node: jobs, paint };
}

// Tu biblioteca: lo que el usuario guardó (ver libItems), en lista o cuadrícula.

let filter = ''; // '' = todo; 'playlists' | 'artists' | 'albums'
let recent = true; // orden: recientes o alfabético
let grid = (() => { try { return localStorage.getItem('palobox.libgrid') === '1'; } catch { return false; } })();

async function libraryView() {
  const [d, dl] = await Promise.all([library(), downloads()]);
  const box = el('div', {});
  const chips = [['playlists', 'Playlists'], ['artists', 'Artistas'], ['albums', 'Álbumes']].map(([k, text]) => {
    const b = el('button', { className: 'chip', textContent: text });
    b.onclick = () => { filter = filter === k ? '' : k; paint(); };
    b.dataset.k = k;
    return b;
  });
  const order = el('button', { className: 'btn ghost' });
  order.onclick = () => { recent = !recent; paint(); };
  const layout = el('button', { className: 'icon-btn' });
  layout.onclick = () => {
    grid = !grid;
    try { localStorage.setItem('palobox.libgrid', grid ? '1' : '0'); } catch {}
    paint();
  };
  const add = el('button', { className: 'btn secondary' }, icon('plus'), 'Crear playlist');
  add.onclick = () => createPlaylist();
  // Los filtros repintan la lista sin volver a pedir la biblioteca.
  function paint() {
    for (const b of chips) b.setAttribute('aria-pressed', filter === b.dataset.k);
    order.replaceChildren(icon(recent ? 'down' : 'up'), recent ? 'Recientes' : 'Alfabético');
    order.setAttribute('aria-label', `Orden: ${recent ? 'recientes primero' : 'alfabético'}. Cambiar`);
    layout.replaceChildren(icon(grid ? 'list' : 'grid'));
    layout.setAttribute('aria-label', grid ? 'Ver como lista' : 'Ver como cuadrícula');
    const items = libItems(d, { show: filter, recent });
    box.replaceChildren(grid ? el('div', { className: 'cards compact' }, items.map((it) => card(it)))
      : el('ul', { className: 'list' }, items.map((it) => el('li', {}, libRow(it)))));
  }
  paint();
  const filters = el('div', { className: 'toolbar' }, chips);
  filters.setAttribute('role', 'group');
  filters.setAttribute('aria-label', 'Filtrar la biblioteca');
  return frag(filters,
    el('div', { className: 'toolbar' }, order, el('span', { className: 'spacer' }), layout, add),
    dl?.node, dl && heading('Biblioteca'), box,
    !d.playlists.length && !d.albums.some((a) => a.saved) && !d.artists.some((a) => a.saved) &&
      note('Aquí queda lo que guardas: toca + en un álbum o Seguir en un artista. Lo encuentras en Inicio y en Buscar.', 'first'));
}

// Páginas de álbum, artista y playlist: encabezado grande, botones de reproducir y lista de pistas

// tint tiñe el encabezado con el color medio de la carátula; si el canvas falla queda el fondo normal.
function tint(hero, art) {
  art.querySelector('img')?.addEventListener('load', (e) => {
    try {
      const c = el('canvas', { width: 8, height: 8 }).getContext('2d');
      c.drawImage(e.target, 0, 0, 8, 8);
      const px = c.getImageData(0, 0, 8, 8).data;
      const sum = [0, 0, 0];
      for (let i = 0; i < px.length; i += 4) for (let k = 0; k < 3; k++) sum[k] += px[i + k];
      hero.style.setProperty('--tint', `rgb(${sum.map((v) => Math.round(v / 64)).join(' ')})`);
    } catch {}
  });
}

// page arma una página de colección. meta: textos o nodos bajo el título; actions: botones extra; body: lo que va bajo los botones.
function page({ label, title, art, meta, tracks, actions = [], body }) {
  document.title = `${title} · PaloBox`;
  const hero = el('header', { className: 'hero' }, art, el('div', { className: 'hero-text' },
    el('p', { className: 'hero-label', textContent: label }), el('h1', { textContent: title }),
    el('p', { className: 'hero-meta' }, meta.filter(Boolean).flatMap((m, i) => (i ? [' · ', m] : [m])))));
  tint(hero, art);
  const play = el('button', { className: 'icon-btn play-big' }, icon('play'));
  play.setAttribute('aria-label', `Reproducir ${title}`);
  play.disabled = !tracks.length;
  play.onclick = () => playAt(tracks, isShuffle() ? Math.floor(Math.random() * tracks.length) : 0);
  const mix = el('button', { className: 'icon-btn toggle' }, icon('shuffle'));
  mix.dataset.act = 'shuffle';
  mix.setAttribute('aria-label', 'Aleatorio');
  mix.setAttribute('aria-pressed', isShuffle());
  return frag(hero, el('div', { className: 'actions' }, play, mix, actions), body);
}
const total = (tracks) => { const s = tracks.reduce((n, t) => n + t.dur, 0); return s > 0 && fmtLong(s); };
const songs = (n) => plural(n, 'canción', 'canciones');

async function albumView(path) {
  const [al] = await Promise.all([getJSON(`/api/music/album?path=${encodeURIComponent(path)}`), ensureLib().catch(() => {})]);
  if (!al) return gone('Este álbum ya no está');
  return page({
    label: 'Álbum', title: al.name, art: cover(al.path), tracks: al.tracks, actions: [saveButton('album', al)],
    meta: [al.artistPath && el('a', { href: href('musica', `artista/${al.artistPath}`), textContent: al.artist }), al.year || null, songs(al.tracks.length), total(al.tracks)],
    body: trackList(al.tracks),
  });
}

async function artistView(path) {
  const [ar] = await Promise.all([getJSON(`/api/music/artist?path=${encodeURIComponent(path)}`), ensureLib().catch(() => {})]);
  if (!ar) return gone('Este artista ya no está');
  return page({
    label: 'Artista', title: ar.name, art: cover(ar.path, { round: true, glyph: 'user' }), tracks: ar.tracks, actions: [saveButton('artist', ar)],
    meta: [plural(ar.albums.length, 'álbum', 'álbumes'), songs(ar.tracks.length)],
    body: frag(heading('Populares'), trackList(ar.popular, { covers: true, sub: (t) => t.album }),
      heading('Discografía'), el('div', { className: 'cards' }, ar.albums.map((a) => card(item.album(a), joinText(a.year || null, songs(a.tracks)))))),
  });
}

const emptyList = () => empty('music', 'Aún no hay canciones aquí', 'Agrégalas desde el menú de cualquier canción.', linkBtn('Buscar música', href('musica', 'buscar'), 'search'));

async function likedView() {
  const tracks = await getJSON('/api/music/likes');
  await ensureLib().catch(() => {});
  return page({
    label: 'Playlist', title: 'Canciones que te gustan', art: el('div', { className: 'cover liked' }, icon('heart')), tracks,
    meta: [songs(tracks.length), total(tracks)],
    body: tracks.length ? trackList(tracks, { covers: true, sub: (t) => joinText(t.artist, t.album) })
      : empty('heart', 'Aún no te gusta ninguna', 'Toca el corazón de una canción y queda guardada aquí.', linkBtn('Buscar música', href('musica', 'buscar'), 'search')),
  });
}

async function playlistView(id) {
  const base = `/api/music/playlists/${encodeURIComponent(id)}`;
  const [pl] = await Promise.all([getJSON(base), ensureLib().catch(() => {})]);
  if (!pl) return gone('Esta playlist ya no está');
  const { tracks } = pl;
  const send = async (url, opts, fail) => {
    const r = await api(url, opts).catch(() => null);
    if (!r?.ok) return toast(r ? fail : OFFLINE, { kind: 'warn' });
    await library().catch(() => {});
    return true;
  };
  const move = async (from, to) => {
    const paths = tracks.map((t) => t.path);
    paths.splice(to, 0, ...paths.splice(from, 1));
    if (await send(`${base}/order`, json('PUT', { paths }), 'No se pudo cambiar el orden.')) render();
  };
  const more = el('button', { className: 'icon-btn' }, icon('more'));
  more.setAttribute('aria-label', `Más acciones para ${pl.name}`);
  more.setAttribute('aria-haspopup', 'menu');
  more.onclick = () => menu(more, `Acciones de ${pl.name}`, [
    { icon: 'edit', label: 'Cambiar nombre', run: async () => {
      const name = await ask({ heading: 'Cambiar nombre', input: pl.name, ok: 'Guardar' });
      if (name && name !== pl.name && await send(base, json('PATCH', { name }), 'No se pudo cambiar el nombre.')) render();
    } },
    { icon: 'trash', label: 'Eliminar playlist', danger: true, run: async () => {
      if (!(await ask({ heading: `¿Eliminar «${pl.name}»?`, text: 'Las canciones siguen en la biblioteca.', ok: 'Eliminar', danger: true }))) return;
      if (await send(base, { method: 'DELETE' }, 'No se pudo eliminar la playlist.')) go('musica', 'biblioteca');
    } },
  ]);
  return page({
    label: 'Playlist', title: pl.name, art: cover(tracks.length ? tracks[0].albumPath : null), tracks, actions: [more],
    meta: [songs(tracks.length), total(tracks)],
    body: !tracks.length ? emptyList() : trackList(tracks, {
      covers: true, sub: (t) => joinText(t.artist, t.album), onMove: move,
      extra: (t, i) => [
        i > 0 && { icon: 'up', label: 'Subir', run: () => move(i, i - 1) },
        i < tracks.length - 1 && { icon: 'down', label: 'Bajar', run: () => move(i, i + 1) },
        { icon: 'x', label: 'Quitar de esta playlist', danger: true, run: async () => {
          if (await send(`${base}/tracks?path=${encodeURIComponent(t.path)}`, { method: 'DELETE' }, 'No se pudo quitar la canción.')) render();
        } },
      ],
    }),
  });
}
