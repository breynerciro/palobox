// Modo sin conexión (todas las cuentas): descargar música al celular y usar la app sin servidor. Sin conexión toda
// cuenta queda como de solo música: Fotos, Archivos y Administración necesitan el servidor.
//
// - El audio se guarda ya «limpio» (/api/music/offline: solo cuadros MP3, sin etiquetas) en la caché del
//   navegador. Así varias canciones se unen una tras otra en un solo audio (player.js), que es lo que deja pasar
//   de canción con la pantalla bloqueada en iPhone sin cargar nada.
// - Los datos (qué está descargado, sus álbumes, playlists y me gusta) van en localStorage.
// - Sin conexión, api() (session.js) no va a la red: handle() responde lo mismo que respondería el servidor,
//   pero armado solo con lo descargado. Las pantallas no cambian: reciben menos música.
// Jam, Connect, descargas de Spotify y cualquier cambio (me gusta, playlists) necesitan conexión.
const AUDIO = 'palobox-offline-audio';
const KEY = 'palobox.offline';
const HZ = 48000; // todo lo descargado es MPEG-1 capa III a 48 kHz en estéreo (StreamHz en el servidor)
const FRAME = 1152 / HZ; // segundos de cada cuadro MP3
const VERSION = 2; // formato de lo guardado: si cambia, lo descargado antes no se puede unir con lo nuevo
const MAX_AWAY = 30 * 24 * 3600 * 1000; // sin pasar por el servidor en este tiempo (lo que dura una sesión), ya no se entra
const blank = () => ({ v: VERSION, user: '', seen: 0, tracks: {}, albums: {}, artists: {}, playlists: {}, likes: [], top: [], recent: [], pending: [] });
let state = blank();
try {
  const kept = JSON.parse(localStorage.getItem(KEY));
  if (kept?.v === VERSION) state = { ...blank(), seen: Date.now(), ...kept }; // seen: lo guardado antes de que existiera cuenta desde hoy
  else if (kept) { // descargas de un formato anterior: se sueltan (no se pueden unir con las nuevas); la cuenta sigue
    state = { ...blank(), user: kept.user ?? '' };
    localStorage.setItem(KEY, JSON.stringify(state));
    caches.delete(AUDIO);
  }
} catch {}
// store guarda el estado y dice si se pudo: el cupo de localStorage se puede llenar.
const store = () => { try { localStorage.setItem(KEY, JSON.stringify(state)); return true; } catch { return false; } };
const held = new Map(); // ruta → Blob ya leído de la caché: rearmar el audio unido no vuelve a consultarla

export let active = false; // la app está funcionando sin servidor
export const audioUrl = (path) => `/api/music/offline?path=${encodeURIComponent(path)}`;
const thumb = (dir) => `/api/media/thumb?lib=musica&path=${encodeURIComponent(dir)}`;
export const enabled = () => !!state.user; // quién puede descargar: cualquiera que inició sesión en este celular
export const has = (path) => !!state.tracks[path];
export const count = () => Object.keys(state.tracks).length;
export const bytes = () => Object.values(state.tracks).reduce((n, x) => n + x.bytes, 0);
// saved: el usuario que puede entrar sin conexión en este celular: el último que inició sesión, como cuenta de
// solo música. Entra aunque todavía no haya bajado nada; verá la biblioteca vacía.
// Como la cuenta no se puede comprobar sin servidor, solo vale un tiempo desde la última vez que sí se comprobó.
export const saved = () => (state.user && enabled() && Date.now() - state.seen < MAX_AWAY ? { name: state.user, admin: false, musicOnly: true } : null);
// tracks: todo lo descargado, ordenado por artista, álbum y número de pista (la playlist «Canciones descargadas»).
export const tracks = () => all().sort((a, b) => (a.artistPath || '').localeCompare(b.artistPath || '', 'es') || (a.albumPath || '').localeCompare(b.albumPath || '', 'es') || a.num - b.num);
export function enter() {
  active = true;
  document.body.classList.add('offline');
}
export function leave() {
  active = false;
  document.body.classList.remove('offline');
}
// login: quién inició sesión con conexión. Las descargas de otro usuario no se le dejan al siguiente.
export async function login(me) {
  if (state.user && state.user !== me.name) await wipe();
  state.user = me.name;
  state.seen = Date.now();
  store();
}
export async function wipe() {
  state = blank();
  held.clear();
  store();
  try { await caches.delete(AUDIO); } catch {}
}

// countFrames cuenta los cuadros de un MP3 ya limpio: con eso se sabe su duración exacta dentro del audio unido.
export function countFrames(b) {
  const kbps = [0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320];
  let n = 0;
  for (let i = 0; i + 4 <= b.length;) {
    const k = b[i] === 0xFF && (b[i + 1] & 0xFE) === 0xFA && ((b[i + 2] >> 2) & 3) === 1 ? kbps[b[i + 2] >> 4] : 0;
    if (!k) { i++; continue; }
    i += Math.floor(144000 * k / HZ) + ((b[i + 2] >> 1) & 1);
    n++;
  }
  return n;
}
const slim = (t) => ({ path: t.path, title: t.title, artist: t.artist, album: t.album, albumPath: t.albumPath, artistPath: t.artistPath, year: t.year, num: t.num, dur: t.dur });

// pending: lo que se pidió descargar y aún no baja. Queda anotado para seguir si la app se cierra o el sistema
// la detiene a mitad de camino.
export const pending = () => state.pending.filter((t) => !has(t.path));
// queue anota pistas para descargar sin bajarlas todavía (hay otra descarga en curso).
export function queue(tracks) {
  const known = new Set(state.pending.map((t) => t.path));
  state.pending = [...state.pending.filter((t) => !has(t.path)), ...tracks.filter((t) => !known.has(t.path) && !has(t.path)).map(slim)];
  store();
}
// download guarda en el celular las pistas que falten y avisa el avance (hechas, total). Devuelve cuántas no se
// pudieron bajar porque el servidor no las tiene o llegaron dañadas; esas no se reintentan.
export async function download(tracks, onProgress = () => {}) {
  const c = await caches.open(AUDIO);
  let done = 0;
  let bad = 0;
  // Fallos aún sin soltar: 5 seguidos no son canciones perdidas sino el servidor sin música (disco desmontado).
  let lost = [];
  const drop = () => {
    state.pending = state.pending.filter((x) => !lost.includes(x.path));
    store();
    bad += lost.length;
    lost = [];
  };
  queue(tracks);
  for (const t of tracks) {
    if (!state.tracks[t.path]) {
      const r = await fetch(audioUrl(t.path));
      // Sesión vencida o servidor caído detrás del túnel: pasa con todas, así que se reintenta después como un fallo de red.
      if (r.status === 401 || r.status >= 500) throw new Error(`No se pudo descargar «${t.title}».`);
      const buf = r.ok ? await r.arrayBuffer() : new ArrayBuffer(0);
      const frames = countFrames(new Uint8Array(buf));
      if (!frames) { // el servidor ya no la tiene o llegó dañada: se suelta para que no frene a las demás en cada reintento
        lost.push(t.path);
        if (lost.length >= 5) throw new Error('El servidor no está entregando la música.');
        onProgress(++done, tracks.length);
        continue;
      }
      drop();
      await c.put(audioUrl(t.path), new Response(buf, { headers: { 'Content-Type': 'audio/mpeg' } }));
      if (t.albumPath && !(await c.match(thumb(t.albumPath)))) { // la carátula, para verla sin conexión (sw.js la sirve)
        const img = await fetch(thumb(t.albumPath)).catch(() => null);
        if (img?.ok) await c.put(thumb(t.albumPath), img);
      }
      state.tracks[t.path] = { t: slim(t), frames, bytes: buf.byteLength, dur: frames * FRAME };
      state.pending = state.pending.filter((x) => x.path !== t.path);
      if (!store()) { // no cupo la anotación: sin ella el audio quedaría ocupando espacio sin poderse usar
        delete state.tracks[t.path];
        await c.delete(audioUrl(t.path));
        throw new DOMException('No hay espacio para anotar la descarga.', 'QuotaExceededError');
      }
    }
    onProgress(++done, tracks.length);
  }
  drop();
  try { navigator.storage?.persist?.(); } catch {} // que el sistema no lo borre por su cuenta, si lo permite
  return bad;
}
export async function remove(paths) {
  const c = await caches.open(AUDIO);
  for (const p of paths) {
    delete state.tracks[p];
    held.delete(p);
    await c.delete(audioUrl(p));
  }
  for (const id of Object.keys(state.playlists)) if (!state.playlists[id].paths.some(has)) delete state.playlists[id];
  store();
}
export const keeps = (id) => !!state.playlists[id]; // esta playlist se eligió para tenerla sin conexión
export function dropPlaylist(id) {
  delete state.playlists[id];
  store();
}
// keepPlaylist recuerda una playlist descargada: sin conexión se arma con las canciones que cada quien eligió bajar.
export function keepPlaylist(pl, tracks) {
  state.playlists[pl.id] = { id: pl.id, name: pl.name, updated: pl.updated || Date.now(), paths: tracks.map((t) => t.path) };
  store();
}
// remember guarda, con conexión, lo que la biblioteca dice de lo descargado (guardados, me gusta, más escuchadas).
export function remember(lib) {
  if (active || !count()) return;
  const dirs = new Set(Object.values(state.tracks).map((x) => x.t.albumPath));
  state.albums = Object.fromEntries(lib.albums.filter((a) => dirs.has(a.path)).map((a) => [a.path, a]));
  state.artists = Object.fromEntries(lib.artists.filter((a) => a.saved).map((a) => [a.path, a.saved])) // solo los guardados: el catálogo entero no cabe bien en localStorage;
  state.likes = lib.likes;
  state.top = lib.top.map((t) => t.path);
  state.recent = (lib.recent || []).map((t) => t.path);
  for (const p of lib.playlists) if (state.playlists[p.id]) Object.assign(state.playlists[p.id], { name: p.name, updated: p.updated });
  const alive = new Set(lib.playlists.map((p) => String(p.id)));
  for (const id of Object.keys(state.playlists)) if (!alive.has(String(id))) delete state.playlists[id];
  store();
}

// blobs entrega el audio guardado de cada ruta y lo que dura, para unirlo en un solo audio.
export async function blobs(paths) {
  const c = await caches.open(AUDIO);
  const want = new Set(paths);
  for (const p of held.keys()) if (!want.has(p)) held.delete(p); // lo que ya no entra en el audio unido no se queda en memoria
  return Promise.all(paths.map(async (p) => {
    if (!held.has(p)) {
      const r = await c.match(audioUrl(p));
      if (!r) { // el sistema lo borró para liberar espacio
        delete state.tracks[p];
        store();
        throw new Error('Una canción descargada ya no está en este celular. Vuelve a descargarla cuando tengas conexión.');
      }
      held.set(p, await r.blob());
    }
    return { blob: held.get(p), secs: state.tracks[p].dur };
  }));
}

// span dice qué tramo [from, to) de una cola entra en el audio unido si se arma para la pista i: una antes (para
// devolverse sin rearmar) y las que sigan hasta completar max segundos. durs: la duración de cada pista de la cola.
export function span(durs, i, max) {
  const from = Math.max(0, i - 1);
  let to = from;
  for (let secs = 0; to < durs.length && (to <= i || secs + (durs[to] || 240) <= max); to++) secs += durs[to] || 240;
  return [from, to];
}

// Búsqueda con la misma tolerancia del servidor (internal/music/search.go): ortografía distinta que suena igual
// y errores de tecleo en palabras largas. webtest/offline.test.mjs usa los mismos casos que search_test.go.
const SOUNDS = { ch: 'ch', ph: 'f', qu: 'k', ce: 'se', ci: 'si', ll: 'i', c: 'k', q: 'k', z: 's', v: 'b', y: 'i', h: '' };
function sound(s) {
  let out = '';
  let last = '';
  for (const ch of fold(s).replace(/ch|ph|qu|ce|ci|ll|[cqzvyh]/g, (m) => SOUNDS[m])) {
    const r = /[\p{L}\p{N}]/u.test(ch) ? ch : ' ';
    if (r === last && !/\p{N}/u.test(r)) continue; // las letras dobles no cuentan
    out += r;
    last = r;
  }
  return out;
}
// approx: la menor distancia de edición entre p y cualquier pedazo de t (algoritmo de Sellers).
function approx(p, t) {
  const row = new Array(t.length + 1).fill(0);
  for (let i = 1; i <= p.length; i++) {
    let diag = row[0];
    row[0] = i;
    for (let j = 1; j <= t.length; j++) {
      const cur = Math.min(row[j] + 1, row[j - 1] + 1, diag + (p[i - 1] === t[j - 1] ? 0 : 1));
      diag = row[j];
      row[j] = cur;
    }
  }
  return Math.min(...row);
}
// matcher devuelve, para un texto, cuánto hubo que adivinar para encontrar todas las palabras (-1: no coincide).
export function matcher(raw) {
  const terms = fold(raw).split(/\s+/).filter(Boolean).map((w) => ({ plain: w, key: sound(w).replaceAll(' ', '') }));
  return (s) => {
    const plain = fold(s);
    const key = sound(s);
    let total = 0;
    for (const t of terms) {
      if (plain.includes(t.plain)) continue;
      const d = approx(t.key, key);
      if (d > (t.key.length >= 8 ? 2 : t.key.length >= 5 ? 1 : 0)) return -1;
      total += d;
    }
    return total;
  };
}
// best: lo que coincide, primero lo más parecido (y entre iguales, lo de menor rank), hasta n.
function best(match, items, n, text, rank = () => 0) {
  const hits = items.map((it) => ({ it, d: match(text(it)), r: rank(it) })).filter((h) => h.d >= 0).sort((a, b) => a.d - b.d || a.r - b.r);
  return (n ? hits.slice(0, n) : hits).map((h) => h.it);
}

// Lo que respondería el servidor, armado solo con lo descargado.
const fold = (s) => String(s ?? '').normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
const all = () => Object.values(state.tracks).map((x) => x.t);
const byAlbum = (dir) => all().filter((t) => t.albumPath === dir).sort((a, b) => a.num - b.num || a.path.localeCompare(b.path));
function album(dir, ts = byAlbum(dir)) {
  const base = state.albums[dir] ?? { path: dir, name: ts[0].album, artist: ts[0].artistPath || ts[0].artist, artistPath: ts[0].artistPath, year: ts[0].year, added: 0, played: 0, plays: 0, saved: 0 };
  return { ...base, tracks: ts.length, dur: ts.reduce((n, t) => n + t.dur, 0) };
}
function albums() {
  const groups = new Map(); // una sola pasada: esto corre en cada petición sin conexión
  for (const t of all()) groups.get(t.albumPath)?.push(t) ?? groups.set(t.albumPath, [t]);
  return [...groups].map(([dir, ts]) => album(dir, ts)).sort((a, b) => fold(a.name).localeCompare(fold(b.name)));
}
function artists(list = albums()) {
  const out = new Map();
  for (const a of list) {
    if (!a.artistPath) continue;
    const x = out.get(a.artistPath) ?? { path: a.artistPath, name: a.artistPath, albums: 0, tracks: 0, added: 0, played: 0, plays: 0, saved: state.artists[a.artistPath] || 0 };
    x.albums++;
    x.tracks += a.tracks;
    x.plays += a.plays;
    x.added = Math.max(x.added, a.added);
    x.played = Math.max(x.played, a.played);
    out.set(a.artistPath, x);
  }
  return [...out.values()].sort((a, b) => fold(a.name).localeCompare(fold(b.name)));
}
const playlistTracks = (p) => p.paths.filter(has).map((path) => state.tracks[path].t);
const playlists = () => Object.values(state.playlists).map((p) => {
  const ts = playlistTracks(p);
  return { id: p.id, name: p.name, updated: p.updated, tracks: ts.length, cover: ts[0]?.albumPath ?? '' };
}).filter((p) => p.tracks > 0).sort((a, b) => b.updated - a.updated);
// more: canciones descargadas al azar que no estén en skip, para que la música siga al acabarse la cola.
export function more(skip, n = 20) {
  const seen = new Set(skip.map((t) => t.path));
  return all().filter((t) => !seen.has(t.path)).sort(() => Math.random() - 0.5).slice(0, n);
}

const reply = (v) => new Response(JSON.stringify(v), { headers: { 'Content-Type': 'application/json' } });
const gone = () => new Response('no encontrado', { status: 404 });
export const NEEDS_NET = 'Sin conexión: esto vuelve a estar disponible cuando te conectes.';
// handle responde una petición de la app sin ir a la red.
export function handle(path, opts = {}) {
  const u = new URL(path, location.origin);
  const q = u.searchParams;
  if ((opts.method ?? 'GET') !== 'GET') return new Response(NEEDS_NET, { status: 503 });
  const list = albums();
  switch (u.pathname) {
    case '/api/me': return reply(saved());
    case '/api/music': return reply({ albums: list, artists: artists(list), playlists: playlists(), likes: state.likes.filter(has), top: state.top.filter(has).map((p) => state.tracks[p].t),
      recent: state.recent.filter(has).map((p) => state.tracks[p].t), fresh: [] });
    case '/api/music/album': {
      const dir = q.get('path') ?? '';
      if (!byAlbum(dir).length) return gone();
      const a = album(dir);
      return reply({ saved: a.saved, path: dir, name: a.name, artist: a.artist, artistPath: a.artistPath, year: a.year, tracks: byAlbum(dir) });
    }
    case '/api/music/artist': {
      const dir = q.get('path') ?? '';
      const mine = list.filter((a) => a.artistPath === dir);
      if (!mine.length) return gone();
      return reply({ saved: state.artists[dir] || 0, path: dir, name: dir, albums: mine, tracks: all().filter((t) => t.artistPath === dir), popular: [] });
    }
    case '/api/music/search': {
      const raw = q.get('q') ?? '';
      if (raw.replace(/\s/g, '').length < 2) return reply({ tracks: [], albums: [], artists: [], playlists: [] });
      const match = matcher(raw);
      return reply({
        // Lo que coincide en el título sale antes que lo que coincide solo en el artista o el álbum.
        tracks: best(match, all().sort((a, b) => fold(a.title).localeCompare(fold(b.title))), 30, (t) => `${t.title} ${t.artist} ${t.album}`, (t) => (match(t.title) >= 0 ? 0 : 1)),
        albums: best(match, list, 12, (a) => `${a.name} ${a.artist}`),
        artists: best(match, artists(list), 8, (a) => a.name),
        playlists: best(match, playlists(), 0, (p) => p.name),
      });
    }
    case '/api/music/likes': return reply(state.likes.filter(has).map((p) => state.tracks[p].t));
    case '/api/music/radio': return reply(more([{ path: q.get('path') }], 40));
    case '/api/spotdl': return reply({ enabled: false, items: [] });
  }
  const pl = u.pathname.match(/^\/api\/music\/playlists\/(\d+)$/);
  if (pl) {
    const p = state.playlists[pl[1]];
    return p ? reply({ id: p.id, name: p.name, tracks: playlistTracks(p) }) : gone();
  }
  return new Response(NEEDS_NET, { status: 503 });
}
