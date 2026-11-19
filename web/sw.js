// PRUEBA (etapa 1 del modo sin conexión): que la página de prueba abra sin servidor.
// Solo atiende esa página; todo lo demás de la app sigue yendo a la red como siempre.
const CACHE = 'palobox-prueba-sin-conexion';
const PAGE = '/sinconexion.html';

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.add(PAGE)).then(() => self.skipWaiting()));
});
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));
self.addEventListener('fetch', (e) => {
  if (new URL(e.request.url).pathname !== PAGE) return;
  // Con red, la versión del servidor (y se actualiza la copia); sin red, la copia guardada.
  e.respondWith(fetch(e.request).then((r) => {
    if (r.ok) caches.open(CACHE).then((c) => c.put(PAGE, r.clone()));
    return r;
  }).catch(() => caches.match(PAGE)));
});
