// Navegación: secciones, rutas en el hash y direcciones del API de cada sección.

export const TABS = {
  fotos: { title: 'Fotos', icon: 'image' },
  archivos: { title: 'Archivos', icon: 'folder' },
  musica: { title: 'Música', icon: 'music' },
  admin: { title: 'Administración', icon: 'activity' }, // sin pestaña: se entra por el ícono del encabezado
};

// Navegación: la ruta vive en el hash (#/archivos/Viajes/2024), así funciona "atrás"

export function route(hash = location.hash) {
  const [tab, ...rest] = hash.replace(/^#\/?/, '').split('/').map(decodeURIComponent);
  return TABS[tab] ? { tab, path: rest.filter(Boolean).join('/') } : { tab: 'fotos', path: '' };
}
export const href = (tab, path = '') => '#/' + [tab, ...path.split('/').filter(Boolean)].map(encodeURIComponent).join('/');
export const go = (tab, path) => (location.hash = href(tab, path));

const isLib = (tab) => tab === 'musica'; // biblioteca compartida, de solo lectura
const q = (tab, path) => (isLib(tab) ? `lib=${tab}&` : '') + 'path=' + encodeURIComponent(path);
export const rawUrl = (tab, path) => `/api/${isLib(tab) ? 'media' : 'files'}/raw?${q(tab, path)}`;
export const thumbUrl = (tab, path) => `/api/media/thumb?${q(tab, path)}`;

// Las vistas piden repintar con render(); app.js registra aquí la función que lo hace.
let renderer = () => {};
export const setRenderer = (f) => (renderer = f);
export const render = () => renderer();
