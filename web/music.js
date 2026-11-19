// Música: estado compartido (biblioteca, me gusta, playlists) y las piezas que repiten las vistas.
import { $, el, icon, json, joinText, plural, clock, lazyImg, toast, ask, menu } from './lib.js';
import { route, href, go, thumbUrl, render } from './nav.js';
import { api } from './session.js';
import { playAt, playNext, enqueue, audio } from './player.js';
import * as offline from './offline.js';

const OFFLINE = 'Sin conexión con el servidor.';
export const liked = new Set(); // rutas de las pistas con me gusta
let lib = null; // última respuesta de /api/music
let saving = 0; // me gusta enviados que el servidor aún no confirma

// library trae álbumes, artistas, playlists y me gusta, y con eso repinta la barra lateral.
export async function library() {
  const r = await api('/api/music');
  if (!r.ok) throw new Error('Intenta de nuevo en un momento.');
  lib = await r.json();
  offline.remember(lib); // lo que la biblioteca dice de lo descargado, para verlo igual sin conexión
  if (!saving) { // con un me gusta en camino, esta respuesta puede ser anterior a él
    liked.clear();
    for (const p of lib.likes) liked.add(p);
    markLikes();
  }
  paintSide();
  return lib;
}
export const ensureLib = () => (lib ? Promise.resolve(lib) : library());
// forget suelta lo del usuario al salir: el siguiente no ve su biblioteca ni sus me gusta.
export function forget() {
  lib = null;
  liked.clear();
  $('#side-list').replaceChildren();
}

// Me gusta

export function markLikes() {
  for (const b of document.querySelectorAll('[data-like]')) b.setAttribute('aria-pressed', liked.has(b.dataset.like));
}
export async function toggleLike(path) {
  const on = !liked.has(path);
  const set = (v) => { v ? liked.add(path) : liked.delete(path); markLikes(); paintSide(); };
  set(on); // se pinta de una vez; si el servidor falla, se devuelve
  saving++;
  const r = await api(`/api/music/likes?path=${encodeURIComponent(path)}`, { method: on ? 'PUT' : 'DELETE' }).catch(() => null);
  saving--;
  if (!r?.ok) { set(!on); return toast('No se pudo guardar el me gusta.', { kind: 'warn' }); }
  if (route().path === 'gustan') render();
}
function likeButton(t) {
  const b = el('button', { className: 'icon-btn like' }, icon('heart'));
  b.dataset.like = t.path;
  b.setAttribute('aria-label', `Me gusta: ${t.title}`);
  b.setAttribute('aria-pressed', liked.has(t.path));
  b.onclick = () => toggleLike(t.path);
  return b;
}

// Playlists

// createPlaylist pide el nombre y la crea; con first, además le agrega esa pista.
export async function createPlaylist(first) {
  const name = await ask({ heading: 'Nueva playlist', input: '', ok: 'Crear' });
  if (!name) return;
  const r = await api('/api/music/playlists', json('POST', { name })).catch(() => null);
  if (!r?.ok) return toast(r ? await r.text() : OFFLINE, { kind: 'warn' });
  const p = await r.json();
  if (first) return addTo(p, first);
  await library().catch(() => {});
  go('musica', `playlist/${p.id}`);
}
async function addTo(p, t) {
  const r = await api(`/api/music/playlists/${p.id}/tracks`, json('POST', { path: t.path })).catch(() => null);
  if (!r?.ok) return toast(r ? 'No se pudo agregar a la playlist.' : OFFLINE, { kind: 'warn' });
  toast((await r.json()).added ? `«${t.title}» se agregó a ${p.name}.` : `«${t.title}» ya estaba en ${p.name}.`, { kind: 'ok' });
  library().catch(() => {});
  if (route().path === `playlist/${p.id}`) render();
}
$('#side-new').onclick = () => createPlaylist();

// Menú de una pista; extra son las acciones propias de la vista (p. ej. quitar de la playlist).
function trackMenu(anchor, t, extra = []) {
  const here = route().path;
  menu(anchor, `Acciones de ${t.title}`, [
    { icon: 'skip', label: 'Reproducir a continuación', run: () => playNext(t) },
    { icon: 'queue', label: 'Agregar a la cola', run: () => enqueue(t) },
    !offline.active && { icon: 'list-plus', label: 'Agregar a playlist…', run: async () => {
      const { playlists } = await ensureLib().catch(() => ({ playlists: [] }));
      menu(anchor, 'Agregar a playlist', [
        { icon: 'plus', label: 'Nueva playlist', run: () => createPlaylist(t) },
        ...playlists.map((p) => ({ icon: 'music', label: p.name, run: () => addTo(p, t) })),
      ]);
    } },
    t.artistPath && here !== `artista/${t.artistPath}` && { icon: 'user', label: 'Ir al artista', run: () => go('musica', `artista/${t.artistPath}`) },
    here !== `album/${t.albumPath}` && { icon: 'disc', label: 'Ir al álbum', run: () => go('musica', `album/${t.albumPath}`) },
    ...(extra || []),
    canDownload() && (offline.has(t.path)
      ? { icon: 'trash', label: 'Quitar de las descargas', run: async () => { await offline.remove([t.path]); toast(`«${t.title}» ya no está en este celular.`, { kind: 'ok' }); render(); } }
      : { icon: 'download', label: 'Descargar', run: () => fetchOffline([t]) }),
  ]);
}

// Descargas para escuchar sin conexión (offline.js). Solo con conexión y en cuentas que pueden descargar.
const canDownload = () => offline.enabled() && !offline.active;
// keepAwake mantiene viva la app mientras descarga aunque el usuario salga de ella (sin cerrarla). El celular
// detiene una app web en segundo plano salvo que esté reproduciendo audio: si no hay música sonando, suena un
// silencio en bucle hasta que la descarga termina. Tiene que empezar dentro del toque que pide la descarga.
let silence = null;
function keepAwake(on) {
  if (!on) return void silence?.pause();
  if (!audio.paused) return; // con música sonando la app ya sigue viva
  if (!silence) {
    const wav = new Uint8Array(44 + 8000).fill(128); // un segundo de silencio: PCM de 8 bits, mono, 8 kHz
    const view = new DataView(wav.buffer);
    [[0, 0x52494646], [8, 0x57415645], [12, 0x666d7420], [36, 0x64617461]].forEach(([at, tag]) => view.setUint32(at, tag)); // RIFF WAVE fmt data
    [[4, 36 + 8000], [16, 16], [24, 8000], [28, 8000], [40, 8000]].forEach(([at, n]) => view.setUint32(at, n, true));
    [[20, 1], [22, 1], [32, 1], [34, 8]].forEach(([at, n]) => view.setUint16(at, n, true));
    silence = new Audio(URL.createObjectURL(new Blob([wav], { type: 'audio/wav' })));
    silence.loop = true;
  }
  silence.play().catch(() => {}); // sin toque (al retomar una descarga) no arranca: la app está a la vista y no hace falta
}
let downloading = false;
// resumeDownloads retoma lo que quedó a medias si la app se cerró o el sistema la detuvo.
export function resumeDownloads() {
  const left = offline.pending();
  if (!downloading && left.length && canDownload() && document.visibilityState === 'visible') fetchOffline(left);
}
document.addEventListener('visibilitychange', resumeDownloads);
// fetchOffline baja las pistas al celular mostrando el avance; playlist: la lista que se descarga, para recordarla.
async function fetchOffline(tracks, playlist) {
  if (playlist) offline.keepPlaylist(playlist, tracks); // desde ya: si la descarga se retoma después, la playlist ya está anotada
  if (downloading) {
    offline.queue(tracks);
    return void toast('Ya hay una descarga en curso; esta sigue cuando termine.');
  }
  downloading = true;
  keepAwake(true);
  const missing = tracks.filter((t) => !offline.has(t.path)).length;
  let failed = false;
  try {
    await offline.download(tracks, (done, total) => total > 1 && toast(`Descargando ${done} de ${total}… Puedes salir de la app, sin cerrarla.`, { sticky: true }));
    offline.remember(lib ?? { albums: [], artists: [], playlists: [], likes: [], top: [] });
    toast(missing ? `${plural(missing, 'canción lista', 'canciones listas')} para escuchar sin conexión.` : 'Ya estaba todo descargado.', { kind: 'ok' });
  } catch (e) {
    failed = true; // lo pendiente queda anotado y se retoma al volver a la app, no en bucle
    toast(e.name === 'QuotaExceededError' ? 'No hay espacio en este celular para descargar más.' : e.message || OFFLINE, { kind: 'warn', sticky: true });
  }
  downloading = false;
  keepAwake(false);
  const queued = offline.pending(); // lo que se pidió mientras tanto
  if (!failed && queued.length && canDownload()) return fetchOffline(queued);
  render();
}
// downloadButton descarga (o quita) todas las pistas de un álbum o una playlist.
export function downloadButton(tracks, playlist) {
  if (!canDownload() || !tracks.length) return null;
  // Una playlist está descargada si además se eligió tenerla: sus canciones pueden estar por un álbum y ella no.
  const all = tracks.every((t) => offline.has(t.path)) && (!playlist || offline.keeps(playlist.id));
  const b = el('button', { className: 'icon-btn toggle' }, icon('download'));
  b.setAttribute('aria-pressed', all);
  b.setAttribute('aria-label', all ? 'Quitar de las descargas' : 'Descargar para escuchar sin conexión');
  b.onclick = async () => {
    if (!all) { b.disabled = true; return fetchOffline(tracks, playlist); }
    if (!(await ask({ heading: '¿Quitar de las descargas?', text: `${plural(tracks.length, 'canción deja', 'canciones dejan')} de estar en este celular. Siguen en la biblioteca.`, ok: 'Quitar', danger: true }))) return;
    if (playlist) offline.dropPlaylist(playlist.id);
    await offline.remove(tracks.map((t) => t.path));
    render();
  };
  return b;
}
// offlineBox resume lo descargado en Tu biblioteca y deja borrarlo todo.
export function offlineBox() {
  const n = offline.count();
  if (!offline.enabled() || !n) return null;
  const mb = Math.round(offline.bytes() / 1048576);
  const row = el('div', { className: 'row static' }, el('span', { className: 'type', style: '--tc: var(--t-audio)' }, icon('download')),
    el('div', { className: 'row-text' }, el('p', { textContent: `${plural(n, 'canción descargada', 'canciones descargadas')} · ${mb} MB` }),
      el('p', { className: 'muted', textContent: offline.active ? 'Estás sin conexión: esto es lo que hay en este celular.' : 'Disponibles en este celular aunque no haya conexión.' })));
  if (offline.active) return el('ul', { className: 'list offline-box' }, el('li', {}, row));
  const del = el('button', { className: 'icon-btn' }, icon('trash'));
  del.setAttribute('aria-label', 'Borrar todas las descargas');
  del.onclick = async () => {
    if (!(await ask({ heading: '¿Borrar todas las descargas?', text: `Se liberan ${mb} MB de este celular. La música sigue en la biblioteca.`, ok: 'Borrar', danger: true }))) return;
    const user = offline.saved();
    await offline.wipe();
    if (user) await offline.login(user); // sigue siendo su celular: solo se van las canciones
    render();
  };
  return el('ul', { className: 'list offline-box' }, el('li', { className: 'row-wrap' }, row, del));
}

// Piezas de las vistas

// cover pinta la carátula de un álbum, artista o pista; path null deja solo el ícono.
export function cover(path, { round = false, small = false, glyph = 'music' } = {}) {
  return el('div', { className: `cover${round ? ' round' : ''}${small ? ' small' : ''}`, style: '--tc: var(--t-audio)' },
    icon(glyph), path != null && lazyImg(thumbUrl('musica', path)));
}
const likedCover = (small) => el('div', { className: `cover liked${small ? ' small' : ''}` }, icon('heart'));

// Lo que puede haber en la biblioteca, reducido a lo que necesita una tarjeta o una fila.
export const item = {
  liked: () => ({ href: href('musica', 'gustan'), name: 'Canciones que te gustan', sub: joinText('Playlist', plural(liked.size, 'canción', 'canciones')), when: Infinity, art: likedCover }),
  playlist: (p) => ({ href: href('musica', `playlist/${p.id}`), name: p.name, sub: joinText('Playlist', plural(p.tracks, 'canción', 'canciones')), when: p.updated,
    art: (small) => cover(p.tracks ? p.cover : null, { small }) }),
  album: (a) => ({ href: href('musica', `album/${a.path}`), name: a.name, sub: joinText('Álbum', a.artist), when: Math.max(a.played, a.saved), art: (small) => cover(a.path, { small }) }),
  artist: (a) => ({ href: href('musica', `artista/${a.path}`), name: a.name, sub: 'Artista', when: Math.max(a.played, a.saved), round: true,
    art: (small) => cover(a.path, { small, round: true, glyph: 'user' }) }),
};
export const card = (it, sub = it.sub) => el('a', { className: `card${it.round ? ' center' : ''}`, href: it.href },
  it.art(false), el('p', { textContent: it.name }), sub && el('p', { className: 'muted', textContent: sub }));
export const libRow = (it) => el('a', { className: 'row', href: it.href }, it.art(true),
  el('div', { className: 'row-text' }, el('p', { className: 'ellipsis', textContent: it.name }), el('p', { className: 'muted ellipsis', textContent: it.sub })));

// Barra lateral de escritorio: lo mismo que Tu biblioteca sin filtro.
function paintSide() {
  if (!lib) return;
  $('#side-list').replaceChildren(...libItems(lib).map((it) => el('li', {}, libRow(it))));
}
// libItems arma Tu biblioteca: me gusta, playlists, artistas que sigues y álbumes guardados, como en
// Spotify; el resto de la música compartida está en Inicio y Buscar. show = 'playlists' | 'artists' | 'albums' o vacío.
export function libItems(d, { show = '', recent = true } = {}) {
  const pick = (k) => !show || show === k;
  const items = [...(pick('playlists') ? d.playlists.map(item.playlist) : []), ...(pick('artists') ? d.artists.filter((a) => a.saved).map(item.artist) : []),
    ...(pick('albums') ? d.albums.filter((a) => a.saved).map(item.album) : [])];
  items.sort(recent ? (a, b) => b.when - a.when : (a, b) => a.name.localeCompare(b.name, 'es'));
  if (pick('playlists')) items.unshift(item.liked()); // siempre fija arriba
  return items;
}

// saveButton guarda un álbum (+) o sigue a un artista (Seguir) en Tu biblioteca. x es la respuesta de
// /api/music/album o /artist: trae path, name y saved (ms, 0 = no guardado).
export function saveButton(kind, x) {
  if (offline.active) return null; // guardar, seguir, me gusta y editar playlists necesitan conexión
  const album = kind === 'album';
  const b = el('button', { className: album ? 'icon-btn toggle' : 'btn secondary small' });
  const paint = () => {
    const on = x.saved > 0;
    b.setAttribute('aria-pressed', on);
    if (!album) return void (b.textContent = on ? 'Siguiendo' : 'Seguir');
    b.replaceChildren(icon(on ? 'check' : 'plus'));
    b.setAttribute('aria-label', on ? 'Quitar de Tu biblioteca' : 'Guardar en Tu biblioteca');
  };
  b.onclick = async () => {
    const on = !(x.saved > 0);
    b.disabled = true;
    const r = await api(`/api/music/saved?kind=${kind}&path=${encodeURIComponent(x.path)}`, { method: on ? 'PUT' : 'DELETE' }).catch(() => null);
    b.disabled = false;
    if (!r?.ok) return toast(r ? 'No se pudo cambiar Tu biblioteca.' : OFFLINE, { kind: 'warn' });
    x.saved = on ? Date.now() : 0;
    paint();
    toast(album ? (on ? 'Se guardó en Tu biblioteca.' : 'Se quitó de Tu biblioteca.') : (on ? `Ahora sigues a ${x.name}.` : `Dejaste de seguir a ${x.name}.`), { kind: 'ok' });
    library().catch(() => {}); // repinta la barra lateral
  };
  paint();
  return b;
}

// trackList pinta pistas reproducibles. covers: carátula junto al título (listas de varios álbumes);
// sub: texto bajo el título; extra(t, i): acciones de menú de la vista; onMove(de, a): permite reordenar arrastrando.
export function trackList(tracks, { covers = false, sub = (t) => t.artist, extra, onMove } = {}) {
  return swipeQueue(tracks, el('ul', { className: `list tracks${covers ? ' covers' : ''}` }, tracks.map((t, i) => {
    const b = el('button', { className: 'row' }, el('span', { className: 'num', textContent: i + 1 }), covers && cover(t.albumPath, { small: true }),
      el('div', { className: 'row-text' }, el('p', { className: 'ellipsis', textContent: t.title }), el('p', { className: 'muted ellipsis', textContent: sub(t) })));
    b.dataset.track = t.path;
    b.setAttribute('aria-label', `Reproducir ${joinText(t.title, t.artist)}`);
    b.onclick = () => playAt(tracks, i);
    const more = el('button', { className: 'icon-btn more' }, icon('more'));
    more.setAttribute('aria-label', `Más acciones para ${t.title}`);
    more.setAttribute('aria-haspopup', 'menu');
    more.onclick = () => trackMenu(more, t, extra?.(t, i));
    const li = el('li', { className: 'row-wrap' }, b, likeButton(t), el('span', { className: 'dur muted', textContent: t.dur ? clock(t.dur) : '' }), more);
    if (onMove) {
      li.draggable = true;
      li.ondragstart = (e) => e.dataTransfer.setData('text/plain', i);
      li.ondragover = (e) => e.preventDefault();
      li.ondrop = (e) => {
        e.preventDefault();
        const from = Number(e.dataTransfer.getData('text/plain'));
        if (tracks[from] && from !== i) onMove(from, i);
      };
    }
    return li;
  })));
}

// Deslizar una fila a la izquierda con el dedo la agrega a la cola, como las acciones de iOS. El mouse tiene el menú.
function swipeQueue(tracks, ul) {
  let d = null; // { id, x, y, li, on }
  let swallowUntil = 0; // el click que sigue al gesto no reproduce la pista
  ul.addEventListener('pointerdown', (e) => {
    const li = e.target.closest('li');
    if (e.pointerType !== 'mouse' && e.isPrimary && li) d = { id: e.pointerId, x: e.clientX, y: e.clientY, li, on: false };
  });
  ul.addEventListener('pointermove', (e) => {
    if (d?.id !== e.pointerId) return;
    const dx = e.clientX - d.x;
    if (!d.on) {
      if (Math.hypot(dx, e.clientY - d.y) < 12) return; // todavía puede ser un toque
      if (-dx < Math.abs(e.clientY - d.y) * 1.5) return (d = null); // desplazamiento vertical o hacia la derecha
      d.on = true;
      d.li.classList.add('swipe');
      ul.setPointerCapture(e.pointerId);
    }
    d.li.style.transition = 'none';
    d.li.style.transform = `translateX(${Math.max(Math.min(0, dx), -96)}px)`;
  });
  const end = (e) => {
    if (d?.id !== e.pointerId) return;
    const { li, on, x } = d;
    d = null;
    if (!on) return;
    swallowUntil = e.timeStamp + 400;
    li.style.transition = 'transform .2s';
    li.style.transform = '';
    li.addEventListener('transitionend', () => li.classList.remove('swipe'), { once: true });
    if (e.type === 'pointerup' && x - e.clientX > 72) enqueue(tracks[[...ul.children].indexOf(li)]);
  };
  ul.addEventListener('pointerup', end);
  ul.addEventListener('pointercancel', end);
  ul.addEventListener('click', (e) => {
    if (e.timeStamp > swallowUntil) return;
    swallowUntil = 0;
    e.stopPropagation();
    e.preventDefault();
  }, true);
  return ul;
}
