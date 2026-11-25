// Galería de fotos y videos, y su visor.
import { $, el, icon, setIcon, sleep, size, clock, fmtDate, plural, lazyImg, toast, ask, empty, uploadButton } from './lib.js';
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
  $('#sel-start').hidden = !photos.length;
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
    const grid = el('div', { className: 'grid' }, items.map(([it, i]) => tile(it, i)));
    // En selección, cada mes trae su propio «Seleccionar»: marca (o desmarca, si ya están todas) el mes entero.
    const all = el('button', { className: 'sel-group', textContent: 'Seleccionar' });
    all.setAttribute('aria-label', `Seleccionar todo ${m}`);
    all.onclick = () => {
      const on = items.some(([it]) => !selected.has(it.id));
      [...grid.children].forEach((b, k) => mark(b, items[k][0], on));
      paintSel();
    };
    frag.append(el('h2', { className: 'section-title' }, m, el('span', { textContent: items.length }), all), grid);
  }
  return frag;
}

// Una miniatura: un toque la abre (o la marca, en selección múltiple); mantenerla presionada o el clic derecho abren
// el menú propio. El menú del sistema (el de Safari para imágenes) se apaga en style.css.
let swallowUntil = 0; // tras un toque largo o un barrido, el clic que le sigue no cuenta
function tile(it, i) {
  const b = el('button', { className: 'tile' }, lazyImg(`/api/photos/${it.id}/t?v=${it.v}`),
    it.kind === 'video' && el('span', { className: 'badge' }, icon('play'), 'Video'), el('span', { className: 'tile-check' }, icon('check')));
  b.dataset.id = it.id;
  b.setAttribute('aria-label', `${noun(it)} del ${fmtDate(it.taken)}`);
  let hold = null; // temporizador del toque largo
  const cancel = () => { clearTimeout(hold); hold = null; };
  const open = () => { swallowUntil = performance.now() + 600; tileMenu(b, it); };
  b.addEventListener('pointerdown', (e) => {
    if (e.pointerType === 'mouse' || document.body.classList.contains('selecting')) return; // con mouse, el clic derecho; eligiendo, el dedo barre
    const { clientX: x, clientY: y } = e;
    hold = setTimeout(open, 450);
    b.onpointermove = (m) => { if (Math.hypot(m.clientX - x, m.clientY - y) > 10) cancel(); }; // se está desplazando la galería
  });
  for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) b.addEventListener(ev, cancel);
  b.oncontextmenu = (e) => {
    e.preventDefault();
    cancel();
    if (performance.now() > swallowUntil && !document.body.classList.contains('selecting')) open();
  };
  b.onclick = () => {
    if (performance.now() < swallowUntil) return;
    if (document.body.classList.contains('selecting')) toggle(b, it);
    else openViewer(i);
  };
  return b;
}

// El menú de una foto, como el de la fototeca del teléfono: la foto se levanta sobre el fondo desenfocado y debajo
// van sus acciones, cada una con su ícono a la derecha.
function tileMenu(b, it) {
  const d = $('#pmenu');
  const row = (glyph, label, run, danger) => {
    const x = el('button', { className: `pmenu-item${danger ? ' danger' : ''}` }, el('span', { textContent: label }), icon(glyph));
    x.setAttribute('role', 'menuitem');
    x.onclick = () => { shut(); run(); };
    const li = el('li', {}, x);
    li.setAttribute('role', 'none');
    return li;
  };
  d.setAttribute('aria-label', `Acciones de ${it.name}`);
  $('#pmenu-img').src = `/api/photos/${it.id}/${it.kind === 'video' ? 't' : 'p'}?v=${it.v}`;
  $('#pmenu-cap').textContent = `${fmtDate(it.taken)} · ${size(it.size)}`;
  $('#pmenu-list').replaceChildren(
    row('check', 'Seleccionar', () => { startSelect(); toggle(b, it, true); }),
    row('download', 'Descargar original', () => el('a', { href: `/api/photos/${it.id}/o?dl`, download: it.name }).click()),
    row('trash', 'Eliminar', async () => { if (await remove([it])) render(); }, true));
  armed = false;
  d.showModal();
}
// Un toque fuera lo cierra; soltar el dedo que lo abrió no cuenta, por eso el toque tiene que empezar con el menú ya abierto.
let armed = false;
$('#pmenu').onpointerdown = () => { armed = true; };
const shut = () => { $('#pmenu').close(); swallowUntil = 0; }; // cerrado el menú, el siguiente toque ya es otro
$('#pmenu').onclick = (e) => e.target.closest('.pmenu-item, .pmenu-card') || (armed && shut());

// Selección múltiple: la barra de arriba muestra cuántas van y deja eliminarlas o cancelar.
function startSelect() { document.body.classList.add('selecting'); paintSel(); }
function endSelect() {
  selected.clear();
  document.body.classList.remove('selecting');
  swallowUntil = 0; // el clic que se ignora tras un barrido solo aplica mientras se elige
  $('#sel-del').hidden = $('#sel-cancel').hidden = true;
  for (const t of document.querySelectorAll('.tile[aria-pressed]')) t.removeAttribute('aria-pressed');
}
// mark deja una miniatura elegida o no, sin repintar la barra (quien marca muchas la repinta una vez).
function mark(b, it, on) {
  if (selected.has(it.id) === on) return;
  on ? selected.add(it.id) : selected.delete(it.id);
  b.setAttribute('aria-pressed', on);
}
function toggle(b, it, on = !selected.has(it.id)) {
  mark(b, it, on);
  paintSel();
}
function paintSel() {
  $('#sel-del').hidden = !selected.size;
  $('#sel-cancel').hidden = false;
  $('#subtitle').textContent = selected.size ? plural(selected.size, 'seleccionada', 'seleccionadas') : 'Toca o desliza el dedo sobre las fotos para elegirlas';
}
$('#sel-start').onclick = startSelect;
$('#sel-cancel').onclick = () => { endSelect(); $('#subtitle').textContent = plural(photos.length, 'elemento', 'elementos'); };
$('#sel-del').onclick = async () => { if ((await remove(photos.filter((p) => selected.has(p.id)))) !== false) render(); };

// Barrido: en selección, deslizar el dedo de lado sobre las fotos las va eligiendo (o quitando, si la primera ya lo
// estaba); sin soltar, subir o bajar toma filas enteras, y cerca del borde la galería se desplaza sola. Si el dedo
// vuelve atrás, lo que deja de estar entre el inicio y el dedo recupera el estado que tenía.
let sweep = null; // { id, tile, x, y, live, list, from, on, base }
function sweepPaint() {
  const to = sweep.list.indexOf(document.elementFromPoint(sweep.x, sweep.y)?.closest('.tile'));
  if (to < 0) return;
  const [a, b] = [Math.min(sweep.from, to), Math.max(sweep.from, to)];
  sweep.list.forEach((t, i) => {
    const it = photos[sweep.at[i]];
    mark(t, it, i >= a && i <= b ? sweep.on : sweep.base.has(it.id));
  });
  paintSel();
}
function sweepScroll() {
  if (!sweep?.live) return;
  const edge = 96;
  const v = sweep.y < edge ? sweep.y - edge : sweep.y > innerHeight - edge - 64 ? sweep.y - (innerHeight - edge - 64) : 0; // 64: la barra de pestañas
  if (v) { scrollBy(0, Math.max(-18, Math.min(18, v / 4))); sweepPaint(); }
  requestAnimationFrame(sweepScroll);
}
$('#view').addEventListener('pointerdown', (e) => {
  const t = e.target.closest?.('.tile');
  sweep = t && document.body.classList.contains('selecting') && e.isPrimary ? { id: e.pointerId, tile: t, x: e.clientX, y: e.clientY, live: false } : null;
});
$('#view').addEventListener('pointermove', (e) => {
  if (sweep?.id !== e.pointerId) return;
  if (!sweep.live) {
    const dx = e.clientX - sweep.x;
    const dy = e.clientY - sweep.y;
    if (Math.hypot(dx, dy) < 12) return; // todavía puede ser un toque
    if (Math.abs(dy) > Math.abs(dx)) return void (sweep = null); // hacia arriba o abajo de entrada: es la galería desplazándose
    const list = [...document.querySelectorAll('#view .tile')];
    const index = new Map(photos.map((p, i) => [String(p.id), i]));
    Object.assign(sweep, { live: true, list, at: list.map((t) => index.get(t.dataset.id)), from: list.indexOf(sweep.tile),
      on: sweep.tile.getAttribute('aria-pressed') !== 'true', base: new Set(selected) });
    requestAnimationFrame(sweepScroll);
  }
  sweep.x = e.clientX;
  sweep.y = e.clientY;
  sweepPaint();
});
for (const ev of ['pointerup', 'pointercancel']) $('#view').addEventListener(ev, (e) => {
  if (sweep?.id !== e.pointerId) return;
  if (sweep.live) swallowUntil = performance.now() + 400; // el clic que sigue al barrido no vuelve a cambiar la foto
  sweep = null;
});

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
  const when = new Date(it.taken);
  const hour = when.toLocaleTimeString('es-CO', { hour: 'numeric', minute: '2-digit' });
  $('#v-title').textContent = when.toLocaleDateString('es-CO', { day: 'numeric', month: 'long', year: 'numeric' });
  $('#v-sub').textContent = hour;
  $('#v-count').textContent = `${vIndex + 1} de ${photos.length}`;
  $('#v-name').textContent = it.name;
  $('#v-meta').textContent = `${noun(it)} ${it.name.includes('.') ? it.name.split('.').pop().toUpperCase() : ''} · ${size(it.size)}`;
  $('#v-when').textContent = `${when.toLocaleDateString('es-CO', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })}, ${hour}`;
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
// La información del archivo se abre y se cierra con su botón, y se queda así al pasar de una foto a otra.
$('#v-info').onclick = () => {
  const open = $('#v-details').hidden;
  $('#v-details').hidden = !open;
  $('#v-info').setAttribute('aria-expanded', open);
};
// Gestos con el dedo, iguales en fotos y videos (los controles van fuera del escenario): la foto acompaña al dedo.
// A los lados pasa a la vecina; hacia abajo se encoge, deja ver la galería y al soltar cierra.
let drag = null; // { x, y, axis: '' | 'x' | 'y' | 'n' }
let moved = false;
const shown = () => $('#stage').firstElementChild;
const edge = (dx) => (dx > 0 && vIndex === 0) || (dx < 0 && vIndex === photos.length - 1);
$('#stage').addEventListener('touchstart', (e) => {
  drag = e.touches.length === 1 && !(visualViewport?.scale > 1) ? { x: e.touches[0].clientX, y: e.touches[0].clientY, axis: '' } : null; // con zoom, el dedo mueve la foto
  moved = false;
}, { passive: true });
$('#stage').addEventListener('touchmove', (e) => {
  const m = shown();
  if (!drag || !m) return;
  const dx = e.touches[0].clientX - drag.x;
  const dy = e.touches[0].clientY - drag.y;
  if (!drag.axis) {
    if (Math.hypot(dx, dy) < 10) return; // todavía puede ser un toque
    drag.axis = Math.abs(dx) > Math.abs(dy) ? 'x' : dy > 0 ? 'y' : 'n';
    moved = true;
    m.style.animation = m.style.transition = 'none';
  }
  if (drag.axis === 'x') m.style.transform = `translateX(${edge(dx) ? dx / 3 : dx}px)`; // en los extremos se resiste
  else if (drag.axis === 'y') {
    m.style.transform = `translate(${dx / 3}px, ${dy}px) scale(${1 - Math.min(dy / 600, 0.4)})`;
    view.style.setProperty('--fade', 1 - Math.min(dy / 300, 1));
    view.classList.add('dragging');
  }
}, { passive: true });
async function dragEnd(e) {
  const m = shown();
  if (!drag || !m) return;
  const { axis, x, y } = drag;
  drag = null;
  const dx = e.changedTouches[0].clientX - x;
  const dy = e.changedTouches[0].clientY - y;
  const settle = () => { view.style.removeProperty('--fade'); view.classList.remove('dragging'); };
  if (axis === 'y' && dy > 110 && e.type === 'touchend') { view.close(); return settle(); }
  if (axis === 'x' && Math.abs(dx) > 60 && !edge(dx) && e.type === 'touchend') {
    m.style.transition = 'transform 140ms ease-in, opacity 140ms';
    m.style.transform = `translateX(${Math.sign(dx) * 100}%)`;
    m.style.opacity = 0;
    await sleep(140);
    return step(dx < 0 ? 1 : -1);
  }
  m.style.transition = 'transform var(--med) var(--ease)'; // no alcanzó: vuelve a su sitio
  m.style.transform = '';
  settle();
}
$('#stage').addEventListener('touchend', dragEnd);
$('#stage').addEventListener('touchcancel', dragEnd);
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
