// Galería de fotos y videos, y su visor.
import { $, el, icon, setIcon, size, clock, fmtDate, plural, lazyImg, toast, ask, menu, empty, uploadButton } from './lib.js';
import { render } from './nav.js';
import { api } from './session.js';

let photos = [];
const selected = new Set(); // ids elegidos en la selección múltiple; vacío y sin body.selecting = modo normal
const noun = (it) => (it.kind === 'video' ? 'Video' : 'Foto');

export async function photosView() {
  const r = await api('/api/photos');
  if (!r.ok) throw new Error('Intenta de nuevo en un momento.');
  photos = await r.json();
  endSelect(); // cada pintada empieza sin selección (app.js ya ocultó sus botones)
  $('#subtitle').textContent = photos.length ? plural(photos.length, 'elemento', 'elementos') : '';
  if (!photos.length) {
    return empty('image', 'Aún no hay fotos', 'Sube fotos y videos de tu galería; se guardan con su calidad original.', uploadButton());
  }
  const frag = document.createDocumentFragment();
  const groups = new Map();
  photos.forEach((it, i) => {
    const d = new Date(it.taken).toLocaleDateString('es-CO', { month: 'long', year: 'numeric' });
    const m = d[0].toUpperCase() + d.slice(1); // "Septiembre de 2026", no "Septiembre De 2026"
    if (!groups.has(m)) groups.set(m, []);
    groups.get(m).push([it, i]);
  });
  for (const [m, items] of groups) {
    frag.append(el('h2', { className: 'section-title' }, m, el('span', { textContent: items.length })));
    frag.append(el('div', { className: 'grid' }, items.map(([it, i]) => tile(it, i))));
  }
  return frag;
}

// Una miniatura: un toque la abre (o la marca, en selección múltiple); mantenerla presionada o el clic derecho abren
// el menú propio. El menú del sistema (el de Safari para imágenes) se apaga en style.css.
function tile(it, i) {
  const b = el('button', { className: 'tile' }, lazyImg(`/api/photos/${it.id}/t?v=${it.v}`),
    it.kind === 'video' && el('span', { className: 'badge' }, icon('play'), 'Video'), el('span', { className: 'tile-check' }, icon('check')));
  b.dataset.id = it.id;
  b.setAttribute('aria-label', `${noun(it)} del ${fmtDate(it.taken)}`);
  let hold = null; // temporizador del toque largo
  let held = false; // el toque largo ya abrió el menú: el clic que sigue no cuenta
  const cancel = () => { clearTimeout(hold); hold = null; };
  b.addEventListener('pointerdown', (e) => {
    held = false;
    if (e.pointerType === 'mouse') return; // con mouse, el clic derecho
    const { clientX: x, clientY: y } = e;
    hold = setTimeout(() => { held = true; tileMenu(b, it); }, 450);
    b.onpointermove = (m) => { if (Math.hypot(m.clientX - x, m.clientY - y) > 10) cancel(); }; // se está desplazando la galería
  });
  for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) b.addEventListener(ev, cancel);
  b.oncontextmenu = (e) => { e.preventDefault(); cancel(); if (!held) tileMenu(b, it); };
  b.onclick = () => {
    if (held) return void (held = false);
    if (document.body.classList.contains('selecting')) toggle(b, it);
    else openViewer(i);
  };
  return b;
}

function tileMenu(b, it) {
  menu(b, `Acciones de ${it.name}`, [
    { icon: 'check', label: 'Seleccionar', run: () => { startSelect(); toggle(b, it, true); } },
    { icon: 'download', label: 'Descargar original', run: () => el('a', { href: `/api/photos/${it.id}/o?dl`, download: it.name }).click() },
    { icon: 'trash', label: 'Eliminar', danger: true, run: async () => { if (await remove([it])) render(); } },
  ]);
}

// Selección múltiple: la barra de arriba muestra cuántas van y deja eliminarlas o cancelar.
function startSelect() { document.body.classList.add('selecting'); paintSel(); }
function endSelect() {
  selected.clear();
  document.body.classList.remove('selecting');
  $('#sel-del').hidden = $('#sel-cancel').hidden = true;
  for (const t of document.querySelectorAll('.tile[aria-pressed]')) t.removeAttribute('aria-pressed');
}
function toggle(b, it, on = !selected.has(it.id)) {
  on ? selected.add(it.id) : selected.delete(it.id);
  b.setAttribute('aria-pressed', on);
  paintSel();
}
function paintSel() {
  $('#sel-del').hidden = !selected.size;
  $('#sel-cancel').hidden = false;
  $('#subtitle').textContent = selected.size ? plural(selected.size, 'seleccionada', 'seleccionadas') : 'Toca las fotos y videos que quieras elegir';
}
$('#sel-cancel').onclick = () => { endSelect(); $('#subtitle').textContent = plural(photos.length, 'elemento', 'elementos'); };
$('#sel-del').onclick = async () => { if ((await remove(photos.filter((p) => selected.has(p.id)))) !== false) render(); };

// remove elimina del servidor, con una sola confirmación, una foto o todas las elegidas.
async function remove(items) {
  if (!items.length) return;
  const one = items.length === 1;
  if (!(await ask({ heading: one ? `¿Eliminar ${items[0].kind === 'video' ? 'este video' : 'esta foto'} del servidor?` : `¿Eliminar ${items.length} elementos del servidor?`,
    text: 'No se borra de tu celular.', ok: 'Eliminar', danger: true }))) return false;
  let bad = 0;
  for (const it of items) {
    const r = await api(`/api/photos/${it.id}`, { method: 'DELETE' }).catch(() => null);
    if (!r?.ok) bad++;
  }
  if (bad) toast(one ? 'No se pudo eliminar.' : `${plural(bad, 'elemento no se pudo', 'elementos no se pudieron')} eliminar.`, { kind: 'warn' });
  else if (!one) toast(`${plural(items.length, 'elemento eliminado', 'elementos eliminados')}.`, { kind: 'ok' });
  return bad < items.length; // false: se canceló o no se borró ninguno
}

// Visor. Deslizar a los lados cambia de foto o de video y hacia abajo lo cierra; un toque sobre la foto esconde o
// muestra las barras. Los videos tienen controles propios (no los del navegador, que tapaban la tira y cambian de
// un sistema a otro): botón central, barra de avance, sonido y pantalla completa. Abajo, la tira de miniaturas.

let vIndex = 0;
let vid = null; // el <video> a la vista, o null si es una foto
let hideT; // esconde las barras a los pocos segundos de estar reproduciendo
const STRIP = 15; // miniaturas a cada lado de la actual: con miles de fotos la tira no las crea todas
const view = $('#viewer');
function openViewer(i) {
  vIndex = i;
  showItem(0);
  view.showModal();
}
// wake muestra las barras; si el video está sonando, las vuelve a esconder solas.
function wake() {
  clearTimeout(hideT);
  view.classList.remove('bare');
  if (vid && !vid.paused) hideT = setTimeout(() => view.classList.add('bare'), 3000);
}
function paintVideo() {
  if (!vid) return;
  const d = vid.duration || 0;
  const p = d ? vid.currentTime / d : 0;
  $('#v-cur').textContent = clock(vid.currentTime);
  $('#v-dur').textContent = clock(d);
  if (!$('#v-seek').matches(':active')) $('#v-seek').value = Math.round(p * 1000); // mientras se arrastra manda el dedo
  $('#v-seek').style.setProperty('--p', `${p * 100}%`);
  setIcon($('#v-play'), vid.paused ? 'play' : 'pause');
  $('#v-play').setAttribute('aria-label', vid.paused ? 'Reproducir' : 'Pausar');
  view.classList.toggle('playing', !vid.paused);
  setIcon($('#v-mute'), vid.muted ? 'mute' : 'volume');
  $('#v-mute').setAttribute('aria-pressed', vid.muted);
}
function showItem(dir = 0) {
  const it = photos[vIndex];
  const o = `/api/photos/${it.id}/o`;
  const video = it.kind === 'video';
  const media = video
    ? el('video', { src: o, playsInline: true, preload: 'metadata', poster: `/api/photos/${it.id}/t?v=${it.v}` })
    : el('img', { src: `/api/photos/${it.id}/p?v=${it.v}`, alt: it.name, draggable: false });
  media.style.setProperty('--dx', `${dir * 32}px`); // entra desde el lado hacia el que se avanzó
  $('#stage').replaceChildren(media);
  vid = video ? media : null;
  clearTimeout(hideT);
  view.classList.remove('bare', 'playing', 'v-error');
  view.classList.toggle('is-video', video); // el escenario deja libre la franja de los controles
  $('#v-ctl').hidden = $('#v-play').hidden = !video;
  if (video) {
    for (const ev of ['timeupdate', 'durationchange', 'play', 'pause', 'volumechange', 'ended']) media.addEventListener(ev, paintVideo);
    media.addEventListener('play', wake);
    media.addEventListener('pause', wake);
    // Un .mov del iPhone (HEVC) no siempre se puede ver en otro navegador: se dice, y descargar sigue arriba.
    media.addEventListener('error', () => { view.classList.add('v-error'); $('#v-play').hidden = true; });
    paintVideo();
  }
  $('#v-title').textContent = new Date(it.taken).toLocaleDateString('es-CO', { day: 'numeric', month: 'long', year: 'numeric' });
  $('#v-sub').textContent = `${vIndex + 1} de ${photos.length} · ${size(it.size)}`;
  $('#v-dl').href = `${o}?dl`;
  $('#v-dl').download = it.name;
  $('#v-prev').hidden = vIndex === 0;
  $('#v-next').hidden = vIndex === photos.length - 1;
  const from = Math.max(0, vIndex - STRIP);
  $('#v-strip').replaceChildren(...photos.slice(from, vIndex + STRIP + 1).map((p, k) => {
    const i = from + k;
    const b = el('button', { className: 'v-thumb' }, lazyImg(`/api/photos/${p.id}/t?v=${p.v}`), p.kind === 'video' && icon('play'));
    b.setAttribute('aria-label', `${noun(p)} ${i + 1} de ${photos.length}`);
    if (i === vIndex) b.setAttribute('aria-current', 'true');
    b.onclick = () => { const d = Math.sign(i - vIndex); vIndex = i; showItem(d); };
    return b;
  }));
  $('#v-strip [aria-current]')?.scrollIntoView({ inline: 'center', block: 'nearest' });
  // Las vecinas se piden de una vez: al deslizar ya están.
  for (const n of [photos[vIndex + 1], photos[vIndex - 1]]) if (n && n.kind !== 'video') new Image().src = `/api/photos/${n.id}/p?v=${n.v}`;
}
function step(d) {
  const n = vIndex + d;
  if (n < 0 || n >= photos.length) return;
  vIndex = n;
  showItem(d);
}
const playPause = () => vid && (vid.paused ? vid.play().catch(() => {}) : vid.pause());
$('#v-play').onclick = playPause;
$('#v-seek').oninput = (e) => {
  if (!vid?.duration) return;
  vid.currentTime = (e.target.value / 1000) * vid.duration;
  paintVideo();
  wake();
};
$('#v-mute').onclick = () => { if (vid) vid.muted = !vid.muted; wake(); };
// Pantalla completa: en el iPhone solo el propio video puede pedirla (webkitEnterFullscreen).
$('#v-full').onclick = () => vid && (vid.webkitEnterFullscreen ? vid.webkitEnterFullscreen() : vid.requestFullscreen?.());
$('#v-prev').onclick = () => step(-1);
$('#v-next').onclick = () => step(1);
$('#v-close').onclick = () => view.close();
view.onclose = () => { clearTimeout(hideT); vid = null; $('#stage').replaceChildren(); }; // detiene el video
view.addEventListener('keydown', (e) => {
  if (e.key === 'ArrowLeft') step(-1);
  if (e.key === 'ArrowRight') step(1);
  if (e.key === ' ' && vid && e.target === view) { e.preventDefault(); playPause(); }
});
// Deslizar entre elementos, o hacia abajo para cerrar: igual en fotos y en videos (los controles van fuera del escenario).
let touchX = null;
let touchY = 0;
let moved = false;
$('#stage').addEventListener('touchstart', (e) => {
  touchX = e.touches.length === 1 ? e.touches[0].clientX : null;
  touchY = e.touches[0].clientY;
  moved = false;
}, { passive: true });
$('#stage').addEventListener('touchend', (e) => {
  if (touchX == null) return;
  const dx = e.changedTouches[0].clientX - touchX;
  const dy = e.changedTouches[0].clientY - touchY;
  touchX = null;
  if (visualViewport?.scale > 1) return; // con zoom, el dedo mueve la foto
  moved = Math.hypot(dx, dy) > 10;
  if (dy > 90 && dy > Math.abs(dx) * 1.5) view.close();
  else if (Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy)) step(dx < 0 ? 1 : -1);
});
// Un toque (no un arrastre): en una foto esconde o muestra las barras; en un video, si están escondidas las trae y
// si están a la vista reproduce o pausa.
$('#stage').onclick = () => {
  if (moved) return void (moved = false);
  if (!vid) return void view.classList.toggle('bare');
  if (view.classList.contains('bare')) wake(); else playPause();
};
$('#v-del').onclick = async () => {
  const it = photos[vIndex];
  if (!(await remove([it])) || photos[vIndex] !== it) return;
  photos.splice(vIndex, 1);
  if (!photos.length) view.close();
  else { vIndex = Math.min(vIndex, photos.length - 1); showItem(0); }
  render();
};
