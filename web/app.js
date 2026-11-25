// Punto de entrada: decide qué vista pintar según la ruta y mantiene la interfaz al día.
import { $, el, icon, empty, toast } from './lib.js';
import { TABS, route, go, setRenderer, render } from './nav.js';
import { audio, markPlaying, joinJam } from './player.js';
import { isAdmin, musicOnly, userName, start } from './session.js';
import * as offline from './offline.js';
import { uploading } from './uploads.js';
import { photosView } from './photos.js';
import { filesView } from './files.js';
import { ensureLib } from './music.js';
import { musicView, greeting, syncSearchBox } from './music-views.js';
import { adminView } from './admin.js';

let lastHash = ''; // de dónde se entró a Administración, para volver ahí
let lastCloud = '#/fotos'; // última sección fuera de Música: a ella vuelve «Nube»
let lastView = ''; // vista pintada la última vez: repintarla (buscar mientras se escribe, una acción del panel) no muestra el esqueleto
const MUSIC = { '': 'inicio', buscar: 'buscar', biblioteca: 'biblioteca' }; // vistas raíz de Música; el resto son páginas de detalle

window.addEventListener('hashchange', () => !$('#app').hidden && render());
// Cambiar de sección o de pestaña no se apila en el historial, como en una app nativa:
// «atrás» recorre las páginas dentro de la sección y no devuelve a la sección anterior.
const TAB_LINKS = '#tabs a, #mtabs a, #side-exit, #side nav a, #side .side-head a';
document.addEventListener('click', (e) => {
  const a = e.target.closest(TAB_LINKS);
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  e.preventDefault();
  if (a.href === location.href) { // tocar la pestaña en la que ya se está sube al inicio; en Buscar, además, deja escribir
    scrollTo({ top: 0, behavior: 'smooth' });
    if (a.dataset.m === 'buscar') $('#search').focus();
    return;
  }
  location.replace(a.href);
});

// Menú de la cuenta: en el celular reúne, igual en todas las secciones, lo que en escritorio son los botones de la barra de arriba.
$('#me-btn').onclick = () => {
  const d = $('#me');
  const item = (glyph, label, run) => {
    const b = el('button', { className: 'menu-item' }, icon(glyph), label);
    b.onclick = () => { d.close(); run(); };
    return el('li', {}, b);
  };
  $('#me-avatar').textContent = $('#me-btn').textContent;
  $('#me-name').textContent = userName;
  const music = document.body.classList.contains('music');
  $('#me-list').replaceChildren(...[
    music && !offline.active && item('cast', 'Dispositivos y jam', () => $('#jam-link').click()),
    music && !offline.active && !musicOnly && item('cloud', 'Fotos y archivos', () => location.replace(lastCloud)),
    !music && item('music', 'Música', () => location.replace('#/musica')),
    !offline.active && isAdmin && item('activity', 'Administración', () => { location.hash = '#/admin'; }),
    item('logout', 'Cerrar sesión', () => $('#logout').click()),
  ].filter(Boolean));
  d.showModal();
};
$('#me').onclick = (e) => e.target === e.currentTarget && e.currentTarget.close(); // clic en el telón

let renderId = 0;
let wasMusic; // la vista anterior era de Música (undefined antes de la primera)
async function renderView() {
  // Enlace de invitación a un jam (#/jam/CÓDIGO): se une y sigue en Música.
  const invite = location.hash.match(/^#\/jam\/([a-z0-9]{6})$/i);
  if (invite) {
    history.replaceState(null, '', '#/musica');
    joinJam(invite[1]);
  }
  // Una cuenta de solo Música (o cualquiera sin conexión) no tiene Fotos ni Archivos (el servidor tampoco se los entrega).
  if ((musicOnly && ['fotos', 'archivos'].includes(route().tab)) || (!isAdmin && route().tab === 'admin')) history.replaceState(null, '', '#/musica');
  const { tab, path } = route();
  const id = ++renderId;
  for (const a of document.querySelectorAll('#tabs a')) {
    a.dataset.tab === tab ? a.setAttribute('aria-current', 'page') : a.removeAttribute('aria-current');
  }
  const parts = path ? path.split('/') : [];
  const admin = tab === 'admin';
  const music = tab === 'musica';
  const section = music && Object.hasOwn(MUSIC, parts[0] ?? '') ? MUSIC[parts[0] ?? ''] : null;
  const detail = music && !section; // álbum, artista o playlist: la vista trae su propio encabezado
  if (!admin) lastHash = location.hash;
  if (tab === 'fotos' || tab === 'archivos') lastCloud = location.hash || '#/fotos';

  // En Música la barra inferior (o la lateral en escritorio) es la de la sección.
  // Fuera de Música el reproductor arranca minimizado (el botón redondo); al salir de Música se vuelve a minimizar.
  if (!music && (wasMusic ?? true)) document.body.classList.add('player-tucked');
  wasMusic = music;
  document.body.classList.toggle('music', music);
  document.body.classList.toggle('flat', music || tab === 'archivos' || tab === 'fotos'); // listas sin caja en el celular
  $('#tabs').hidden = music;
  $('#mtabs').hidden = $('#side').hidden = !music;
  $('#mtabs-exit').href = $('#side-exit').href = musicOnly ? '#/musica' : lastCloud;
  for (const a of document.querySelectorAll('[data-m]')) {
    a.dataset.m === section ? a.setAttribute('aria-current', 'page') : a.removeAttribute('aria-current');
  }
  if (music) ensureLib().catch(() => {}); // la barra lateral y los me gusta, también al entrar directo a un álbum

  $('#title').textContent = section === 'inicio' ? greeting() : section === 'buscar' ? 'Buscar' : section === 'biblioteca' ? 'Tu biblioteca' : admin ? TABS.admin.title : parts.at(-1) ?? TABS[tab].title;
  $('#title').hidden = detail;
  $('#subtitle').textContent = '';
  $('#admin-link').hidden = !isAdmin || admin;
  $('#jam-link').hidden = !music;
  $('#me-btn').textContent = userName.slice(0, 1).toUpperCase();
  $('#back').hidden = music ? !detail : !parts.length && !admin;
  $('#me-btn').hidden = !$('#back').hidden; // en el celular, a la izquierda va el avatar o la flecha de volver
  $('#back-label').textContent = admin ? TABS[route(lastHash).tab].title : music ? 'Atrás' : parts.at(-2) ?? TABS[tab].title;
  $('#back').onclick = () => {
    if (admin) location.hash = lastHash || '#/musica';
    else if (!music) go(tab, parts.slice(0, -1).join('/'));
    else if (history.length > 1) history.back();
    else go('musica', '');
  };
  $('#add').hidden = tab !== 'fotos' && tab !== 'archivos';
  $('#mkdir').hidden = tab !== 'archivos';
  document.title = `${music ? (section === 'inicio' ? 'Música' : $('#title').textContent) : $('#title').textContent} · PaloBox`;

  $('#searchbox').hidden = section !== 'buscar';
  if (section === 'buscar') {
    $('#search').placeholder = offline.active ? 'Busca en tu música descargada' : 'Busca o pega un enlace de Spotify';
    syncSearchBox();
  }

  const view = $('#view');
  const viewKey = `${tab}/${section ?? path}`;
  view.setAttribute('aria-busy', 'true');
  // Repintar la misma vista deja la anterior hasta que llega la nueva: sin parpadeo y sin perder el desplazamiento.
  if (lastView !== viewKey || !view.childElementCount) view.replaceChildren(skeleton(tab));
  lastView = viewKey;
  try {
    const node = tab === 'fotos' ? await photosView() : admin ? await adminView(path) : music ? await musicView(path) : await filesView(path);
    if (id !== renderId) return; // el usuario ya navegó a otra parte
    view.replaceChildren(node);
  } catch (e) {
    if (id === renderId) view.replaceChildren(empty('alert', 'No se pudo cargar', e.message));
  } finally {
    if (id === renderId) view.removeAttribute('aria-busy');
  }
  if (tab !== 'fotos') markPlaying();
}

function skeleton(tab) {
  if (tab === 'fotos') return el('div', { className: 'grid' }, Array.from({ length: 18 }, () => el('div', { className: 'tile skeleton' })));
  return el('ul', { className: 'list' }, Array.from({ length: 6 }, () => el('li', {}, el('div', { className: 'row' },
    el('span', { className: 'type skeleton' }), el('div', { className: 'row-text' })))));
}

// La app instalada en el celular no se recarga sola: al volver a primer plano, si el servidor
// tiene otra versión de la interfaz, se recarga. Si está sonando música no la corta: avisa y deja que el usuario
// decida. Con una subida en curso espera a la próxima vez.
let version, offered;
async function checkVersion() {
  const v = await fetch('/api/version').then((r) => (r.ok ? r.text() : null)).catch(() => null);
  if (!v) return;
  version ??= v;
  if (v === version || uploading) return;
  if (audio.paused) return location.reload();
  if (offered === v) return;
  offered = v;
  toast('Hay una versión nueva de PaloBox.', { sticky: true, action: { label: 'Actualizar', run: () => location.reload() } });
}
checkVersion();
document.addEventListener('visibilitychange', () => document.visibilityState === 'visible' && checkVersion());

setRenderer(renderView);
start();
