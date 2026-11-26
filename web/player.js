// Reproductor de música: cola, aleatorio, repetir y la barra que sigue sonando al cambiar de sección.
import { tv } from './tv.js';
import { $, icon, setIcon, json, joinText, lazyImg, toast, clock, menu, coverColor } from './lib.js';
import { thumbUrl, go } from './nav.js';
import { api } from './session.js';
import { ensureLib, markLikes, toggleLike, liked, pickPlaylist } from './music.js';
import * as jam from './jam.js';
import * as offline from './offline.js';
// Los paneles (cola, dispositivos y jam) y los gestos viven aparte; leen el estado con state() y cambian la
// reproducción con las acciones que se exportan aquí.
import { paintQueue, openQueue } from './player-queue.js';
import { paintJam } from './player-jam.js';
import './player-gestures.js';
export { joinJam } from './player-jam.js';

export const audio = new Audio();
// Televisor: con música sonando la pantalla no se apaga. El protector de pantalla manda la app al fondo y la corta.
let lock;
if (tv && navigator.wakeLock) {
  const hold = async () => {
    if (audio.paused || document.visibilityState !== 'visible' || (lock && !lock.released)) return;
    lock = await navigator.wakeLock.request('screen').catch(() => null);
  };
  audio.addEventListener('playing', hold);
  document.addEventListener('visibilitychange', hold); // el sistema lo suelta al salir de la app
  audio.addEventListener('pause', () => lock?.release());
}
audio.preload = 'metadata';
audio.hidden = true;
// Dentro de la página y no suelto: así lo tenía la versión que sí mostraba la reproducción en la
// Dynamic Island y la pantalla bloqueada del iPhone.
document.body.append(audio);
// iOS solo deja sonar sin un toque a un audio que ya sonó con uno. Casi todo lo de la sala suena cuando responde el
// servidor, ya fuera del toque (por Cloudflare tarda más de lo que Safari espera): el primer toque en la app lo
// desbloquea con un play y pause seguidos, sin que alcance a sonar nada. Basta una vez por carga de la página.
function unlock() {
  document.removeEventListener('touchend', unlock, true);
  document.removeEventListener('click', unlock, true);
  if (!audio.paused) return;
  audio.play().catch(() => {});
  audio.pause();
}
document.addEventListener('touchend', unlock, true);
document.addEventListener('click', unlock, true);
// Sesión de audio de reproducción (Audio Session API, Safari 17+): sin esto iOS puede tratar la app como
// sonido secundario y silenciarla en segundo plano o con el interruptor de silencio.
try { if (navigator.audioSession) navigator.audioSession.type = 'playback'; } catch {}
let queue = []; // orden real de reproducción
let order = []; // orden original de la lista, para volver a él al apagar el aleatorio
let pos = -1;
let manual = 0; // pistas agregadas a mano justo después de la actual: el aleatorio no las mueve
let shuffle = false;
let repeat = 0; // 0 apagado, 1 toda la cola, 2 solo la pista actual
let user = ''; // dueño de la cola guardada en este navegador
let counted = false; // la pista actual ya se anotó en el historial
let savedAt = 0;
let resume = 0; // posición restaurada que el audio aún no ha cargado
let pausedAt = 0; // cuándo se pausó el audio: un flujo que lleva rato en pausa pudo cerrarse sin avisar

// Sala de reproducción (jam.js): con sala, la cola y la posición son las del servidor y este reproductor
// las refleja; cada acción se manda como comando en vez de cambiar el estado local. Hay sala cuando elijo
// en cuál de mis dispositivos suena, o en un jam con otros usuarios.
let room = null; // la foto de la sala; null = reproductor local
let devices = []; // mis dispositivos conectados
let here = true; // en este dispositivo es donde suena para mi cuenta
let offset = 0; // reloj del servidor menos el de este dispositivo
let curPath = ''; // pista cargada en el audio
let warned = false; // ya se avisó que el navegador no dejó sonar sin un toque
const roomTime = () => (room.playing ? room.at + (Date.now() + offset - room.stamp) / 1000 : room.at);
export const playing = () => (room && !here ? room.playing : !audio.paused);

// Dos motores, el mismo en todos los dispositivos:
// - Con conexión, flujo continuo. Android e iOS dejan que una página siga sonando en segundo plano mientras sea
//   el mismo audio, pero cortan al cargar uno nuevo, que es lo que pasa al terminar cada canción. Por eso todo
//   suena a través de una sala: el audio es una sola respuesta del servidor (/api/jam/stream) que trae la pista
//   de la sala y luego la que sigue, sin cambiar de fuente. La cola, la pista y la posición son las de la sala;
//   esta página pinta el avance con el reloj de la sala y manda cada acción como comando, igual que en un jam.
// - Sin conexión, el audio unido (deck): las canciones descargadas, una tras otra, en un solo audio del celular.
// local: el reproductor suena desde lo descargado. Sigue así un rato después de volver la conexión (goOnline)
// para no cortar la canción; por eso es distinto de offline.active, que es el modo de la app.
let local = false;
export function goOffline() {
  if (local) return void keepDownloaded(deckTime(), !audio.paused); // ya sonaba desde lo descargado
  local = true;
  // Si la red se cae con la app abierta, lo que sonaba sigue donde iba, ya desde lo descargado.
  const at = room ? roomTime() : resume;
  const play = here && (room ? room.playing : !audio.paused);
  jam.close();
  stream = null;
  room = null;
  devices = [];
  here = true;
  document.body.classList.remove('in-room', 'own-stream');
  $('#jam').close();
  keepDownloaded(at, play);
}
// goOnline: volvió la conexión. La app ya es la de siempre; lo que suena sigue sin corte desde la copia del
// celular y el reproductor pasa al modo normal cuando deje de sonar o se pida algo que necesita al servidor.
export function goOnline() {
  if (!local || offline.active) return;
  if (!audio.paused) return void audio.addEventListener('pause', goOnline, { once: true });
  toNormal();
}
// toNormal suelta el audio unido y deja el reproductor con conexión en la misma pista y segundo, sonando si
// sonaba. edit: cambio de cola que provocó el paso (una canción que no está descargada). keep = false: lo que
// sonaba no se retoma, viene música nueva.
function toNormal(edit, keep = true) {
  if (!local || offline.active) return;
  const at = deck ? deckTime() : resume;
  const on = !!deck && !audio.paused;
  local = false;
  audio.pause();
  audio.removeAttribute('src');
  if (deck) URL.revokeObjectURL(deck.url);
  deck = null;
  curPath = '';
  jam.connect(applyJam);
  edit?.();
  if (!keep || !queue[pos]) return;
  load(pos, { play: on, at });
  if (on) startRoom({ playing: true });
}
// purge: se quitaron descargas estando sin conexión; lo que ya no está en el celular sale de la cola.
export function purge() {
  if (local && queue.some((t) => !offline.has(t.path))) keepDownloaded(deckTime(), !audio.paused);
}
// keepDownloaded deja en la cola solo lo descargado y sigue en la pista que sonaba, si quedó.
function keepDownloaded(at, play) {
  const now = queue[pos];
  if (!now) return;
  queue = queue.filter((t) => offline.has(t.path));
  order = order.filter((t) => offline.has(t.path));
  const i = queue.indexOf(now);
  if (queue.length) return load(Math.max(0, i), { play: play && i >= 0, at: i < 0 ? 0 : at });
  audio.pause(); // nada de la cola está descargado: el reproductor se guarda
  audio.removeAttribute('src');
  if (deck) URL.revokeObjectURL(deck.url);
  deck = null;
  pos = -1;
  $('#player').hidden = true;
  document.body.classList.remove('has-player');
}
let stream = null; // { pos, origin, end, held }: qué traía la sala cuando se abrió (o se dio por bueno) el flujo que suena
const streaming = () => !local && !!room && here;
const dur = () => (deck ? deck.lens[pos] : 0) || queue[pos]?.dur || 0; // el flujo no tiene duración: vale la de la pista

// Sin conexión la cola suena como un solo audio: las canciones descargadas, ya limpias, unidas una tras otra. El
// teléfono no carga un audio nuevo al pasar de canción (lo que iOS no siempre deja con la pantalla bloqueada), y
// saltar es moverse dentro del mismo audio. Se une un tramo de la cola y no toda: con 167 canciones (545 MB en
// un solo audio) el iPhone no lo abría. deck guarda qué tramo es, y dónde empieza y cuánto dura cada pista en él.
const DECK_SECS = 9000; // 2 h 30 min por tramo: unos 145 MB a 128 kbps
let deck = null; // { key, url, from, to, starts, lens }: cubre queue[from..to); starts y lens van por índice de la cola
let deckBusy = false;
const deckSpan = (i) => offline.span(queue.map((t) => t.dur), i, DECK_SECS);
const spanKey = (from, to) => queue.slice(from, to).map((t) => t.path).join('\n');
// deckHas: el audio unido que hay sirve para la pista i (la cubre, y ese tramo de la cola no cambió).
const deckHas = (i) => !!deck && i >= deck.from && i < deck.to && spanKey(deck.from, deck.to) === deck.key;
const deckTime = () => Math.max(0, audio.currentTime - (deck?.starts[pos] ?? 0)); // segundo dentro de la pista actual
// fresh: rearmar el tramo desde esta pista aunque el que hay todavía sirva.
async function deckOpen(at, play, fresh = false) {
  if (!fresh && deckHas(pos)) audio.currentTime = deck.starts[pos] + at;
  else {
    const [from, to] = deckSpan(pos);
    const key = spanKey(from, to);
    deckBusy = true;
    let parts;
    try { parts = await offline.blobs(queue.slice(from, to).map((t) => t.path)); } catch (e) { deckBusy = false; return void toast(e.message, { kind: 'warn', sticky: true }); }
    deckBusy = false;
    if (deckSpan(pos)[0] !== from || spanKey(from, to) !== key) return; // la cola o la pista cambiaron mientras se leía: ya viene otra apertura
    if (deck) URL.revokeObjectURL(deck.url);
    const starts = [], lens = [];
    let total = 0;
    parts.forEach((p, k) => { starts[from + k] = total; lens[from + k] = p.secs; total += p.secs; });
    deck = { key, from, to, url: URL.createObjectURL(new Blob(parts.map((p) => p.blob), { type: 'audio/mpeg' })), starts, lens };
    audio.src = deck.url;
    const target = deck.starts[pos] + at;
    // moving: el audio nuevo arranca en cero y solo después salta a su sitio; mientras tanto deckTick no debe
    // creer que la música se devolvió a la primera pista del tramo.
    const d = deck;
    d.moving = target > 0;
    if (target > 0) audio.addEventListener('loadedmetadata', () => { audio.currentTime = target; d.moving = false; }, { once: true });
  }
  if (play) audio.play().catch(warnPlay);
}
// deckTick sigue al audio unido: cuando pasa solo a la pista siguiente, aquí solo hay que pintarla.
function deckTick() {
  if (deck.moving) return;
  const now = audio.currentTime;
  let k = pos;
  while (k + 1 < deck.to && now >= deck.starts[k + 1] - 0.02) k++;
  while (k > deck.from && now < deck.starts[k] - 0.02) k--;
  if (k !== pos) {
    if (repeat === 2 && k === pos + 1 && !audio.seeking) return void (audio.currentTime = deck.starts[pos]); // repetir esta canción
    if (k === pos + 1) count();
    pos = k;
    counted = false;
    manual = Math.max(0, manual - 1);
    curPath = queue[k].path;
    present(queue[k], 0);
    positionState(); // el audio unido no avisa el cambio de pista: sin esto la pantalla de bloqueo sigue con la anterior
    // Con la app a la vista y el tramo ya por la mitad, se arma el siguiente desde esta pista: con la pantalla
    // bloqueada el iPhone no siempre deja cargarlo, y así queda música para rato antes de bloquearla.
    const left = deck.starts[deck.to - 1] + deck.lens[deck.to - 1] - now;
    if (left < DECK_SECS / 2 && deckSpan(k)[1] > deck.to && document.visibilityState === 'visible') return void deckOpen(0, !audio.paused, true);
  }
  const at = deckTime();
  if (!audio.paused && at >= 30) count();
  if (Date.now() - savedAt > 5000) { saveAt(); positionState(); } // y se corrige la deriva cada 5 s
  if (seeking) return;
  for (const s of seeks) setSeek(s, deck.lens[pos] ? at / deck.lens[pos] : 0);
  paintTimes(at, deck.lens[pos]);
}
const warnPlay = (e) => console.warn('El navegador no dejó reproducir:', e?.name, e?.message);
// Posición en la pantalla de bloqueo y la notificación; sin esto Android puede dar la sesión por inactiva.
function positionState() {
  const total = dur();
  if (!navigator.mediaSession?.setPositionState || !(total > 0 && total < Infinity)) return;
  const at = deck ? deckTime() : room ? roomTime() : resume;
  try { navigator.mediaSession.setPositionState({ duration: total, position: Math.max(0, Math.min(at, total)), playbackRate: audio.playbackRate || 1 }); } catch {}
}
for (const ev of ['loadedmetadata', 'playing', 'pause', 'seeked', 'ratechange']) audio.addEventListener(ev, positionState);
const mix = (a) => {
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [a[i], a[j]] = [a[j], a[i]];
  }
  return a;
};
export const cover = (t) => [icon('music'), lazyImg(thumbUrl('musica', t.albumPath))];

function load(i, { play = true, at = 0 } = {}) {
  if (i < 0 || i >= queue.length) return;
  pos = i;
  counted = false;
  resume = at;
  const t = queue[i];
  if (local) { // sin conexión: el audio unido de la cola, movido hasta esta pista
    curPath = t.path;
    deckOpen(at, play);
  } else curPath = t.path; // aquí solo se pinta: el audio es el flujo de la sala (syncStream)
  present(t, at);
}
// present pinta la pista en la barra, la pantalla completa y la pantalla de bloqueo.
function present(t, at) {
  const sub = joinText(t.artist, t.album);
  for (const [titleEl, subEl, coverEl] of [['#now', '#now-sub', '#player-cover'], ['#np-title', '#np-sub', '#np-cover']]) {
    $(titleEl).textContent = t.title;
    $(subEl).textContent = sub;
    $(coverEl).replaceChildren(...cover(t));
  }
  // El color de la carátula tiñe el reproductor y el fondo de la app (--amb, style.css); sin carátula queda el de siempre.
  const art = $('#player-cover img'); // la de la barra: la de pantalla completa no carga mientras está cerrada
  // El color anterior se queda hasta que cargue la nueva: quitarlo antes hace parpadear el fondo entre canciones.
  const amb = document.documentElement.style;
  if (!art) amb.removeProperty('--amb');
  art?.addEventListener('load', () => { if (art.isConnected) amb.setProperty('--amb', coverColor(art) ?? ''); });
  // Si falla, lazyImg ya la quitó; el color se quita solo si en la barra no quedó otra carátula (la de la pista siguiente).
  art?.addEventListener('error', () => { if (!$('#player-cover img')) amb.removeProperty('--amb'); });
  for (const b of document.querySelectorAll('#player .like, #np .like')) b.dataset.like = t.path;
  for (const s of seeks) setSeek(s, t.dur ? Math.min(1, at / t.dur) : 0); // una canción retomada a medias no arranca con la barra en cero
  paintTimes(at, t.dur);
  $('#player').hidden = false;
  document.body.classList.add('has-player');
  if ('mediaSession' in navigator) {
    // Título, artista y carátula en la pantalla de bloqueo.
    navigator.mediaSession.metadata = new MediaMetadata({
      title: t.title, artist: t.artist, album: t.album, artwork: [{ src: thumbUrl('musica', t.albumPath), sizes: '400x400', type: 'image/jpeg' }],
    });
  }
  mediaActions();
  markLikes();
  syncPlayButton();
  paintQueue();
  save();
}

// Las sugerencias cuando la cola se acaba las pone el servidor en la sala (extend, jam.go) y llegan marcadas (auto)
// con la foto; sin conexión se agregan al armar la cola (playAt).

// playAt reemplaza la cola por tracks y empieza en la pista i.
export function playAt(tracks, i) {
  if (i < 0 || i >= tracks.length) return;
  if (tv && !$('#np').open) $('#np').showModal(); // en el televisor elegir una canción lleva a «Reproduciendo»; atrás vuelve a la lista
  toNormal(null, false); // volvió la conexión y se elige música nueva: ya suena por el camino normal
  if (local && !repeat && tracks.length - i < 6) tracks = [...tracks, ...offline.more(tracks)]; // que la música siga
  if (room) {
    const from = Math.max(0, i - 50); // la sala guarda hasta 500 pistas alrededor de la elegida
    let list = tracks.slice(from, from + 500);
    let at = i - from;
    if (shuffle && !room.open) { // en la sala propia del flujo el aleatorio se aplica al armar la cola
      list = [list[at], ...mix(list.filter((_, k) => k !== at))];
      at = 0;
    }
    return void jam.send('queue', { paths: list.map((t) => t.path), pos: at });
  }
  order = tracks.map((t) => ({ ...t })); // copias: la misma pista puede estar dos veces en la cola
  manual = 0;
  if (shuffle) {
    const first = order[i];
    queue = [first, ...mix(order.filter((t) => t !== first))];
    load(0);
  } else {
    queue = order.slice();
    load(i);
  }
  if (!local) startRoom({ playing: true, at: 0 }); // con conexión todo suena a través de una sala
}

function addAt(t, nextUp) {
  const done = () => toast(`«${t.title}» ${nextUp ? 'suena a continuación' : 'se agregó a la cola'}.`, { kind: 'ok' });
  if (local && !offline.active && !offline.has(t.path)) return toNormal(() => addAt(t, nextUp)); // no cabe en el audio unido
  if (room) return void jam.send('add', { paths: [t.path], next: nextUp }).then((ok) => ok && done());
  if (pos < 0) return playAt([t], 0);
  queue.splice(nextUp ? pos + 1 : pos + 1 + manual, 0, { ...t });
  manual++;
  done();
  paintQueue();
  save();
}
export const playNext = (t) => addAt(t, true);
export const enqueue = (t) => addAt(t, false);

// Pasar a otra canción antes de que cuente como escucha (30 s) queda registrado, con si era una sugerencia:
// es la medida de qué tan bien aciertan las sugerencias.
function skipped() {
  const t = queue[pos];
  if (t && !counted && !local) api('/api/music/skips', json('POST', { path: t.path, auto: !!t.auto })).catch(() => {});
}
export function next() {
  skipped();
  if (streaming()) return streamDo('next');
  if (room) return void jam.send('next', { pos }); // suena en otro dispositivo: este es el control remoto
  let n = pos + 1;
  if (n >= queue.length) {
    if (!repeat) return; // se acabó la cola
    n = 0;
  }
  manual = Math.max(0, manual - 1);
  load(n);
  if (!local) startRoom({ playing: true, at: 0 });
}
function prev() {
  if (streaming()) return streamDo(roomTime() > 3 || pos === 0 ? 'seek' : 'prev', { at: 0 });
  if (room) return void (roomTime() > 3 || pos === 0 ? jam.send('seek', { at: 0 }) : jam.send('prev'));
  if (!local) { // cola restaurada, todavía sin sala: la anterior (o la misma desde el principio) arranca en una
    if (pos > 0) load(pos - 1);
    return void startRoom({ playing: true, at: 0 });
  }
  if ((deck ? deckTime() : audio.currentTime) > 3 || pos === 0) return void seekTo(0);
  manual = 0;
  load(pos - 1);
}

export const isShuffle = () => shuffle;
function setShuffle(on) {
  shuffle = on;
  if (pos >= 0) {
    const kept = pos + 1 + manual; // lo ya escuchado, la actual y lo agregado a mano no se tocan
    if (on) queue = [...queue.slice(0, kept), ...mix(queue.slice(kept))];
    else {
      const i = order.indexOf(queue[pos]);
      if (i >= 0) { // sigue desde la pista actual en el orden original
        queue = [...order.slice(0, i + 1), ...queue.slice(pos + 1, kept), ...order.slice(i + 1)];
        pos = i;
      }
    }
  }
  syncToggles();
  paintQueue();
  save();
}

// En la sala propia del flujo el aleatorio baraja lo que sigue en la cola del servidor. Apagarlo no devuelve
// el orden original (la sala no lo guarda): solo deja de barajar lo próximo que pongas.
function streamShuffle() {
  shuffle = !shuffle;
  if (shuffle) jam.send('tail', { paths: mix(queue.slice(pos + 1)).map((t) => t.path) });
  syncToggles();
  save();
}

export const REPEAT = ['Repetir', 'Repetir: toda la cola', 'Repetir: esta canción'];
export function syncToggles() {
  for (const b of document.querySelectorAll('[data-act=shuffle]')) b.setAttribute('aria-pressed', shuffle);
  for (const b of document.querySelectorAll('[data-act=repeat]')) {
    b.setAttribute('aria-pressed', repeat > 0);
    b.setAttribute('aria-label', REPEAT[repeat]);
    if (!b.classList.contains('queue-btn')) setIcon(b, repeat === 2 ? 'repeat-1' : 'repeat');
  }
  paintQueue(); // sus botones de abajo y el de pausa de la pista que suena
}
// Los botones de aleatorio, repetir y cola existen en la barra, en pantalla completa y en las vistas.
document.addEventListener('click', (e) => {
  const act = e.target.closest?.('[data-act]')?.dataset.act;
  if (act === 'jam' && offline.active) toast(offline.NEEDS_NET);
  else if (act === 'jam') { toNormal(); $('#jam').showModal(); paintJam(); }
  else if (act === 'shuffle' && room && !room.open && here) streamShuffle();
  else if (room && (act === 'shuffle' || (act === 'repeat' && room.open))) toast('Aleatorio y repetir no están disponibles mientras la reproducción es compartida.');
  else if (act === 'shuffle') setShuffle(!shuffle);
  else if (act === 'repeat') {
    repeat = (repeat + 1) % 3;
    syncToggles();
    save();
    if (room) { // sala propia del flujo: el servidor decide qué suena al terminar
      room.repeat = repeat; // syncRoom toma el de la sala: que no lo devuelva mientras llega la respuesta
      jam.send('repeat', { repeat });
    }
  }
  else if (act === 'queue') openQueue();
});
// El me gusta de los reproductores: un toque la guarda; si ya está guardada, ofrece quitarla o sumarla a una playlist.
for (const b of document.querySelectorAll('#player .like, #np .like')) {
  b.onclick = () => {
    const t = queue[pos];
    if (!t || !liked.has(t.path)) return void (b.dataset.like && toggleLike(b.dataset.like));
    menu(b, `Guardar ${t.title}`, [
      { icon: 'list-plus', label: 'Agregar a playlist…', run: () => pickPlaylist(b, t) },
      { icon: 'heart', label: 'Quitar de Canciones que te gustan', run: () => toggleLike(t.path) },
    ]);
  };
}
// Los tres puntos de la pantalla completa: lo que se puede hacer con la canción que suena.
$('#np-more').onclick = () => {
  const t = queue[pos];
  if (!t) return;
  const leave = (path) => { $('#np').close(); go('musica', path); };
  menu($('#np-more'), `Acciones de ${t.title}`, [
    !offline.active && { icon: 'heart', label: liked.has(t.path) ? 'Quitar de Canciones que te gustan' : 'Agregar a Canciones que te gustan', run: () => toggleLike(t.path) },
    !offline.active && { icon: 'list-plus', label: 'Agregar a playlist…', run: () => pickPlaylist($('#np-more'), t) },
    { icon: 'queue', label: 'Abrir la cola', run: openQueue },
    !offline.active && { icon: 'cast', label: 'Dispositivos y jam', run: () => { toNormal(); $('#jam').showModal(); paintJam(); } },
    { icon: 'disc', label: 'Ir al álbum', run: () => leave(`album/${t.albumPath}`) },
    t.artistPath && { icon: 'user', label: 'Ir al artista', run: () => leave(`artista/${t.artistPath}`) },
  ]);
};
// Atajo con el dedo: tocar el título lleva al álbum y tocar el artista, al artista (con teclado están en el menú de arriba).
for (const [sel, path] of [['#np-title', (t) => `album/${t.albumPath}`], ['#np-sub', (t) => t.artistPath && `artista/${t.artistPath}`]]) {
  $(sel).onclick = () => { const t = queue[pos]; const p = t && path(t); if (p) { $('#np').close(); go('musica', p); } };
}

// markPlaying resalta en la vista la pista que suena y pone al día los botones compartidos.
export function markPlaying() {
  const current = queue[pos]?.path;
  for (const r of document.querySelectorAll('[data-track]')) {
    const on = r.dataset.track === current;
    if (!on && !r.classList.contains('playing')) continue; // ni estaba ni queda marcada: una lista larga no se reescribe entera
    r.classList.toggle('playing', on);
    if (on) r.setAttribute('aria-current', 'true'); else r.removeAttribute('aria-current');
    r.querySelector('.num')?.replaceChildren(on ? icon(playing() ? 'play' : 'pause') : r.dataset.n); // el número lo dejó trackList (music.js)
  }
  syncToggles();
}

// En pausa el reproductor solo se ve en Música (style.css, body.paused). La espera evita que se esconda
// y reaparezca en el cambio de canción, cuando el audio pasa un instante por pausa.
let pausedTimer;
function syncPlayButton() {
  clearTimeout(pausedTimer);
  const on = playing();
  if (on) document.body.classList.remove('paused');
  else pausedTimer = setTimeout(() => !playing() && document.body.classList.add('paused'), 600);
  for (const b of [$('#playpause'), $('#np-play')]) {
    setIcon(b, on ? 'pause' : 'play');
    b.setAttribute('aria-label', on ? 'Pausar' : 'Reproducir');
  }
  if ('mediaSession' in navigator) navigator.mediaSession.playbackState = on ? 'playing' : 'paused';
  markPlaying();
}
audio.onplay = syncPlayButton;
audio.onpause = () => { pausedAt = Date.now(); syncPlayButton(); save(); };
audio.onended = () => {
  if (deck) { // se acabó el audio unido: el tramo, o toda la cola
    count();
    if (repeat === 2) return void load(pos); // repetir esta canción: era la última del tramo
    if (deck.to < queue.length) { // sigue el tramo siguiente
      manual = Math.max(0, manual - 1);
      return void load(deck.to);
    }
    return void (repeat === 1 && queue.length && load(0));
  }
  streamLost(); // el flujo no termina con cada pista: si se acabó es que se cortó
};
audio.onerror = () => {
  if (deck) return void toast('No se pudo reproducir la música descargada. Vuelve a descargarla cuando tengas conexión.', { kind: 'warn', sticky: true });
  if (audio.getAttribute('src')) streamLost(); // quitar el audio a propósito también dispara este evento
};

// Historial: una escucha cuenta a los 30 s o al terminar la pista.
function count() {
  if (counted || !queue[pos]) return;
  counted = true;
  api('/api/music/plays', json('POST', { path: queue[pos].path })).catch(() => {});
}

// Dos barras de avance (mini y pantalla completa) sobre el mismo audio.
const seeks = [$('#seek'), $('#np-seek')];
let seeking = false;
function setSeek(s, p) {
  s.value = Math.round(p * 1000);
  s.style.setProperty('--p', `${p * 100}%`);
}
function paintTimes(cur, dur) {
  $('#np-cur').textContent = $('#cur').textContent = clock(cur);
  $('#np-dur').textContent = $('#dur').textContent = clock(dur);
}
audio.ontimeupdate = () => {
  if (deck) return deckTick();
  paintRemote(); // el avance del flujo se pinta con el reloj de la sala; este evento sí llega en segundo plano
};
for (const s of seeks) {
  s.oninput = () => {
    seeking = true;
    s.style.setProperty('--p', `${s.value / 10}%`);
    if (dur()) $('#np-cur').textContent = $('#cur').textContent = clock((s.value / 1000) * dur());
  };
  s.onchange = () => {
    if (dur()) seekTo((s.value / 1000) * dur());
    seeking = false;
  };
}
function setVolume(v) {
  audio.volume = Math.min(1, Math.max(0, v));
  $('#volume').value = Math.round(audio.volume * 100);
  $('#volume').style.setProperty('--p', `${audio.volume * 100}%`);
}
$('#volume').oninput = (e) => { setVolume(e.target.value / 100); save(); };

function seekTo(at) {
  if (deck) return void (audio.currentTime = deck.starts[pos] + Math.max(0, at));
  if (streaming()) return streamDo('seek', { at });
  if (room) return void jam.send('seek', { at });
  resume = at; // cola restaurada, aún sin sala: arrancará desde aquí
  paintTimes(at, dur());
}
export function toggle() {
  if (local) return audio.paused ? audio.play().catch(warnPlay) : audio.pause();
  if (!room) return void (pos >= 0 && startRoom({ playing: true })); // cola restaurada: arranca en una sala
  if (!here && !devices.some((d) => d.active)) { // el dispositivo donde sonaba se desconectó: reproducir aquí lo toma
    here = true;
    jam.send('here', { target: jam.deviceId });
  }
  if (streaming()) {
    // Pausar y reanudar no sueltan el flujo: el teléfono guarda su colchón y sigue con el mismo audio, dentro del
    // propio toque (o del botón de la pantalla bloqueada), que es lo único que iOS deja hacer sin la app abierta.
    if (!audio.paused) {
      audio.pause();
      return void jam.send('pause');
    }
    // El flujo en pausa pudo cerrarse (Cloudflare corta una conexión que pasa un rato sin datos): se abre otro
    // aquí mismo, dentro del toque. Si se abriera al llegar la respuesta del servidor, Safari no lo dejaría sonar.
    // Un cierre limpio no deja rastro en el audio (sonaría el colchón y se callaría): tras 60 s en pausa se da por cerrado.
    if (!stream || !audio.src || audio.ended || audio.error || Date.now() - pausedAt > 60000) return streamDo('seek', { at: roomTime() });
    audio.play().catch(warnPlay);
    return void jam.send('play');
  }
  jam.send(room.playing ? 'pause' : 'play'); // suena en otro dispositivo: este es el control remoto
}
$('#playpause').onclick = $('#np-play').onclick = toggle;
$('#next').onclick = $('#np-next').onclick = () => next();
$('#prev').onclick = $('#np-prev').onclick = prev;
$('#player-open').onclick = () => $('#np').showModal();
$('#np-close').onclick = () => $('#np').close();
// Controles de la pantalla de bloqueo y la notificación. iOS decide qué botones muestra cuando empieza a sonar
// cada pista: con «adelantar/atrasar» registrados, o sin «siguiente/anterior» en ese momento, pone los de ±10 s.
// Por eso se registran de nuevo con cada pista y los de salto quedan explícitamente apagados.
function mediaActions() {
  if (!('mediaSession' in navigator)) return;
  const set = (name, run) => { try { navigator.mediaSession.setActionHandler(name, run); } catch {} }; // acción que este navegador no conoce
  set('play', () => !playing() && toggle());
  set('pause', () => playing() && toggle());
  set('seekbackward', null);
  set('seekforward', null);
  set('nexttrack', () => next());
  set('previoustrack', prev);
  set('seekto', (d) => seekTo(d.seekTime));
}
mediaActions();
audio.addEventListener('playing', mediaActions);

// state: lo que los paneles y los gestos leen del reproductor (los let de aquí no se pueden leer desde otro módulo).
export const state = () => ({ queue, pos, manual, shuffle, repeat, room, devices, here });
export const canGo = (dir) => (dir > 0 ? pos + 1 < queue.length || repeat > 0 : pos > 0);
// swipePrev: la anterior al deslizar; a diferencia del botón, deslizar siempre cambia de canción.
export function swipePrev() {
  if (streaming()) streamDo('prev');
  else if (room) jam.send('prev');
  else {
    manual = 0;
    load(pos - 1);
    if (!local) startRoom({ playing: true, at: 0 });
  }
}
// jumpTo salta a la pista i de la cola (un toque en su fila).
export function jumpTo(i) {
  if (!room) {
    manual = Math.max(0, manual - (i - pos));
    load(i);
    return void (!local && startRoom({ playing: true, at: 0 }));
  }
  // La ruta va con el índice: si el servidor recortó la cola desde la última foto, la ubica en su puesto nuevo.
  if (streaming()) return streamDo('jump', { index: i, path: queue[i]?.path });
  jam.send('jump', { index: i, path: queue[i]?.path });
}

// Atajos de escritorio: espacio pausa, flechas adelantan 5 s, Mayús + flechas cambian de pista.
document.addEventListener('keydown', (e) => {
  // En el televisor las flechas mueven el foco (tv.js): solo adelantan sobre la barra de posición, donde el paso
  // propio del deslizador (una milésima de la canción) no sirve con un control remoto.
  const onSeek = tv && e.target.matches?.('#seek, #np-seek') && (e.key === 'ArrowRight' || e.key === 'ArrowLeft');
  if (pos < 0 || e.ctrlKey || e.metaKey || e.altKey || (tv ? !onSeek : e.target.closest?.('input, textarea, select'))) return;
  if (onSeek) e.preventDefault();
  if (document.querySelector('dialog[open]:not(#np)')) return; // el visor de fotos y los menús usan las mismas teclas
  if (e.key === ' ' && !e.target.closest?.('button, a')) { e.preventDefault(); toggle(); }
  else if (e.key === 'ArrowRight') e.shiftKey ? next() : seekTo((room ? roomTime() : deck ? deckTime() : resume) + 5);
  else if (e.key === 'ArrowLeft') e.shiftKey ? prev() : seekTo((room ? roomTime() : deck ? deckTime() : resume) - 5);
});

// setTail deja lo que sigue después de la pista actual como list; m: cuántas del principio son «En la cola».
export function setTail(list, m) {
  m = Math.max(0, Math.min(m, list.length));
  if (room) return void jam.send('tail', { paths: list.map((t) => t.path), manual: m });
  queue = [...queue.slice(0, pos + 1), ...list];
  order = queue.slice();
  manual = m;
  paintQueue();
  save();
}

// Cola, pista y posición se recuerdan en este navegador para retomar al volver a abrir la app.

const KEEP = 500; // pistas guardadas alrededor de la actual: localStorage tiene cupo
// saveAt guarda solo el segundo de la pista actual, en su propia clave: es lo único que cambia mientras suena, y
// reescribir la cola (hasta 500 pistas) cada 5 s es mucho para un celular. Falso si no hay nada que guardar.
function saveAt() {
  savedAt = Date.now();
  if (pos < 0 || !user) return false;
  // Sin conexión, un cambio en la cola (agregar, quitar, barajar) obliga a rearmar el audio unido donde iba.
  if (deck && !deckBusy && !deckHas(pos)) deckOpen(deckTime(), !audio.paused);
  try { localStorage.setItem('palobox.player.at', JSON.stringify({ path: queue[pos].path, at: deck ? deckTime() : resume })); } catch {}
  return true;
}
function save() {
  // Con conexión siempre hay sala: se guarda su cola para retomarla al volver a abrir la app.
  if (!saveAt()) return;
  const from = Math.max(0, pos - 50);
  try {
    localStorage.setItem('palobox.player', JSON.stringify({
      user, queue: queue.slice(from, from + KEEP), pos: pos - from, at: deck ? deckTime() : resume, shuffle, repeat, volume: audio.volume,
    }));
  } catch {} // modo privado o cupo lleno: la app sigue sin recordar
}
window.addEventListener('pagehide', save);
// Al volver a la app la sala se pone al día: el flujo de eventos pudo morir en segundo plano, y si el flujo de
// audio se cortó o el navegador no lo dejó arrancar sin un toque, syncRoom lo vuelve a abrir.
function wake() {
  if (document.visibilityState !== 'visible' || !user) return;
  if (local) return; // sin servidor no hay sala que poner al día
  if (!jam.alive()) jam.connect(applyJam); // la foto que llega llama a syncRoom
  else if (room) syncRoom();
}
document.addEventListener('visibilitychange', wake);
window.addEventListener('pageshow', wake);

// restore deja lista (en pausa) la cola que este usuario tenía la última vez.
export function restore(name) {
  if (user === name) return;
  user = name;
  if (!local) jam.connect(applyJam);
  let s;
  try { s = JSON.parse(localStorage.getItem('palobox.player')); } catch {}
  if (!s || s.user !== name || !Array.isArray(s.queue) || !s.queue[s.pos]?.path) return;
  // El segundo se guarda aparte (saveAt) y es más reciente; si no está o es de otra pista, vale el de la cola.
  try { const p = JSON.parse(localStorage.getItem('palobox.player.at')); if (p?.path === s.queue[s.pos].path && p.at >= 0) s.at = p.at; } catch {}
  setVolume(s.volume ?? 1);
  document.body.classList.add('paused'); // la cola restaurada empieza en pausa: que no asome fuera de Música
  queue = s.queue;
  let at = s.pos;
  if (local) { // sin conexión solo queda en la cola lo que está descargado
    const cur = queue[at];
    queue = queue.filter((t) => offline.has(t.path));
    at = Math.max(0, queue.indexOf(cur));
    if (!queue.length) return;
    if (queue[at] !== cur) s.at = 0;
  }
  order = queue.slice();
  shuffle = !!s.shuffle;
  repeat = [0, 1, 2].includes(s.repeat) ? s.repeat : 0;
  load(at, { play: false, at: s.at });
  if (tv && !$('#np').open) $('#np').showModal(); // en el televisor «Reproduciendo» es la pantalla principal
  ensureLib().catch(() => {}); // para pintar el me gusta de la pista restaurada
}

// reset vacía el reproductor al salir: la cola de un usuario no le queda al siguiente.
export function reset() {
  audio.pause();
  audio.removeAttribute('src');
  jam.close();
  if (deck) URL.revokeObjectURL(deck.url);
  deck = null;
  stream = null;
  room = null;
  devices = [];
  here = true;
  curPath = '';
  document.body.classList.remove('in-room');
  queue = order = [];
  pos = -1;
  user = '';
  for (const d of [$('#np'), $('#queue'), $('#jam')]) d.close();
  $('#player').hidden = true;
  document.body.classList.remove('has-player'); // player-tucked se queda: el siguiente usuario también lo ve minimizado
}

// Salas: elegir en qué dispositivo suena y jam con otros usuarios

// applyJam recibe cada foto del servidor: mis dispositivos y, si hay sala, su estado.
function applyJam(s) {
  offset = s.now - Date.now();
  devices = s.devices.map((d) => ({ ...d, me: !d.user && d.id === s.me })); // en un jam vienen también los de los demás (user)
  const was = room;
  room = s.room;
  skipMark = s.skip || null;
  here = !room || devices.some((d) => d.me && d.active);
  document.body.classList.toggle('in-room', !!room);
  document.body.classList.toggle('own-stream', !!room && !room.open); // sala propia: no es «compartida»
  if (room) return syncRoom();
  if (was) { // la sala terminó (se salió del jam): lo que sonaba sigue, en una sala propia con la cola que quedó
    order = queue.slice();
    save();
    stream = null;
    const on = was.playing && !audio.paused;
    audio.pause();
    if (on) startRoom({ playing: true });
  }
  syncPlayButton();
  paintJam();
}

// startRoom pasa la cola de este dispositivo a una sala nueva.
export function startRoom(extra = {}) {
  const from = Math.max(0, pos - 50);
  return jam.send('start', {
    paths: queue.slice(from, from + 500).map((t) => t.path), pos: Math.max(0, pos - from),
    at: resume, playing: false, repeat, manual, ...extra,
  });
}

// syncRoom deja este reproductor igual a la sala: misma cola, misma pista y, si suena aquí, misma posición.
let savedRev = ''; // sala y versión de la cola ya guardadas en este navegador
function syncRoom() {
  queue = order = room.queue;
  manual = room.manual || 0;
  repeat = room.repeat ?? repeat; // el de la sala: así dos dispositivos no discrepan sobre qué sigue
  const t = queue[room.pos];
  if (!t) { // sala recién creada sin música
    audio.pause();
    pos = -1;
    $('#player').hidden = true;
    document.body.classList.remove('has-player');
  } else {
    const rev = room.code + room.qrev;
    if (pos !== room.pos || curPath !== t.path) load(room.pos, { play: false, at: roomTime() }); // y guarda la cola
    // La cola cambió sin cambiar de pista (alguien agregó, llegaron sugerencias): el tic de 5 s ya solo guarda el segundo.
    else if (rev !== savedRev) save();
    savedRev = rev;
    if (!here) {
      audio.pause();
      stream = null;
    } else syncStream(t);
  }
  syncPlayButton();
  paintQueue();
  paintJam();
  paintRemote();
}

// syncStream mantiene sonando el flujo de la sala en este dispositivo. Mientras la sala siga su curso (la misma
// pista, o la siguiente justo cuando acababa la anterior) no se toca nada: el flujo ya trae lo que toca. Solo si
// alguien saltó o adelantó hay que abrir otro, y eso se hace con la app a la vista; en segundo plano no se puede
// cargar un audio nuevo, y es el servidor el que cambia lo que manda por el mismo (se oye al acabarse el colchón).
function syncStream(t) {
  if (!room.playing) {
    if (!audio.paused) audio.pause();
    if (stream) stream.held = room.at; // dónde quedó: al reanudar ahí mismo, el flujo abierto sigue sirviendo
    return;
  }
  const origin = room.stamp - room.at * 1000; // cuándo «empezó» esta pista en el reloj de la sala
  const mark = { pos: room.pos, origin, end: origin + (t.dur || 0) * 1000 };
  const steady = stream && audio.src && (
    (room.pos === stream.pos && Math.abs(origin - stream.origin) < 1500) // la misma pista, sin saltos
    || (room.pos === after(stream.pos) && Math.abs(origin - stream.end) < 3000) // pasó sola a la que sigue (con repetir: la misma o la primera)
    || (stream.held != null && room.pos === stream.pos && Math.abs(room.at - stream.held) < 1.5)); // se reanudó donde estaba
  if (steady) {
    stream = mark;
    // Reanudado desde otro dispositivo: igual que en toggle, tras 60 s en pausa el flujo se da por cerrado y se abre otro.
    if (audio.paused) Date.now() - pausedAt > 60000 ? openStream() : audio.play().catch(streamBlocked);
    return;
  }
  // En segundo plano el iPhone no deja abrir otro audio: se salta el colchón dentro del mismo. Chrome en Android
  // no deja moverse dentro de un audio en vivo, pero sí abrir otro: ahí se reabre como con la app a la vista.
  if (stream && audio.src && !audio.paused && document.visibilityState !== 'visible' && apple) {
    stream = mark;
    // El servidor ya reinició el flujo por este cambio y dice dónde empieza lo nuevo (skip): se salta hasta ahí sin
    // pedirle nada. Si ese punto ya quedó atrás o no vino, se le pide «cut» como antes.
    const k = skipMark && skipMark.n !== skipDone && skipMark.at >= audio.currentTime ? skipMark : null;
    if (k) skipDone = k.n;
    return void skipBuffer(k?.at);
  }
  stream = mark;
  openStream();
}
// after: qué pista suena cuando termina la i, o -1 si la cola se acaba ahí. La misma regla que after() en jam.go.
const after = (i) => (repeat === 2 ? i : i + 1 < queue.length ? i + 1 : repeat === 1 && queue.length ? 0 : -1);
function openStream() {
  audio.src = `/api/jam/stream?device=${jam.deviceId}&t=${Date.now()}`;
  audio.play().then(() => (warned = false)).catch(streamBlocked);
}
const apple = /iPhone|iPad|iPod/.test(navigator.userAgent) || (/Macintosh/.test(navigator.userAgent) && navigator.maxTouchPoints > 1);
// streamDo salta, devuelve o adelanta en el flujo continuo. La acción viaja en la misma petición que abre el audio
// nuevo, y esa petición se hace aquí mismo, dentro del toque: la canción anterior se corta al instante y, como
// el cambio lo pidió el usuario (también desde la pantalla bloqueada), iOS lo deja cargar. La sala se adelanta
// aquí para pintar ya la pista nueva; la foto del servidor dirá lo mismo.
function streamDo(op, arg = {}) {
  const from = room.pos;
  const p = op === 'next' ? (repeat && from + 1 >= queue.length ? 0 : from + 1) : op === 'prev' ? Math.max(0, from - 1) : op === 'jump' ? arg.index : from;
  const at = op === 'seek' ? Math.max(0, arg.at || 0) : 0;
  const known = !!queue[p]; // «siguiente» al final de la cola: las sugerencias las pone el servidor y llegan con la foto
  const now = Date.now() + offset;
  // El flujo que se abre ya es el de la pista pedida: cuando llegue la foto con ese cambio no hay que abrir otro.
  stream = { pos: p, origin: now - at * 1000, end: now + ((queue[p]?.dur || 0) - at) * 1000 };
  if (known) {
    room = { ...room, pos: p, at, stamp: now, playing: true };
    if (p !== pos) load(p, { play: false, at });
  }
  const q = new URLSearchParams({ device: jam.deviceId, op, from, n: `${Date.now()}${Math.random()}` });
  if (op === 'jump') { q.set('index', arg.index); if (arg.path) q.set('path', arg.path); }
  if (op === 'seek') q.set('at', at);
  audio.src = `/api/jam/stream?${q}`;
  audio.play().then(() => (warned = false)).catch(streamBlocked);
  syncPlayButton();
  paintQueue();
  paintRemote();
}
// skipBuffer hace oír de inmediato un cambio que hizo otro dispositivo con este en segundo plano. El audio que
// suena no se suelta (ahí iOS no siempre deja abrir otro): se le pide al servidor que vuelva a empezar el envío en
// lo que la sala suena ahora, y cuando eso empieza a llegar el reproductor se adelanta dentro del mismo audio
// hasta ese punto, dejando atrás el colchón con la canción anterior. Si algo falla, la música sigue y el cambio
// se oye al acabarse el colchón, como antes.
const edge = () => (audio.buffered.length ? audio.buffered.end(audio.buffered.length - 1) : NaN);
let skipping = false;
let skipMark = null; // último reinicio del flujo que anunció el servidor en una foto: { n, at }
let skipDone = 0; // el n del último reinicio ya atendido
async function skipBuffer(known) {
  if (skipping) return;
  skipping = true;
  const r = known == null ? await jam.send('cut', {}, true) : { cut: known };
  // Dónde empieza lo nuevo: el servidor lo dice (r.cut), porque lo viejo puede seguir en camino por la red. Si
  // no lo dice, o no cuadra con lo que suena, se toma lo recibido hasta la respuesta, como antes.
  const got = edge();
  const exact = r?.cut >= audio.currentTime && r.cut < got + 15;
  const from = exact ? r.cut : got;
  const started = Date.now();
  const stop = () => {
    skipping = false;
    audio.removeEventListener('progress', check);
    audio.removeEventListener('timeupdate', check);
  };
  function check() {
    if (edge() >= from + 0.5) { // ya llegó medio segundo de lo nuevo: alcanza para caer en él sin quedarse sin audio
      stop();
      const to = from + (exact ? 0.05 : 0.3);
      try { audio.currentTime = to; } catch (e) { console.warn('No se pudo saltar el colchón:', e); }
    } else if (Date.now() - started > 12000) stop(); // lo viejo en camino puede tardar en llegar: es el colchón entero
  }
  if (!r || !(from >= 0) || !streaming()) return stop();
  audio.addEventListener('progress', check); // estos eventos sí llegan con la pantalla bloqueada; los temporizadores no siempre
  audio.addEventListener('timeupdate', check);
}
// needTap: el navegador no dejó sonar sin un toque (pasa al reabrir la app con la sala sonando). Si nadie más está
// oyendo, la sala simplemente queda en pausa; el aviso solo sale cuando hay otros dispositivos o es un jam.
function needTap() {
  if (!room.open && devices.filter((d) => d.active).length <= 1) return void jam.send('pause', {}, true);
  toast('Toca reproducir para escuchar en este dispositivo.', { kind: 'warn' });
}
function streamBlocked(e) {
  pausedAt = Date.now(); // un play() rechazado no dispara pause: sin esto cada foto reabriría el flujo
  warnPlay(e);
  if (e.name !== 'NotAllowedError' || warned || document.visibilityState !== 'visible') return;
  warned = true; // el navegador exige un toque antes de sonar: el botón de reproducir lo destraba
  needTap();
}
// streamLost: el flujo se cortó (red, servidor reiniciado). Se reabre si la sala sigue sonando y la app está a la
// vista; si no, al volver a la app. Dos cortes seguidos no se insisten: sería un bucle contra un servidor caído.
let lostAt = 0;
function streamLost() {
  stream = null;
  const again = Date.now() - lostAt < 8000;
  lostAt = Date.now();
  if (!streaming() || !room.playing) return;
  if (again || document.visibilityState !== 'visible') return; // wake lo reabre al volver a la app
  syncRoom();
}

// Con la música sonando en otro dispositivo el audio de aquí está quieto, y en el flujo continuo el audio no
// sabe en qué pista va: en los dos casos el avance se calcula con el reloj de la sala.
let stateAt = 0;
let askedAt = 0;
let asking = false; // hay una pregunta «state» sin responder
let edgeAt = 0, edgeWas = 0; // cuándo y hasta dónde había llegado el audio en la última medida
let edgeTop = 0, grewAt = 0; // lo más lejos que ha llegado y cuándo creció por última vez
function paintRemote() {
  if (!room || pos < 0) return;
  if (streaming() && room.playing) {
    // En segundo plano el flujo de eventos puede haberse caído y nadie avisa que la sala pasó de pista: se
    // deduce aquí con su mismo reloj (el servidor avanza justo al final), para que la pantalla de bloqueo
    // muestre la canción que suena. La foto que llegue después dirá lo mismo.
    const length = queue[pos].dur || 0;
    const n = after(pos);
    if (length > 0 && roomTime() >= length && room.pos === pos && n >= 0) {
      room = { ...room, pos: n, at: 0, stamp: room.stamp + (length - room.at) * 1000 };
      return syncRoom();
    }
    resume = Math.min(roomTime(), queue[pos].dur || Infinity); // para retomar aquí si la sala se cierra
    if (resume >= 30) count();
    if (Date.now() - stateAt > 5000) { stateAt = Date.now(); positionState(); saveAt(); }
    // En segundo plano el flujo de eventos puede morir y nadie avisaría de un cambio hecho desde otro dispositivo.
    // El aviso es el audio mismo: si alguien salta de canción, el servidor reinicia este flujo y rellena el colchón
    // de golpe (stream.go), y si pausa, deja de mandar. Cuando el audio llega así de raro se pregunta al servidor
    // qué pasó, en vez de preguntar cada segundo por si acaso; cada 15 s se pregunta de todos modos, por si un
    // navegador no deja ver cómo llega. Lo mueve timeupdate, que sí corre con la pantalla bloqueada.
    // Una pregunta que lleva 5 s sin respuesta se da por perdida: en segundo plano la red puede dejarla colgada.
    const now = Date.now(), e = edge(), dt = (now - edgeAt) / 1000;
    // Ráfaga: llegaron más de 3 s de audio de más desde la última medida (normalmente llega 1 s por segundo).
    // dt grande es la primera medida o que el navegador durmió la página: ahí no se puede saber.
    const burst = e - edgeWas - dt > 3 && dt < 10;
    if (burst || dt >= 2) { edgeAt = now; edgeWas = e; }
    // Corte: lleva más de 1,5 s sin llegar nada. Un flujo recién abierto empieza de nuevo desde cero: no es un corte.
    if (!(e <= edgeTop) || e < edgeTop - 1) { edgeTop = e; grewAt = now; }
    const odd = burst || (now - grewAt > 1500 && !audio.paused);
    const since = now - askedAt;
    if (document.visibilityState !== 'visible' && since > (asking ? 5000 : odd ? 1000 : 15000)) {
      askedAt = now;
      asking = true;
      jam.send('state', {}, true).finally(() => (asking = false));
    }
  }
  if (seeking) return;
  const d = queue[pos].dur || 0;
  const cur = d ? Math.min(roomTime(), d) : roomTime();
  for (const s of seeks) setSeek(s, d ? cur / d : 0);
  paintTimes(cur, d);
}
setInterval(paintRemote, 500);

