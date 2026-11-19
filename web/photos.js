// Galería de fotos y videos, y su visor.
import { $, el, icon, size, fmtDate, plural, lazyImg, toast, ask, empty, uploadButton } from './lib.js';
import { render } from './nav.js';
import { api } from './session.js';

let photos = [];

export async function photosView() {
  const r = await api('/api/photos');
  if (!r.ok) throw new Error('Intenta de nuevo en un momento.');
  photos = await r.json();
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
    frag.append(el('div', { className: 'grid' }, items.map(([it, i]) => {
      const b = el('button', { className: 'tile' }, lazyImg(`/api/photos/${it.id}/t?v=${it.v}`),
        it.kind === 'video' && el('span', { className: 'badge' }, icon('play'), 'Video'));
      b.setAttribute('aria-label', `${it.kind === 'video' ? 'Video' : 'Foto'} del ${fmtDate(it.taken)}`);
      b.onclick = () => openViewer(i);
      return b;
    })));
  }
  return frag;
}

// Visor

let vIndex = 0;
function openViewer(i) {
  vIndex = i;
  showItem();
  $('#viewer').showModal();
}
function showItem() {
  const it = photos[vIndex];
  const o = `/api/photos/${it.id}/o`;
  const media = it.kind === 'video'
    ? el('video', { src: o, controls: true, playsInline: true, poster: `/api/photos/${it.id}/t?v=${it.v}` })
    : el('img', { src: `/api/photos/${it.id}/p?v=${it.v}`, alt: it.name });
  $('#stage').replaceChildren(media);
  $('#v-title').textContent = new Date(it.taken).toLocaleDateString('es-CO', { day: 'numeric', month: 'long', year: 'numeric' });
  $('#v-sub').textContent = `${it.name} · ${size(it.size)} · ${vIndex + 1} de ${photos.length}`;
  $('#v-dl').href = `${o}?dl`;
  $('#v-prev').hidden = vIndex === 0;
  $('#v-next').hidden = vIndex === photos.length - 1;
}
function step(d) {
  const n = vIndex + d;
  if (n < 0 || n >= photos.length) return;
  vIndex = n;
  showItem();
}
$('#v-prev').onclick = () => step(-1);
$('#v-next').onclick = () => step(1);
$('#v-close').onclick = () => $('#viewer').close();
$('#viewer').onclose = () => $('#stage').replaceChildren(); // detiene el video
$('#viewer').addEventListener('keydown', (e) => {
  if (e.key === 'ArrowLeft') step(-1);
  if (e.key === 'ArrowRight') step(1);
});
// Deslizar entre fotos, o hacia abajo para cerrar. En videos no, para no chocar con los controles del reproductor.
let touchX = null;
let touchY = 0;
$('#stage').addEventListener('touchstart', (e) => {
  touchX = e.touches.length === 1 && photos[vIndex]?.kind !== 'video' ? e.touches[0].clientX : null;
  touchY = e.touches[0].clientY;
}, { passive: true });
$('#stage').addEventListener('touchend', (e) => {
  if (touchX == null) return;
  const dx = e.changedTouches[0].clientX - touchX;
  const dy = e.changedTouches[0].clientY - touchY;
  if (dy > 90 && dy > Math.abs(dx) * 1.5 && !(visualViewport?.scale > 1)) $('#viewer').close(); // con zoom, el dedo mueve la foto
  else if (Math.abs(dx) > 50) step(dx < 0 ? 1 : -1);
  touchX = null;
});
$('#v-del').onclick = async () => {
  if (!(await ask({ heading: '¿Eliminar del servidor?', text: 'No se borra de tu celular.', ok: 'Eliminar', danger: true }))) return;
  const it = photos[vIndex];
  const r = await api(`/api/photos/${it.id}`, { method: 'DELETE' });
  if (!r.ok) return toast('No se pudo eliminar.', { kind: 'warn' });
  photos.splice(vIndex, 1);
  if (!photos.length) $('#viewer').close();
  else { vIndex = Math.min(vIndex, photos.length - 1); showItem(); }
  render();
};
