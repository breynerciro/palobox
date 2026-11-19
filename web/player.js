// Reproductor de música: cola, aleatorio, repetir y la barra que sigue sonando al cambiar de sección.
import { $, el, icon, setIcon, json, joinText, lazyImg, toast, clock, plural, sleep, ask } from './lib.js';
import { rawUrl, thumbUrl } from './nav.js';
import { api } from './session.js';
import { ensureLib, markLikes, toggleLike } from './music.js';
import * as jam from './jam.js';
import * as offline from './offline.js';

export const audio = new Audio();
audio.preload = 'metadata';
audio.hidden = true;
// Dentro de la página y no suelto: así lo tenía la versión que sí mostraba la reproducción en la
// Dynamic Island y la pantalla bloqueada del iPhone.
document.body.append(audio);
// Sesión de audio de reproducción (Audio Session API, Safari 17+): sin esto iOS puede tratar la app como
// sonido secundario y silenciarla en segundo plano o con el interruptor de silencio.
// shortcut: iOS sigue sin dejar que una web cambie de pista en segundo plano (WebKit, desde iOS 17); si persiste,
// la salida es un solo flujo continuo (ManagedMediaSource) o una app nativa.
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
let epoch = 0; // cambia al reemplazar la cola: descarta sugerencias pedidas para lo anterior
let filling = false;

// Sala de reproducción (jam.js): con sala, la cola y la posición son las del servidor y este reproductor
// las refleja; cada acción se manda como comando en vez de cambiar el estado local. Hay sala cuando elijo
// en cuál de mis dispositivos suena, o en un jam con otros usuarios.
let room = null; // la foto de la sala; null = reproductor local
let devices = []; // mis dispositivos conectados
let here = true; // en este dispositivo es donde suena para mi cuenta
let offset = 0; // reloj del servidor menos el de este dispositivo
let curPath = ''; // pista cargada en el audio
let ahead = null; // { from, until }: pista de la sala que este dispositivo ya dejó atrás por su cuenta
let warned = false; // ya se avisó que el navegador no dejó sonar sin un toque
let syncedAt = 0;
const roomTime = () => (room.playing ? room.at + (Date.now() + offset - room.stamp) / 1000 : room.at);
const playing = () => (room && !here ? room.playing : !audio.paused);

// Flujo continuo (iPhone). iOS deja que una página siga sonando en segundo plano mientras sea el mismo audio,
// pero a veces bloquea cargar uno nuevo, que es lo que pasa al terminar cada canción. Aquí el iPhone siempre
// reproduce a través de una sala: el audio es una sola respuesta del servidor (/api/jam/stream) que trae la
// pista de la sala y luego la que sigue, sin cambiar de fuente. La cola, la pista y la posición son las de la
// sala; esta página pinta el avance con el reloj de la sala y manda cada acción como comando, igual que en un jam.
// Se fuerza o se apaga con localStorage «palobox.flujo» = 1 / 0.
const IOS = /iP(hone|ad|od)/.test(navigator.userAgent) || (navigator.maxTouchPoints > 1 && /Macintosh/.test(navigator.userAgent)); // el iPad se presenta como Mac
let STREAM = (() => {
  let flag = null;
  try { flag = localStorage.getItem('palobox.flujo'); } catch {}
  return flag === '1' || (flag !== '0' && IOS);
})();
// Sin conexión no hay servidor que mande el flujo: el reproductor vuelve a ser local, sobre un solo audio armado
// en el celular con lo descargado (deck, más abajo).
const STREAM_ON = STREAM; // el modo con conexión de este dispositivo, para volver a él
// local: el reproductor suena desde lo descargado. Sigue así un rato después de volver la conexión (goOnline)
// para no cortar la canción; por eso es distinto de offline.active, que es el modo de la app.
let local = false;
export function goOffline() {
  if (local) return void keepDownloaded(deckTime(), !audio.paused); // ya sonaba desde lo descargado
  local = true;
  // Si la red se cae con la app abierta, lo que sonaba sigue donde iba, ya desde lo descargado.
  const at = room ? roomTime() : audio.currentTime || resume;
  const play = here && (room ? room.playing : !audio.paused);
  STREAM = false;
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
  STREAM = STREAM_ON;
  audio.pause();
  audio.removeAttribute('src');
  if (deck) URL.revokeObjectURL(deck.url);
  deck = null;
  curPath = '';
  jam.connect(applyJam);
  edit?.();
  if (!keep || !queue[pos]) return;
  load(pos, { play: on, at });
  if (on && STREAM) startRoom({ playing: true });
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
  epoch++;
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
const streaming = () => STREAM && !!room && here;
const dur = () => (deck ? deck.lens[pos] : 0) || (streaming() ? 0 : audio.duration) || queue[pos]?.dur || 0;

// Sin conexión toda la cola es un solo audio: las canciones descargadas, ya limpias, unidas una tras otra. El
// teléfono nunca carga un audio nuevo al pasar de canción (lo que iOS no siempre deja con la pantalla bloqueada),
// y saltar es moverse dentro del mismo audio. deck guarda dónde empieza y cuánto dura cada pista en ese audio.
let deck = null; // { key, url, starts, lens }
let deckBusy = false;
const deckKey = () => queue.map((t) => t.path).join('\n');
const deckTime = () => Math.max(0, audio.currentTime - (deck?.starts[pos] ?? 0)); // segundo dentro de la pista actual
async function deckOpen(at, play) {
  const key = deckKey();
  if (deck?.key === key) audio.currentTime = deck.starts[pos] + at;
  else {
    deckBusy = true;
    let parts;
    try { parts = await offline.blobs(queue.map((t) => t.path)); } catch (e) { deckBusy = false; return void toast(e.message, { kind: 'warn', sticky: true }); }
    deckBusy = false;
    if (deckKey() !== key) return; // la cola cambió mientras se leía: ya viene otra apertura
    if (deck) URL.revokeObjectURL(deck.url);
    const starts = [];
    let total = 0;
    for (const p of parts) { starts.push(total); total += p.secs; }
    deck = { key, url: URL.createObjectURL(new Blob(parts.map((p) => p.blob), { type: 'audio/mpeg' })), starts, lens: parts.map((p) => p.secs) };
    audio.src = deck.url;
    const target = deck.starts[pos] + at;
    if (target > 0) audio.addEventListener('loadedmetadata', () => (audio.currentTime = target), { once: true });
  }
  if (play) audio.play().catch(warnPlay);
}
// deckTick sigue al audio unido: cuando pasa solo a la pista siguiente, aquí solo hay que pintarla.
function deckTick() {
  const now = audio.currentTime;
  let k = pos;
  while (k + 1 < queue.length && now >= deck.starts[k + 1] - 0.02) k++;
  while (k > 0 && now < deck.starts[k] - 0.02) k--;
  if (k !== pos) {
    if (repeat === 2 && k === pos + 1 && !audio.seeking) return void (audio.currentTime = deck.starts[pos]); // repetir esta canción
    if (k === pos + 1) count();
    pos = k;
    counted = false;
    manual = Math.max(0, manual - 1);
    curPath = queue[k].path;
    present(queue[k], 0);
    positionState(); // el audio unido no avisa el cambio de pista: sin esto la pantalla de bloqueo sigue con la anterior
  }
  const at = deckTime();
  if (!audio.paused && at >= 30) count();
  if (Date.now() - savedAt > 5000) { save(); positionState(); } // y se corrige la deriva cada 5 s
  if (seeking) return;
  for (const s of seeks) setSeek(s, deck.lens[pos] ? at / deck.lens[pos] : 0);
  paintTimes(at, deck.lens[pos]);
}
const warnPlay = (e) => console.warn('El navegador no dejó reproducir:', e?.name, e?.message);
let stuck = false; // un play() sin toque fue rechazado: se reintenta al volver a la app

// La siguiente pista se descarga entera mientras suena la actual y se pone en el mismo audio con un blob:
// así el cambio al terminar no espera la red. En segundo plano, un hueco de silencio mientras carga basta para
// que Android o iOS den la reproducción por terminada. Mismo elemento: conserva la sesión de medios de iOS.
let cur = null; // { path, url } blob de la pista cargada
let ready = null; // { path, url } blob de la que sigue
let tried = ''; // pista que ya se intentó descargar
function srcFor(t) {
  if (cur?.path !== t.path) {
    if (cur) URL.revokeObjectURL(cur.url);
    cur = ready?.path === t.path ? ready : null;
    if (cur) ready = null;
  }
  return cur?.url ?? rawUrl('musica', t.path);
}
async function prefetch() {
  const t = queue[pos + 1] ?? (repeat === 1 ? queue[0] : null);
  if (STREAM || local || !t || !here || t.path === tried || t.path === ready?.path || t.path === cur?.path) return;
  tried = t.path;
  try {
    const r = await fetch(rawUrl('musica', t.path));
    // shortcut: un archivo de más de 80 MB (FLAC largo) no se guarda en memoria y carga de la red como antes.
    if (!r.ok || +r.headers.get('content-length') > 80e6) return void r.body?.cancel();
    const url = URL.createObjectURL(await r.blob());
    if (ready) URL.revokeObjectURL(ready.url);
    ready = { path: t.path, url };
  } catch (e) { console.warn('No se pudo adelantar la descarga de la siguiente pista:', e); }
}
// Posición en la pantalla de bloqueo y la notificación; sin esto Android puede dar la sesión por inactiva.
function positionState() {
  const total = deck ? deck.lens[pos] : streaming() ? queue[pos]?.dur : audio.duration; // el flujo no tiene duración: vale la de la pista
  if (!navigator.mediaSession?.setPositionState || !(total > 0 && total < Infinity)) return;
  const at = deck ? deckTime() : streaming() ? roomTime() : audio.currentTime;
  try { navigator.mediaSession.setPositionState({ duration: total, position: Math.max(0, Math.min(at, total)), playbackRate: audio.playbackRate || 1 }); } catch {}
}
for (const ev of ['loadedmetadata', 'playing', 'pause', 'seeked', 'ratechange']) audio.addEventListener(ev, positionState);
audio.addEventListener('playing', () => (stuck = false));
// begin pone a sonar t dentro del toque del usuario: el navegador solo deja empezar el audio tras un gesto,
// y la foto de la sala llega después.
function begin(t) {
  if (STREAM) return; // el audio es el flujo de la sala: lo abre syncStream cuando llega la foto
  curPath = t.path;
  audio.src = srcFor(t);
  audio.play().catch(warnPlay);
}

const mix = (a) => {
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [a[i], a[j]] = [a[j], a[i]];
  }
  return a;
};
const cover = (t) => [icon('music'), lazyImg(thumbUrl('musica', t.albumPath))];

function load(i, { play = true, at = 0 } = {}) {
  if (i < 0 || i >= queue.length) return;
  pos = i;
  counted = false;
  resume = at;
  const t = queue[i];
  if (local) { // sin conexión: el audio unido de la cola, movido hasta esta pista
    curPath = t.path;
    deckOpen(at, play);
  } else if (STREAM) curPath = t.path; // aquí solo se pinta: el audio es el flujo de la sala (syncStream), no el archivo
  else if (!room || curPath !== t.path) { // en una sala la pista que ya está cargada no se vuelve a pedir
    curPath = t.path;
    audio.src = srcFor(t);
    if (at) audio.addEventListener('loadedmetadata', () => (audio.currentTime = room ? roomTime() : at), { once: true });
  }
  if (play && !STREAM && !local) audio.play().catch((e) => { stuck = e.name === 'NotAllowedError'; warnPlay(e); }); // si el formato no sirve, lo informa onerror
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
  for (const b of document.querySelectorAll('#player .like, #np .like')) b.dataset.like = t.path;
  for (const s of seeks) setSeek(s, 0);
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
  refill();
}

// Sugerencias: cuando a la cola le quedan menos de 6 pistas, el servidor propone qué sigue y la música no se detiene.
async function refill() {
  // En una sala las sugerencias las pone el servidor; sin conexión se agregan al armar la cola (playAt), porque
  // cambiarla después obliga a rearmar el audio unido.
  if (local || room || filling || repeat || pos < 0 || queue.length - pos > 6) return;
  filling = true;
  const e = epoch;
  let more = null;
  try {
    const r = await api(`/api/music/radio?path=${encodeURIComponent(queue[pos].path)}`);
    if (r.ok) more = await r.json();
  } catch {} // sin conexión: la cola termina como antes
  filling = false;
  if (e !== epoch) return refill();
  const have = new Set(queue.map((t) => t.path));
  const add = (more || []).filter((t) => !have.has(t.path)).slice(0, 20).map((t) => ({ ...t, auto: true }));
  if (!add.length) return;
  queue.push(...add);
  order.push(...add);
  paintQueue();
  save();
  if (audio.ended) next(); // la cola se acabó mientras llegaban
}

// playAt reemplaza la cola por tracks y empieza en la pista i.
export function playAt(tracks, i) {
  if (i < 0 || i >= tracks.length) return;
  toNormal(null, false); // volvió la conexión y se elige música nueva: ya suena por el camino normal
  if (local && !repeat && tracks.length - i < 6) tracks = [...tracks, ...offline.more(tracks)]; // que la música siga
  if (room) {
    const from = Math.max(0, i - 50); // la sala guarda hasta 500 pistas alrededor de la elegida
    if (here) begin(tracks[i]);
    let list = tracks.slice(from, from + 500);
    let at = i - from;
    if (STREAM && shuffle && !room.open) { // en la sala propia del flujo el aleatorio se aplica al armar la cola
      list = [list[at], ...mix(list.filter((_, k) => k !== at))];
      at = 0;
    }
    return void jam.send('queue', { paths: list.map((t) => t.path), pos: at });
  }
  order = tracks.map((t) => ({ ...t })); // copias: la misma pista puede estar dos veces en la cola
  manual = 0;
  epoch++;
  if (shuffle) {
    const first = order[i];
    queue = [first, ...mix(order.filter((t) => t !== first))];
    load(0);
  } else {
    queue = order.slice();
    load(i);
  }
  if (STREAM) startRoom({ playing: true, at: 0 }); // en flujo continuo todo suena a través de una sala
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

function next(ended = false) {
  if (streaming() && !ended) return streamDo('next');
  // «ended» lleva la pista que terminó: si otro dispositivo de la sala ya avisó, el servidor no avanza dos veces.
  if (room) {
    const from = pos;
    // Al terminar una pista, la siguiente arranca aquí mismo y de inmediato, sin esperar la respuesta del servidor:
    // en segundo plano el navegador solo deja seguir sonando si el cambio ocurre en el instante en que termina.
    if (ended && here && !STREAM && queue[pos + 1]) {
      ahead = { from, until: Date.now() + 8000 };
      curPath = ''; // aunque sea la misma canción dos veces seguidas, se carga de nuevo
      load(pos + 1);
    }
    return void jam.send(ended ? 'ended' : 'next', { pos: from });
  }
  if (ended && repeat === 2) return load(pos);
  let n = pos + 1;
  if (n >= queue.length) {
    if (!repeat) return; // se acabó la cola
    n = 0;
  }
  manual = Math.max(0, manual - 1);
  load(n);
  if (STREAM) startRoom({ playing: true, at: 0 });
}
function prev() {
  if (streaming()) return streamDo(roomTime() > 3 || pos === 0 ? 'seek' : 'prev', { at: 0 });
  if (room) return void (roomTime() > 3 || pos === 0 ? jam.send('seek', { at: 0 }) : jam.send('prev'));
  if (STREAM) { // cola restaurada, todavía sin sala: la anterior (o la misma desde el principio) arranca en una
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

const REPEAT = ['Repetir', 'Repetir: toda la cola', 'Repetir: esta canción'];
export function syncToggles() {
  for (const b of document.querySelectorAll('[data-act=shuffle]')) b.setAttribute('aria-pressed', shuffle);
  for (const b of document.querySelectorAll('[data-act=repeat]')) {
    b.setAttribute('aria-pressed', repeat > 0);
    b.setAttribute('aria-label', REPEAT[repeat]);
    setIcon(b, repeat === 2 ? 'repeat-1' : 'repeat');
  }
}
// Los botones de aleatorio, repetir y cola existen en la barra, en pantalla completa y en las vistas.
document.addEventListener('click', (e) => {
  const act = e.target.closest?.('[data-act]')?.dataset.act;
  if (act === 'jam' && offline.active) toast(offline.NEEDS_NET);
  else if (act === 'jam') { toNormal(); $('#jam').showModal(); paintJam(); }
  else if (act === 'shuffle' && STREAM && room && !room.open && here) streamShuffle();
  else if (act === 'repeat' && STREAM && room && !room.open) toast('Repetir no está disponible con la reproducción en segundo plano del iPhone: al acabarse la cola siguen sugerencias.');
  else if (room && (act === 'shuffle' || act === 'repeat')) toast('Aleatorio y repetir no están disponibles mientras la reproducción es compartida.');
  else if (act === 'shuffle') setShuffle(!shuffle);
  else if (act === 'repeat') { repeat = (repeat + 1) % 3; syncToggles(); save(); refill(); }
  else if (act === 'queue') { $('#queue').showModal(); paintQueue(); }
});
for (const b of document.querySelectorAll('#player .like, #np .like')) b.onclick = () => b.dataset.like && toggleLike(b.dataset.like);

// markPlaying resalta en la vista la pista que suena y pone al día los botones compartidos.
export function markPlaying() {
  const current = queue[pos]?.path;
  for (const r of document.querySelectorAll('[data-track]')) {
    const on = r.dataset.track === current;
    r.classList.toggle('playing', on);
    if (on) r.setAttribute('aria-current', 'true'); else r.removeAttribute('aria-current');
    r.querySelector('.num')?.replaceChildren(on ? icon(playing() ? 'play' : 'pause') : String([...r.closest('ul').children].indexOf(r.parentElement) + 1));
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
audio.onpause = () => { syncPlayButton(); save(); };
audio.onended = () => {
  if (deck) { // se acabó el audio unido: toda la cola
    count();
    return void (repeat === 1 && queue.length && load(0));
  }
  if (STREAM) return streamLost(); // el flujo no termina con cada pista: si se acabó es que se cortó
  count();
  next(true);
};
let retried = ''; // pista que ya se volvió a pedir tras un error de carga
audio.addEventListener('playing', () => (retried = ''));
audio.onerror = () => {
  if (deck) return void toast('No se pudo reproducir la música descargada. Vuelve a descargarla cuando tengas conexión.', { kind: 'warn', sticky: true });
  if (STREAM) return streamLost();
  const t = queue[pos];
  if (!t) return;
  if (retried !== t.path) { // un corte de red o un blob roto: se pide una vez más a la red, en el mismo punto
    retried = t.path;
    const at = audio.currentTime;
    if (cur) URL.revokeObjectURL(cur.url);
    cur = null;
    audio.src = rawUrl('musica', t.path);
    if (at) audio.addEventListener('loadedmetadata', () => (audio.currentTime = at), { once: true });
    return void audio.play().catch(warnPlay);
  }
  toast(`Este dispositivo no puede reproducir «${queue[pos].title}». Los formatos más compatibles son MP3 y M4A.`, { kind: 'warn', sticky: true });
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
  if (STREAM) return paintRemote(); // el avance del flujo se pinta con el reloj de la sala; este evento sí llega en segundo plano
  if (!audio.duration) return;
  if (!audio.paused && audio.currentTime >= 30) count();
  if (Date.now() - savedAt > 5000) save();
  if (audio.currentTime > 5) prefetch(); // pasado el arranque, para no quitarle red a la pista actual
  // En una sala, este dispositivo se vuelve a alinear si se alejó de la posición común (como mucho cada 5 s).
  if (room && here && room.playing && Date.now() - syncedAt > 5000 && Math.abs(audio.currentTime - roomTime()) > 1.5) {
    syncedAt = Date.now();
    audio.currentTime = roomTime();
  }
  if (seeking) return;
  for (const s of seeks) setSeek(s, audio.currentTime / audio.duration);
  paintTimes(audio.currentTime, audio.duration);
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
  if (!STREAM) return void (audio.currentTime = at);
  resume = at; // cola restaurada en flujo continuo, aún sin sala: arrancará desde aquí
  paintTimes(at, dur());
}
function toggle() {
  if (STREAM && !room) return void (pos >= 0 && startRoom({ playing: true })); // cola restaurada: arranca en una sala
  if (!room) return audio.paused ? audio.play().catch(warnPlay) : audio.pause();
  if (!here && !devices.some((d) => d.active)) { // el dispositivo donde sonaba se desconectó: reproducir aquí lo toma
    here = true;
    jam.send('here', { target: jam.deviceId });
  }
  if (streaming()) {
    // Pausar y reanudar no sueltan el flujo: el teléfono guarda su colchón y sigue con el mismo audio, dentro del
    // propio toque (o del botón de la pantalla bloqueada), que es lo único que iOS deja hacer sin la app abierta.
    const on = !audio.paused;
    if (on) audio.pause();
    else if (stream && audio.src) audio.play().catch(warnPlay);
    return void jam.send(on ? 'pause' : 'play');
  }
  const on = playing();
  if (here && !on) audio.play().catch(warnPlay); // dentro del toque; también destraba el audio si el navegador no lo dejó empezar solo
  if (on || !room.playing) jam.send(on ? 'pause' : 'play');
}
$('#playpause').onclick = $('#np-play').onclick = toggle;
$('#next').onclick = $('#np-next').onclick = () => next();
$('#prev').onclick = $('#np-prev').onclick = prev;
$('#player-open').onclick = () => $('#np').showModal();
$('#np-close').onclick = () => $('#np').close();
$('#queue-close').onclick = () => $('#queue').close();
$('#queue').onclick = (e) => e.target === e.currentTarget && e.currentTarget.close(); // clic en el telón
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

// Gestos de la barra mini: deslizar a los lados cambia de canción; hacia abajo la oculta (solo en la Nube:
// en Música el reproductor es el centro de la sección y ese arrastre debe seguir desplazando la página).
const bar = $('#player');
const strip = bar.querySelector('.player-left'); // lo que acompaña al dedo al cambiar de canción
const reduce = matchMedia('(prefers-reduced-motion: reduce)');
let drag = null; // { id, x, y, t, mode: '' | 'x' | 'y' }
let swallowUntil = 0; // tras un arrastre, el click que le sigue no abre la pantalla completa
function tuck(on) {
  const had = bar.contains(document.activeElement) || document.activeElement === $('#player-tab');
  document.body.classList.toggle('player-tucked', on);
  for (const n of [bar, strip]) slideTo(n, '', '');
  if (had) $(on ? '#player-tab' : '#player-open').focus(); // el foco no se pierde con el botón que desaparece
}
$('#player-tuck').onclick = () => tuck(true);
$('#player-tab').onclick = () => tuck(false);
function slideTo(n, transform, opacity, ms = 0) {
  n.style.transition = ms && !reduce.matches ? `transform ${ms}ms var(--ease), opacity ${ms}ms` : 'none';
  n.style.transform = transform;
  n.style.opacity = opacity;
}
const canGo = (dir) => (dir > 0 ? pos + 1 < queue.length || repeat > 0 : pos > 0);
async function swipeTo(dir) {
  const ms = reduce.matches ? 0 : 160;
  slideTo(strip, `translateX(${-dir * bar.offsetWidth}px)`, 0, ms);
  await sleep(ms);
  if (dir > 0) next();
  else if (room) jam.send('prev');
  else { manual = 0; load(pos - 1); } // a diferencia del botón, deslizar siempre cambia de canción
  slideTo(strip, `translateX(${dir * 48}px)`, 0);
  strip.offsetWidth; // aplica la posición de entrada antes de animar
  slideTo(strip, '', '', ms);
}
bar.addEventListener('pointerdown', (e) => {
  if (!e.isPrimary || e.button !== 0 || e.target.closest('input')) return; // la barra de avance se arrastra sola
  drag = { id: e.pointerId, x: e.clientX, y: e.clientY, t: e.timeStamp, mode: '' };
});
bar.addEventListener('pointermove', (e) => {
  if (drag?.id !== e.pointerId) return;
  const dx = e.clientX - drag.x;
  const dy = e.clientY - drag.y;
  if (!drag.mode) {
    if (Math.hypot(dx, dy) < 12) return; // todavía puede ser un toque
    drag.mode = Math.abs(dx) > Math.abs(dy) * 1.5 ? 'x' : dy > 0 && !document.body.classList.contains('music') ? 'y' : '';
    if (!drag.mode) return (drag = null);
    bar.setPointerCapture(e.pointerId);
  }
  if (drag.mode === 'x') slideTo(strip, `translateX(${canGo(-Math.sign(dx)) ? dx : dx / 4}px)`, 1 - Math.min(Math.abs(dx) / bar.offsetWidth, 0.6));
  else slideTo(bar, `translateY(${Math.max(0, dy)}px)`, 1 - Math.min(Math.max(0, dy) / 160, 0.6));
});
function dragEnd(e) {
  if (drag?.id !== e.pointerId) return;
  const { mode, x, y, t } = drag;
  drag = null;
  const dx = e.clientX - x;
  const dy = e.clientY - y;
  const up = e.type === 'pointerup';
  if (mode) swallowUntil = e.timeStamp + 400;
  // Cambia con un cuarto del ancho, o con un gesto rápido corto.
  if (mode === 'x' && up && canGo(-Math.sign(dx)) && (Math.abs(dx) > bar.offsetWidth / 4 || (Math.abs(dx) > 40 && Math.abs(dx) / (e.timeStamp - t) > 0.5))) return swipeTo(-Math.sign(dx));
  if (mode === 'y' && up && dy > 48) return tuck(true);
  slideTo(mode === 'y' ? bar : strip, '', '', 200);
}
bar.addEventListener('pointerup', dragEnd);
bar.addEventListener('pointercancel', dragEnd); // el navegador tomó el gesto (desplazamiento vertical en Música)
// Pantalla completa: deslizar hacia abajo la cierra y vuelve a la barra pequeña.
const np = $('#np');
let npDrag = null; // { id, x, y, t, on }
np.addEventListener('pointerdown', (e) => {
  if (e.isPrimary && e.pointerType !== 'mouse' && !e.target.closest('input')) npDrag = { id: e.pointerId, x: e.clientX, y: e.clientY, t: e.timeStamp, on: false };
});
np.addEventListener('pointermove', (e) => {
  if (npDrag?.id !== e.pointerId) return;
  const dy = e.clientY - npDrag.y;
  if (!npDrag.on) {
    if (Math.hypot(e.clientX - npDrag.x, dy) < 12) return; // todavía puede ser un toque
    if (dy < Math.abs(e.clientX - npDrag.x) * 1.5) return (npDrag = null); // hacia arriba o de lado
    npDrag.on = true;
    np.setPointerCapture(e.pointerId);
  }
  slideTo(np, `translateY(${Math.max(0, dy)}px)`, '');
});
async function npEnd(e) {
  if (npDrag?.id !== e.pointerId) return;
  const { on, y, t } = npDrag;
  npDrag = null;
  if (!on) return;
  swallowUntil = e.timeStamp + 400; // el botón donde empezó el gesto no se activa
  const dy = e.clientY - y;
  if (e.type === 'pointerup' && (dy > np.offsetHeight / 4 || (dy > 60 && dy / (e.timeStamp - t) > 0.5))) {
    const ms = reduce.matches ? 0 : 200;
    slideTo(np, 'translateY(100%)', '', ms);
    await sleep(ms);
    np.close();
    return slideTo(np, '', '');
  }
  slideTo(np, '', '', 200);
}
np.addEventListener('pointerup', npEnd);
np.addEventListener('pointercancel', npEnd);
np.addEventListener('click', (e) => {
  if (e.timeStamp > swallowUntil) return;
  swallowUntil = 0;
  e.stopPropagation();
  e.preventDefault();
}, true);

bar.addEventListener('click', (e) => {
  if (e.timeStamp > swallowUntil) return;
  swallowUntil = 0;
  e.stopPropagation();
  e.preventDefault();
}, true);

// Atajos de escritorio: espacio pausa, flechas adelantan 5 s, Mayús + flechas cambian de pista.
document.addEventListener('keydown', (e) => {
  if (pos < 0 || e.ctrlKey || e.metaKey || e.altKey || e.target.closest?.('input, textarea, select')) return;
  if (document.querySelector('dialog[open]:not(#np)')) return; // el visor de fotos y los menús usan las mismas teclas
  if (e.key === ' ' && !e.target.closest?.('button, a')) { e.preventDefault(); toggle(); }
  else if (e.key === 'ArrowRight') e.shiftKey ? next() : seekTo((room ? roomTime() : deck ? deckTime() : audio.currentTime) + 5);
  else if (e.key === 'ArrowLeft') e.shiftKey ? prev() : seekTo((room ? roomTime() : deck ? deckTime() : audio.currentTime) - 5);
});

// Cola «A continuación»

const QUEUE_MAX = 100; // filas pintadas; una cola de miles de pistas no cabe en una lista cómoda
function queueRow(t, i) {
  const b = el('button', { className: 'row' }, el('span', { className: 'cover small', style: '--tc: var(--t-audio)' }, cover(t)),
    el('div', { className: 'row-text' }, el('p', { className: 'ellipsis', textContent: t.title }), el('p', { className: 'muted ellipsis', textContent: joinText(t.artist, t.auto && 'Sugerida') })));
  if (i === pos) {
    b.classList.add('playing');
    b.setAttribute('aria-current', 'true');
    b.onclick = toggle;
    return el('li', {}, b);
  }
  b.setAttribute('aria-label', `Saltar a ${t.title}`);
  b.onclick = () => {
    if (!room) {
      manual = Math.max(0, manual - (i - pos));
      load(i);
      return void (STREAM && startRoom({ playing: true, at: 0 }));
    }
    if (streaming()) return streamDo('jump', { index: i });
    if (here) begin(t);
    jam.send('jump', { index: i });
  };
  const rm = el('button', { className: 'icon-btn' }, icon('x'));
  rm.setAttribute('aria-label', `Quitar ${t.title} de la cola`);
  rm.onclick = () => {
    if (room) { jam.send('remove', { index: i, path: t.path }); return $('#queue-close').focus(); }
    if (i <= pos + manual) manual--;
    order = order.filter((x) => x !== t);
    queue.splice(i, 1);
    paintQueue();
    save();
    $('#queue-close').focus(); // el botón quitado se llevaba el foco
  };
  return el('li', { className: 'row-wrap' }, b, rm);
}
function paintQueue() {
  if (!$('#queue').open) return;
  const body = $('#queue-body');
  if (pos < 0) return body.replaceChildren(el('p', { className: 'muted note', textContent: 'No hay nada en la cola.' }));
  const rest = queue.slice(pos + 1, pos + 1 + QUEUE_MAX);
  const more = queue.length - pos - 1 - rest.length;
  body.replaceChildren(...[
    el('h3', { className: 'section-title', textContent: 'Sonando' }),
    el('ul', { className: 'list' }, queueRow(queue[pos], pos)),
    el('h3', { className: 'section-title', textContent: 'A continuación' }),
    rest.length
      ? el('ul', { className: 'list' }, rest.map((t, k) => queueRow(t, pos + 1 + k)))
      : el('p', { className: 'muted note', textContent: repeat === 1 ? 'La cola vuelve a empezar al terminar esta canción.' : 'Esta es la última canción de la cola.' }),
    more > 0 && el('p', { className: 'muted note', textContent: `Y ${plural(more, 'canción más', 'canciones más')}.` }),
  ].filter(Boolean));
}

// Cola, pista y posición se recuerdan en este navegador para retomar al volver a abrir la app.

const KEEP = 500; // pistas guardadas alrededor de la actual: localStorage tiene cupo
function save() {
  savedAt = Date.now();
  // La cola de una sala es del servidor y aquí se conserva la propia; en flujo continuo siempre hay sala, así
  // que se guarda la de la sala para retomarla al volver a abrir la app.
  if ((room && !STREAM) || pos < 0 || !user) return;
  // Sin conexión, un cambio en la cola (agregar, quitar, barajar) obliga a rearmar el audio unido donde iba.
  if (deck && !deckBusy && deck.key !== deckKey()) deckOpen(deckTime(), !audio.paused);
  const from = Math.max(0, pos - 50);
  try {
    localStorage.setItem('palobox.player', JSON.stringify({
      user, queue: queue.slice(from, from + KEEP), pos: pos - from, at: deck ? deckTime() : STREAM ? resume : audio.currentTime || resume, shuffle, repeat, volume: audio.volume,
    }));
  } catch {} // modo privado o cupo lleno: la app sigue sin recordar
}
window.addEventListener('pagehide', save);
// Al volver a la app: la sala se pone al día (el flujo de eventos pudo morir en segundo plano) y, si el
// navegador no dejó arrancar la siguiente pista sin un toque, se intenta de nuevo.
function wake() {
  if (document.visibilityState !== 'visible' || !user) return;
  if (local) return; // sin servidor no hay sala que poner al día
  if (!jam.alive()) jam.connect(applyJam); // la foto que llega llama a syncRoom
  else if (room) syncRoom();
  else if (stuck && audio.paused) audio.play().catch(warnPlay);
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
  ensureLib().catch(() => {}); // para pintar el me gusta de la pista restaurada
}

// reset vacía el reproductor al salir: la cola de un usuario no le queda al siguiente.
export function reset() {
  audio.pause();
  audio.removeAttribute('src');
  for (const b of [cur, ready]) if (b) URL.revokeObjectURL(b.url);
  cur = ready = null;
  tried = '';
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
  epoch++;
  user = '';
  for (const d of [$('#np'), $('#queue'), $('#jam')]) d.close();
  $('#player').hidden = true;
  document.body.classList.remove('has-player'); // player-tucked se queda: el siguiente usuario también lo ve minimizado
}

// Salas: elegir en qué dispositivo suena y jam con otros usuarios

// applyJam recibe cada foto del servidor: mis dispositivos y, si hay sala, su estado.
function applyJam(s) {
  const before = devices.length;
  offset = s.now - Date.now();
  devices = s.devices.map((d) => ({ ...d, me: d.id === s.me }));
  const was = room;
  room = s.room;
  here = !room || devices.some((d) => d.me && d.active);
  document.body.classList.toggle('in-room', !!room);
  document.body.classList.toggle('own-stream', STREAM && !!room && !room.open); // sala propia del flujo: no es «compartida»
  if (room) return syncRoom();
  if (was) { // la sala terminó: lo que sonaba aquí sigue sonando, ya como reproductor local
    order = queue.slice();
    save();
    if (STREAM) { // en flujo continuo «local» es otra sala, propia: se abre con la cola que quedó
      stream = null;
      const on = was.playing && !audio.paused;
      audio.pause();
      if (on) startRoom({ playing: true });
    }
  } else if (devices.length > before && devices.length > 1 && pos >= 0 && !audio.paused) {
    startRoom(); // apareció otro dispositivo mío mientras suena aquí: comparto la reproducción para que la vea y la maneje
  }
  syncPlayButton();
  paintJam();
}

// startRoom pasa la cola de este dispositivo a una sala nueva.
function startRoom(extra = {}) {
  const from = Math.max(0, pos - 50);
  return jam.send('start', {
    paths: queue.slice(from, from + 500).map((t) => t.path), pos: Math.max(0, pos - from),
    at: STREAM ? resume : audio.currentTime || resume, playing: !STREAM && !audio.paused, ...extra,
  });
}

// syncRoom deja este reproductor igual a la sala: misma cola, misma pista y, si suena aquí, misma posición.
function syncRoom() {
  queue = order = room.queue;
  // Este dispositivo ya pasó a la siguiente pista (next) y la sala todavía no: se espera sin devolverse.
  if (ahead && room.pos === ahead.from && Date.now() < ahead.until && queue[pos]?.path === curPath) {
    paintQueue();
    paintJam();
    return;
  }
  ahead = null;
  const t = queue[room.pos];
  if (!t) { // sala recién creada sin música
    audio.pause();
    pos = -1;
    $('#player').hidden = true;
    document.body.classList.remove('has-player');
  } else {
    if (pos !== room.pos || curPath !== t.path) load(room.pos, { play: false, at: roomTime() });
    if (!here) {
      audio.pause();
      stream = null;
    } else if (STREAM) syncStream(t);
    else {
      if (audio.readyState > 0 && Math.abs(audio.currentTime - roomTime()) > 1.5) audio.currentTime = roomTime();
      if (!room.playing) audio.pause();
      else if (audio.paused) {
        audio.play().then(() => (warned = false)).catch((e) => {
          if (e.name !== 'NotAllowedError' || warned) return;
          warned = true; // el navegador exige un toque antes de sonar: el botón de reproducir lo destraba
          needTap();
        });
      }
    }
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
    || (room.pos === stream.pos + 1 && Math.abs(origin - stream.end) < 3000) // pasó sola a la siguiente
    || (stream.held != null && room.pos === stream.pos && Math.abs(room.at - stream.held) < 1.5)); // se reanudó donde estaba
  if (steady) {
    stream = mark;
    if (audio.paused) audio.play().catch(streamBlocked);
    return;
  }
  if (stream && audio.src && !audio.paused && document.visibilityState !== 'visible') {
    stream = mark;
    return void skipBuffer(); // en segundo plano no se abre otro audio: se salta el colchón dentro del mismo
  }
  stream = mark;
  audio.src = `/api/jam/stream?device=${jam.deviceId}&t=${Date.now()}`;
  audio.play().then(() => (warned = false)).catch(streamBlocked);
}
// streamDo salta, devuelve o adelanta en el flujo continuo. La acción viaja en la misma petición que abre el audio
// nuevo, y esa petición se hace aquí mismo, dentro del toque: la canción anterior se corta al instante y, como
// el cambio lo pidió el usuario (también desde la pantalla bloqueada), iOS lo deja cargar. La sala se adelanta
// aquí para pintar ya la pista nueva; la foto del servidor dirá lo mismo.
function streamDo(op, arg = {}) {
  const from = room.pos;
  const p = op === 'next' ? from + 1 : op === 'prev' ? Math.max(0, from - 1) : op === 'jump' ? arg.index : from;
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
  if (op === 'jump') q.set('index', arg.index);
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
async function skipBuffer() {
  if (skipping) return;
  skipping = true;
  const ok = await jam.send('cut', {}, true);
  const from = edge(); // lo recibido hasta la respuesta es lo viejo; lo que llegue después ya es lo nuevo
  const started = Date.now();
  const stop = () => {
    skipping = false;
    audio.removeEventListener('progress', check);
    audio.removeEventListener('timeupdate', check);
  };
  function check() {
    if (edge() >= from + 0.5) { // ya llegó medio segundo de lo nuevo: alcanza para caer en él sin quedarse sin audio
      stop();
      try { audio.currentTime = from + 0.3; } catch (e) { console.warn('No se pudo saltar el colchón:', e); }
    } else if (Date.now() - started > 5000) stop();
  }
  if (!ok || !(from >= 0) || !streaming()) return stop();
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
  stuck = e.name === 'NotAllowedError'; // wake lo reintenta al volver a la app
  warnPlay(e);
  if (!stuck || warned || document.visibilityState !== 'visible') return;
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
  if (again || document.visibilityState !== 'visible') return void (stuck = true);
  syncRoom();
}

// Con la música sonando en otro dispositivo el audio de aquí está quieto, y en el flujo continuo el audio no
// sabe en qué pista va: en los dos casos el avance se calcula con el reloj de la sala.
let stateAt = 0;
let askedAt = 0;
function paintRemote() {
  if (!room || (here && !STREAM) || pos < 0) return;
  if (streaming() && room.playing) {
    // En segundo plano el flujo de eventos puede haberse caído y nadie avisa que la sala pasó de pista: se
    // deduce aquí con su mismo reloj (el servidor avanza justo al final), para que la pantalla de bloqueo
    // muestre la canción que suena. La foto que llegue después dirá lo mismo.
    const length = queue[pos].dur || 0;
    if (length > 0 && roomTime() >= length && room.pos === pos && queue[pos + 1]) {
      room = { ...room, pos: pos + 1, at: 0, stamp: room.stamp + (length - room.at) * 1000 };
      return syncRoom();
    }
    resume = Math.min(roomTime(), queue[pos].dur || Infinity); // para retomar aquí si la sala se cierra
    if (resume >= 30) count();
    if (Date.now() - stateAt > 5000) { stateAt = Date.now(); positionState(); save(); }
    // En segundo plano el flujo de eventos puede morir y nadie avisaría de un cambio hecho desde otro dispositivo:
    // se pregunta cada segundo (este código lo mueve timeupdate, que sí corre con la pantalla bloqueada).
    if (document.visibilityState !== 'visible' && Date.now() - askedAt > 1000) { askedAt = Date.now(); jam.send('state', {}, true); }
  }
  if (seeking) return;
  const d = queue[pos].dur || 0;
  const cur = d ? Math.min(roomTime(), d) : roomTime();
  for (const s of seeks) setSeek(s, d ? cur / d : 0);
  paintTimes(cur, d);
}
setInterval(paintRemote, 500);

export async function joinJam(code) {
  code = code.trim().toUpperCase();
  if (code && await jam.send('join', { code })) toast('Te uniste al jam. Todos pueden agregar canciones y cambiar la que suena.', { kind: 'ok' });
}

// playOn elige en cuál de mis dispositivos suena; con add lo suma a los que ya suenan (o lo quita si ya sonaba).
function playOn(d, add = false) {
  if (d.me && room?.playing && !STREAM && !(add && d.active)) audio.play().catch(warnPlay); // dentro del toque, para que el navegador lo deje sonar
  if (room) return void jam.send('here', { target: d.id, add });
  if (d.me) return; // sin sala ya suena aquí
  if (pos < 0) return toast('Pon a sonar algo primero y luego elige el dispositivo.');
  startRoom({ target: d.id, add });
}

function paintJam() {
  const sounding = devices.filter((d) => d.active);
  const active = sounding.length && { name: sounding.map((d) => d.name).join(' y ') };
  const shared = !!room && (room.members.length > 1 || !here);
  for (const b of document.querySelectorAll('[data-act=jam]')) b.classList.toggle('on', shared);
  $('#np-where').textContent = room && !here ? (active ? `Sonando en ${active.name}` : 'En pausa: ningún dispositivo') : room?.members.length > 1 ? `Jam con ${plural(room.members.length - 1, 'persona', 'personas')}` : 'Reproduciendo';
  if (!$('#jam').open) return;
  const title = (text) => el('h3', { className: 'section-title', textContent: text });
  const note = (text) => el('p', { className: 'muted note', textContent: text });
  const btn = (cls, glyph, text, run) => {
    const b = el('button', { className: `btn ${cls}` }, glyph && icon(glyph), text);
    b.onclick = run;
    return b;
  };
  const out = [title('Reproducir en')];
  out.push(el('ul', { className: 'list' }, devices.map((d) => {
    const on = room ? d.active : d.me;
    const b = el('button', { className: 'row' }, el('span', { className: 'type', style: `--tc: var(--t-${on ? 'audio' : 'doc'})` }, icon('cast')),
      el('div', { className: 'row-text' }, el('p', { textContent: d.name }), el('p', { className: 'muted', textContent: joinText(d.me && 'Este dispositivo', on && 'Suena aquí') })),
      on && icon('check'));
    if (on) b.setAttribute('aria-current', 'true');
    b.onclick = () => playOn(d);
    // El botón de la derecha suma o quita este dispositivo sin tocar los demás: así suenan varios a la vez.
    const several = devices.filter((x) => (room ? x.active : x.me)).length > 1;
    const also = (!on || several) && el('button', { className: 'icon-btn' }, icon(on ? 'x' : 'plus'));
    if (also) {
      also.setAttribute('aria-label', on ? `Dejar de sonar en ${d.name}` : `Sonar también en ${d.name}`);
      also.onclick = () => playOn(d, true);
    }
    return el('li', { className: 'row-wrap' }, b, also);
  })));
  out.push(note(devices.length > 1
    ? 'Toca un dispositivo para que suene solo ahí, o + para que suene también ahí, a la vez. El sonido sale por lo que tenga conectado: su parlante, audífonos o un equipo Bluetooth.'
    : 'Abre PaloBox con tu cuenta en otro dispositivo y aparecerá aquí para pasarle la música o controlarla desde este.'));
  if (audio.webkitShowPlaybackTargetPicker) { // Safari: AirPlay y salidas del sistema
    out.push(el('div', { className: 'jam-actions' }, btn('secondary small', 'volume', 'AirPlay y otras salidas', () => audio.webkitShowPlaybackTargetPicker())));
  }

  out.push(title('Jam'));
  if (room?.open) {
    const link = `${location.origin}/#/jam/${room.code}`;
    out.push(note('Comparte este código con otra persona de PaloBox. Cada quien oye en su dispositivo y todos manejan la cola.'),
      el('p', { className: 'jam-code', textContent: room.code }),
      el('div', { className: 'jam-actions' }, btn('primary small', null, navigator.share ? 'Compartir enlace' : 'Copiar enlace', async () => {
        try {
          if (navigator.share) await navigator.share({ title: 'Jam en PaloBox', url: link });
          else { await navigator.clipboard.writeText(link); toast('Enlace copiado.', { kind: 'ok' }); }
        } catch {} // se cerró el menú de compartir
      })),
      el('ul', { className: 'list' }, room.members.map((m) => el('li', {}, el('div', { className: 'row static' },
        el('span', { className: 'type', style: '--tc: var(--t-image)' }, icon('user')),
        el('div', { className: 'row-text' }, el('p', { textContent: m.name }),
          el('p', { className: 'muted', textContent: !m.online ? 'Sin conexión' : m.device ? `Oye en ${m.device}` : 'Conectado, sin sonido' })))))));
  } else {
    out.push(note('Escucha lo mismo con otras personas de PaloBox: cada quien en su dispositivo, al mismo tiempo.'),
      el('div', { className: 'jam-actions' },
        btn('primary small', 'plus', 'Iniciar jam', () => (room ? jam.send('open') : startRoom({ open: true }))),
        btn('secondary small', null, 'Unirme con un código', async () => {
          const code = await ask({ heading: 'Unirme a un jam', text: 'Escribe el código de 6 caracteres que te compartieron.', input: '', label: 'Código', ok: 'Unirme' });
          if (code) joinJam(code);
        })));
  }
  if (room && (room.open || !STREAM)) { // la sala propia del flujo continuo no se «deja»: es el reproductor
    out.push(el('div', { className: 'jam-actions' }, btn('secondary small', 'logout', room.open ? 'Salir del jam' : 'Dejar de compartir entre dispositivos', () => jam.send('leave'))));
  }
  $('#jam-body').replaceChildren(...out);
}
$('#jam-close').onclick = () => $('#jam').close();
$('#jam').onclick = (e) => e.target === e.currentTarget && e.currentTarget.close(); // clic en el telón
