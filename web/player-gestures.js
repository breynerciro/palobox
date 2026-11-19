// Gestos de la barra mini: deslizar a los lados cambia de canción; hacia arriba abre la pantalla completa;
// hacia abajo la oculta (solo en la Nube: en Música el reproductor es el centro de la sección).
// También los de la pantalla completa; lo que cambian de la reproducción lo piden a player.js.
import { $, sleep } from './lib.js';
import { canGo, next, swipePrev } from './player.js';
import { openQueue } from './player-queue.js';

const bar = $('#player');
const strip = bar.querySelector('.player-text'); // solo el título acompaña al dedo; carátula y botones se quedan
const reduce = matchMedia('(prefers-reduced-motion: reduce)');
let drag = null; // { id, x, y, t, mode: '' | 'x' | 'y' | 'up' }
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
async function swipeTo(dir, n = strip) {
  const ms = reduce.matches ? 0 : 160;
  slideTo(n, `translateX(${-dir * bar.offsetWidth}px)`, 0, ms);
  await sleep(ms);
  if (dir > 0) next();
  else swipePrev();
  slideTo(n, `translateX(${dir * 48}px)`, 0);
  n.offsetWidth; // aplica la posición de entrada antes de animar
  slideTo(n, '', '', ms);
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
    drag.mode = Math.abs(dx) > Math.abs(dy) * 1.5 ? 'x' : dy < 0 ? 'up' : document.body.classList.contains('music') ? '' : 'y';
    if (!drag.mode) return (drag = null);
    bar.setPointerCapture(e.pointerId);
  }
  if (drag.mode === 'x') slideTo(strip, `translateX(${canGo(-Math.sign(dx)) ? dx : dx / 4}px)`, 1 - Math.min(Math.abs(dx) / bar.offsetWidth, 0.6));
  else if (drag.mode === 'up') slideTo(bar, `translateY(${Math.max(-24, Math.min(0, dy / 3))}px)`, '');
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
  if (mode === 'up' && up && dy < -30) $('#np').showModal();
  slideTo(mode === 'x' ? strip : bar, '', '', 200);
}
bar.addEventListener('pointerup', dragEnd);
bar.addEventListener('pointercancel', dragEnd); // el navegador tomó el gesto (desplazamiento vertical en Música)
// Pantalla completa, desde cualquier parte: hacia abajo la cierra y vuelve a la barra pequeña; a los lados cambia de
// canción (la carátula acompaña al dedo); hacia arriba abre la cola.
const np = $('#np');
const npArt = $('#np-cover');
let npDrag = null; // { id, x, y, t, mode: '' | 'x' | 'y' | 'up' }
np.addEventListener('pointerdown', (e) => {
  if (e.isPrimary && e.pointerType !== 'mouse' && !e.target.closest('input')) npDrag = { id: e.pointerId, x: e.clientX, y: e.clientY, t: e.timeStamp, mode: '' };
});
np.addEventListener('pointermove', (e) => {
  if (npDrag?.id !== e.pointerId) return;
  const dx = e.clientX - npDrag.x;
  const dy = e.clientY - npDrag.y;
  if (!npDrag.mode) {
    if (Math.hypot(dx, dy) < 12) return; // todavía puede ser un toque
    npDrag.mode = Math.abs(dx) > Math.abs(dy) * 1.5 ? 'x' : dy > 0 ? 'y' : 'up';
    np.setPointerCapture(e.pointerId);
  }
  if (npDrag.mode === 'x') slideTo(npArt, `translateX(${canGo(-Math.sign(dx)) ? dx : dx / 4}px)`, 1 - Math.min(Math.abs(dx) / np.offsetWidth, 0.6));
  else if (npDrag.mode === 'y') slideTo(np, `translateY(${Math.max(0, dy)}px)`, '');
});
async function npEnd(e) {
  if (npDrag?.id !== e.pointerId) return;
  const { mode, x, y, t } = npDrag;
  npDrag = null;
  if (!mode) return;
  swallowUntil = e.timeStamp + 400; // el botón donde empezó el gesto no se activa
  const dx = e.clientX - x;
  const dy = e.clientY - y;
  const up = e.type === 'pointerup';
  const far = (d, size) => d > size / 4 || (d > 40 && d / (e.timeStamp - t) > 0.5); // un cuarto del tamaño, o un gesto rápido corto
  if (mode === 'x' && up && canGo(-Math.sign(dx)) && far(Math.abs(dx), np.offsetWidth)) return swipeTo(-Math.sign(dx), npArt);
  if (mode === 'up' && up && dy < -60) return openQueue();
  if (mode === 'y' && up && far(dy, np.offsetHeight)) {
    const ms = reduce.matches ? 0 : 200;
    slideTo(np, 'translateY(100%)', '', ms);
    await sleep(ms);
    np.close();
    return slideTo(np, '', '');
  }
  slideTo(mode === 'x' ? npArt : np, '', '', 200);
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
