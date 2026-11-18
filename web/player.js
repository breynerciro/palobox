// Reproductor de música: cola, aleatorio, repetir y la barra que sigue sonando al cambiar de sección.
import { $, el, icon, setIcon, json, joinText, lazyImg, toast, clock, plural, sleep } from './lib.js';
import { rawUrl, thumbUrl } from './nav.js';
import { api } from './session.js';
import { ensureLib, markLikes, toggleLike } from './music.js';

export const audio = new Audio();
audio.preload = 'metadata';
audio.hidden = true;
// Dentro de la página y no suelto: así lo tenía la versión que sí mostraba la reproducción en la
// Dynamic Island y la pantalla bloqueada del iPhone.
document.body.append(audio);
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
  audio.src = rawUrl('musica', t.path);
  if (at) audio.addEventListener('loadedmetadata', () => (audio.currentTime = at), { once: true });
  if (play) audio.play().catch(() => {}); // si el formato no sirve, lo informa onerror
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
  markLikes();
  syncPlayButton();
  paintQueue();
  save();
  refill();
}

// Sugerencias: cuando a la cola le quedan menos de 3 pistas, el servidor propone qué sigue y la música no se detiene.
async function refill() {
  if (filling || repeat || pos < 0 || queue.length - pos > 3) return;
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
}

function addAt(t, i, text) {
  if (pos < 0) return playAt([t], 0);
  queue.splice(i, 0, { ...t });
  manual++;
  toast(`«${t.title}» ${text}.`, { kind: 'ok' });
  paintQueue();
  save();
}
export const playNext = (t) => addAt(t, pos + 1, 'suena a continuación');
export const enqueue = (t) => addAt(t, pos + 1 + manual, 'se agregó a la cola');

function next(ended = false) {
  if (ended && repeat === 2) return load(pos);
  let n = pos + 1;
  if (n >= queue.length) {
    if (!repeat) return; // se acabó la cola
    n = 0;
  }
  manual = Math.max(0, manual - 1);
  load(n);
}
function prev() {
  if (audio.currentTime > 3 || pos === 0) return (audio.currentTime = 0);
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
  if (act === 'shuffle') setShuffle(!shuffle);
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
    r.querySelector('.num')?.replaceChildren(on ? icon(audio.paused ? 'pause' : 'play') : String([...r.closest('ul').children].indexOf(r.parentElement) + 1));
  }
  syncToggles();
}

function syncPlayButton() {
  for (const b of [$('#playpause'), $('#np-play')]) {
    setIcon(b, audio.paused ? 'play' : 'pause');
    b.setAttribute('aria-label', audio.paused ? 'Reproducir' : 'Pausar');
  }
  if ('mediaSession' in navigator) navigator.mediaSession.playbackState = audio.paused ? 'paused' : 'playing';
  markPlaying();
}
audio.onplay = syncPlayButton;
audio.onpause = () => { syncPlayButton(); save(); };
audio.onended = () => { count(); next(true); };
audio.onerror = () => queue[pos] && toast(`Este dispositivo no puede reproducir «${queue[pos].title}». Los formatos más compatibles son MP3 y M4A.`, { kind: 'warn', sticky: true });

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
  if (!audio.duration) return;
  if (!audio.paused && audio.currentTime >= 30) count();
  if (Date.now() - savedAt > 5000) save();
  if (seeking) return;
  for (const s of seeks) setSeek(s, audio.currentTime / audio.duration);
  paintTimes(audio.currentTime, audio.duration);
};
for (const s of seeks) {
  s.oninput = () => {
    seeking = true;
    s.style.setProperty('--p', `${s.value / 10}%`);
    if (audio.duration) $('#np-cur').textContent = $('#cur').textContent = clock((s.value / 1000) * audio.duration);
  };
  s.onchange = () => {
    if (audio.duration) audio.currentTime = (s.value / 1000) * audio.duration;
    seeking = false;
  };
}
function setVolume(v) {
  audio.volume = Math.min(1, Math.max(0, v));
  $('#volume').value = Math.round(audio.volume * 100);
  $('#volume').style.setProperty('--p', `${audio.volume * 100}%`);
}
$('#volume').oninput = (e) => { setVolume(e.target.value / 100); save(); };

const toggle = () => (audio.paused ? audio.play().catch(() => {}) : audio.pause());
$('#playpause').onclick = $('#np-play').onclick = toggle;
$('#next').onclick = $('#np-next').onclick = () => next();
$('#prev').onclick = $('#np-prev').onclick = prev;
$('#player-open').onclick = () => $('#np').showModal();
$('#np-close').onclick = () => $('#np').close();
$('#queue-close').onclick = () => $('#queue').close();
$('#queue').onclick = (e) => e.target === e.currentTarget && e.currentTarget.close(); // clic en el telón
if ('mediaSession' in navigator) {
  const ms = navigator.mediaSession;
  ms.setActionHandler('play', () => audio.play());
  ms.setActionHandler('pause', () => audio.pause());
  ms.setActionHandler('nexttrack', () => next());
  ms.setActionHandler('previoustrack', prev);
  try { ms.setActionHandler('seekto', (d) => (audio.currentTime = d.seekTime)); } catch {}
}

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
  else if (e.key === 'ArrowRight') e.shiftKey ? next() : (audio.currentTime += 5);
  else if (e.key === 'ArrowLeft') e.shiftKey ? prev() : (audio.currentTime -= 5);
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
  b.onclick = () => { manual = Math.max(0, manual - (i - pos)); load(i); };
  const rm = el('button', { className: 'icon-btn' }, icon('x'));
  rm.setAttribute('aria-label', `Quitar ${t.title} de la cola`);
  rm.onclick = () => {
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
  if (pos < 0 || !user) return;
  const from = Math.max(0, pos - 50);
  try {
    localStorage.setItem('palobox.player', JSON.stringify({
      user, queue: queue.slice(from, from + KEEP), pos: pos - from, at: audio.currentTime || resume, shuffle, repeat, volume: audio.volume,
    }));
  } catch {} // modo privado o cupo lleno: la app sigue sin recordar
}
window.addEventListener('pagehide', save);

// restore deja lista (en pausa) la cola que este usuario tenía la última vez.
export function restore(name) {
  if (user === name) return;
  user = name;
  let s;
  try { s = JSON.parse(localStorage.getItem('palobox.player')); } catch {}
  if (!s || s.user !== name || !Array.isArray(s.queue) || !s.queue[s.pos]?.path) return;
  setVolume(s.volume ?? 1);
  queue = s.queue;
  order = queue.slice();
  shuffle = !!s.shuffle;
  repeat = [0, 1, 2].includes(s.repeat) ? s.repeat : 0;
  load(s.pos, { play: false, at: s.at });
  ensureLib().catch(() => {}); // para pintar el me gusta de la pista restaurada
}

// reset vacía el reproductor al salir: la cola de un usuario no le queda al siguiente.
export function reset() {
  audio.pause();
  audio.removeAttribute('src');
  queue = order = [];
  pos = -1;
  epoch++;
  user = '';
  for (const d of [$('#np'), $('#queue')]) d.close();
  $('#player').hidden = true;
  document.body.classList.remove('has-player', 'player-tucked');
}
