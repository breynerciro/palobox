// Cola: la pista que suena, lo que el usuario puso en cola («En la cola») y lo que sigue de la lista o las
// sugerencias. Cada fila se arrastra por su agarre; Editar deja elegir varias para subirlas o quitarlas.
import { $, el, icon, joinText, toast, plural, sheetDrag } from './lib.js';
import { state, cover, playing, toggle, jumpTo, setTail, REPEAT } from './player.js';

const QUEUE_MAX = 100; // filas pintadas; una cola de miles de pistas no cabe en una lista cómoda
let editing = false;
const picked = new Set(); // en edición: las pistas elegidas (los objetos de la cola)
// moveInQueue pasa la pista i de lo que sigue al lugar j (ya sin ella). Soltarla dentro de «En la cola» o justo
// al final la deja ahí; sacarla de ahí la devuelve a lo que sigue.
function moveInQueue(i, j) {
  const { queue, pos, manual } = state();
  const rest = queue.slice(pos + 1);
  if (i === j || !rest[i]) return;
  let m = manual - (i < manual ? 1 : 0);
  const [t] = rest.splice(i, 1);
  rest.splice(j, 0, t);
  if (j <= m) m++;
  setTail(rest, m);
}
function queueRow(t, i) {
  const { pos } = state();
  const text = el('div', { className: 'row-text' }, el('p', { className: 'ellipsis', textContent: t.title }), el('p', { className: 'muted ellipsis', textContent: joinText(t.artist, t.auto && 'Sugerida') }));
  const art = el('span', { className: 'cover small', style: '--tc: var(--t-audio)' }, cover(t));
  if (i === pos) { // la que suena: se pausa y se reanuda desde aquí
    const b = el('button', { className: 'row playing' }, art, text);
    b.setAttribute('aria-current', 'true');
    b.onclick = toggle;
    const pp = el('button', { className: 'icon-btn queue-play' }, icon(playing() ? 'pause' : 'play'));
    pp.setAttribute('aria-label', playing() ? 'Pausar' : 'Reproducir');
    pp.onclick = toggle;
    return el('li', { className: 'row-wrap' }, b, pp);
  }
  const k = i - pos - 1; // lugar dentro de lo que sigue
  if (editing) {
    const b = el('button', { className: 'row' }, el('span', { className: 'check' }, icon('check')), art, text);
    b.setAttribute('role', 'checkbox');
    b.setAttribute('aria-checked', picked.has(t));
    b.onclick = () => { picked.has(t) ? picked.delete(t) : picked.add(t); paintQueue(); };
    return el('li', { className: 'row-wrap' }, b);
  }
  const b = el('button', { className: 'row' }, art, text);
  b.setAttribute('aria-label', `Saltar a ${t.title}`);
  b.onclick = () => jumpTo(i);
  const grip = el('button', { className: 'icon-btn grip' }, icon('list'));
  grip.setAttribute('aria-label', `Mover ${t.title}. Con el teclado: flechas arriba y abajo`);
  grip.onkeydown = (e) => {
    if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
    e.preventDefault();
    const { queue, pos } = state();
    moveInQueue(k, Math.max(0, Math.min(queue.length - pos - 2, k + (e.key === 'ArrowUp' ? -1 : 1))));
  };
  const li = el('li', { className: 'row-wrap' }, b, grip);
  li.dataset.k = k;
  grip.onpointerdown = (e) => dragRow(e, li, k);
  return li;
}
// dragRow arrastra una fila por su agarre: la fila sigue al dedo y una línea marca dónde quedaría.
function dragRow(e, li, k) {
  if (!e.isPrimary || e.button > 0) return;
  e.preventDefault();
  const grip = e.currentTarget;
  const rows = [...li.parentElement.querySelectorAll('li[data-k]')];
  const y0 = e.clientY;
  let to = k;
  grip.setPointerCapture(e.pointerId);
  li.classList.add('dragging');
  rowDrag = true;
  const move = (ev) => {
    li.style.transform = `translateY(${ev.clientY - y0}px)`;
    // El lugar: cuántas de las otras filas quedan por encima del dedo.
    to = rows.filter((x) => x !== li && x.getBoundingClientRect().top + x.offsetHeight / 2 < ev.clientY).length;
    for (const x of rows) x.classList.remove('drop-above', 'drop-below');
    const others = rows.filter((x) => x !== li);
    if (others[to]) others[to].classList.add('drop-above'); else others.at(-1)?.classList.add('drop-below');
  };
  const end = (ev) => {
    grip.removeEventListener('pointermove', move);
    grip.removeEventListener('pointerup', end);
    grip.removeEventListener('pointercancel', end);
    for (const x of rows) x.classList.remove('drop-above', 'drop-below');
    li.classList.remove('dragging');
    li.style.transform = '';
    rowDrag = false;
    if (ev.type === 'pointerup' && to !== k) moveInQueue(k, to);
    else paintQueue(); // lo que llegó durante el arrastre
  };
  grip.addEventListener('pointermove', move);
  grip.addEventListener('pointerup', end);
  grip.addEventListener('pointercancel', end);
}
let painted = ''; // lo que muestran las filas de la cola (paintQueue)
let rowDrag = false; // una fila va en el dedo: la cola no se repinta
export function paintQueue() {
  if (!$('#queue').open) return;
  const { queue, pos, manual, repeat } = state();
  const body = $('#queue-body');
  const rest = queue.slice(pos + 1);
  for (const t of [...picked]) if (!rest.includes(t)) picked.delete(t); // la cola cambió
  if (!rest.length) editing = false;
  $('#queue-clear').hidden = editing || !rest.length;
  $('#queue-edit').hidden = !rest.length;
  $('#queue-edit').textContent = editing ? 'Listo' : 'Editar';
  $('#queue-edit').setAttribute('aria-pressed', editing);
  paintQueueFoot(rest);
  if (pos < 0) return (painted = '', body.replaceChildren(el('p', { className: 'muted note', textContent: 'No hay nada en la cola.' })));
  const shown = rest.slice(0, QUEUE_MAX);
  const more = rest.length - shown.length;
  const m = Math.min(manual, shown.length);
  // Los avisos de la sala llegan a menudo y casi siempre sin cambios en la cola: rehacer las filas haría
  // parpadear las carátulas y soltaría la fila que se está arrastrando.
  const key = JSON.stringify([pos, queue[pos].path, playing(), editing, m, more, repeat, shown.map((t) => [t.path, t.auto, picked.has(t)])]);
  if (rowDrag || (key === painted && body.isConnected && body.childElementCount)) return;
  painted = key;
  const sep = (text) => el('li', { className: 'queue-sep', textContent: text });
  const rows = shown.map((t, k) => queueRow(t, pos + 1 + k));
  if (m > 0) rows.splice(m, 0, ...(m < shown.length ? [sep('A continuación')] : []));
  body.replaceChildren(...[
    el('h3', { className: 'section-title', textContent: 'Sonando' }),
    el('ul', { className: 'list' }, queueRow(queue[pos], pos)),
    el('h3', { className: 'section-title', textContent: m > 0 ? 'En la cola' : 'A continuación' }),
    shown.length
      ? el('ul', { className: 'list' }, rows)
      : el('p', { className: 'muted note', textContent: repeat === 1 ? 'La cola vuelve a empezar al terminar esta canción.' : 'Esta es la última canción de la cola.' }),
    more > 0 && el('p', { className: 'muted note', textContent: `Y ${plural(more, 'canción más', 'canciones más')}.` }),
  ].filter(Boolean));
}
// Abajo: aleatorio y repetir; en edición, qué hacer con las elegidas.
function paintQueueFoot(rest) {
  const { shuffle, repeat } = state();
  const btn = (glyph, text, attrs = {}) => {
    const b = el('button', { className: 'queue-btn' }, icon(glyph), el('span', { textContent: text }));
    for (const [k, v] of Object.entries(attrs)) b.setAttribute(k, v);
    return b;
  };
  if (!editing) {
    $('#queue-foot').replaceChildren(btn('shuffle', 'Aleatorio', { 'data-act': 'shuffle', 'aria-pressed': shuffle }),
      btn(repeat === 2 ? 'repeat-1' : 'repeat', REPEAT[repeat], { 'data-act': 'repeat', 'aria-pressed': repeat > 0 }));
    return;
  }
  const sel = rest.filter((t) => picked.has(t));
  const others = rest.filter((t) => !picked.has(t));
  const inQueue = (list) => list.filter((t) => rest.indexOf(t) < state().manual).length;
  const top = btn('up', 'Mover al inicio');
  const rm = btn('trash', sel.length ? `Quitar (${sel.length})` : 'Quitar');
  top.disabled = rm.disabled = !sel.length;
  const done = () => { picked.clear(); editing = false; };
  top.onclick = () => { done(); setTail([...sel, ...others], sel.length + inQueue(others)); };
  rm.onclick = () => { done(); setTail(others, inQueue(others)); };
  $('#queue-foot').replaceChildren(top, rm);
}
export function openQueue() { $('#queue').showModal(); paintQueue(); }
$('#queue-close').onclick = () => $('#queue').close();
$('#queue').onclick = (e) => e.target === e.currentTarget && e.currentTarget.close(); // clic en el telón
$('#queue-edit').onclick = () => { editing = !editing; picked.clear(); paintQueue(); };
$('#queue-clear').onclick = () => { setTail([], 0); toast('Se quitó lo que seguía en la cola.', { kind: 'ok' }); };
$('#queue').addEventListener('close', () => { editing = false; picked.clear(); $('#queue').classList.remove('full'); });
// En celular la cola es una hoja a media pantalla que se arrastra desde cualquier parte: hacia arriba se abre completa
// y hacia abajo se encoge o se cierra.
sheetDrag($('#queue'), $('#queue-body'), { grow: true });
