// Música: estado compartido (biblioteca, me gusta, playlists) y las piezas que repiten las vistas.
import { $, el, icon, json, joinText, plural, clock, lazyImg, toast, ask, menu } from './lib.js';
import { route, href, go, thumbUrl, render } from './nav.js';
import { api } from './session.js';
import { playAt, playNext, enqueue } from './player.js';

const OFFLINE = 'Sin conexión con el servidor.';
export const liked = new Set(); // rutas de las pistas con me gusta
let lib = null; // última respuesta de /api/music
let saving = 0; // me gusta enviados que el servidor aún no confirma

// library trae álbumes, artistas, playlists y me gusta, y con eso repinta la barra lateral.
export async function library() {
  const r = await api('/api/music');
  if (!r.ok) throw new Error('Intenta de nuevo en un momento.');
  lib = await r.json();
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
    { icon: 'list-plus', label: 'Agregar a playlist…', run: async () => {
      const { playlists } = await ensureLib().catch(() => ({ playlists: [] }));
      menu(anchor, 'Agregar a playlist', [
        { icon: 'plus', label: 'Nueva playlist', run: () => createPlaylist(t) },
        ...playlists.map((p) => ({ icon: 'music', label: p.name, run: () => addTo(p, t) })),
      ]);
    } },
    t.artistPath && here !== `artista/${t.artistPath}` && { icon: 'user', label: 'Ir al artista', run: () => go('musica', `artista/${t.artistPath}`) },
    here !== `album/${t.albumPath}` && { icon: 'disc', label: 'Ir al álbum', run: () => go('musica', `album/${t.albumPath}`) },
    ...extra,
  ]);
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
  return el('ul', { className: `list tracks${covers ? ' covers' : ''}` }, tracks.map((t, i) => {
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
  }));
}
