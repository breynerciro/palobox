// Service worker: que la app abra sin servidor (modo sin conexión, web/offline.js).
// Con red no cambia nada: todo se pide a la red como siempre y solo se guarda una copia; la copia se usa
// únicamente cuando la red falla. Las peticiones al API no pasan por aquí, salvo las carátulas ya descargadas.
const SHELL = 'palobox-shell';
const AUDIO = 'palobox-offline-audio'; // lo llena offline.js: audio y carátulas de lo descargado
// Todos los archivos de la interfaz (main_test.go comprueba que no falte ninguno).
const FILES = ['/', '/style.css', '/app.js', '/admin.js', '/files.js', '/jam.js', '/lib.js', '/music.js', '/music-views.js', '/nav.js',
  '/offline.js', '/photos.js', '/player.js', '/session.js', '/uploads.js', '/manifest.webmanifest', '/icon.svg', '/icon-180.png', '/icon-192.png', '/icon-512.png'];

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(SHELL).then((c) => c.addAll(FILES)).then(() => self.skipWaiting()));
});
self.addEventListener('activate', (e) => {
  e.waitUntil(caches.delete('palobox-prueba-sin-conexion').then(() => self.clients.claim())); // la caché de la prueba de la etapa 1
});
self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== 'GET' || url.origin !== location.origin) return;
  if (url.pathname === '/api/media/thumb') { // carátula: la red si hay; si no, la descargada
    e.respondWith(fetch(e.request).catch(() => caches.open(AUDIO).then((c) => c.match(e.request.url)).then((r) => r ?? Response.error())));
    return;
  }
  if (url.pathname.startsWith('/api/')) return;
  const key = e.request.mode === 'navigate' ? '/' : url.pathname;
  e.respondWith(fetch(e.request).then((r) => {
    if (r.ok) caches.open(SHELL).then((c) => c.put(key, r.clone()));
    return r;
  }).catch(() => caches.open(SHELL).then((c) => c.match(key)).then((r) => r ?? Response.error())));
});
