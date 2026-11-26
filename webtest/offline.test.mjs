// Pruebas del modo sin conexión (web/offline.js) con un navegador de mentira: node --test webtest/
import test from 'node:test';
import assert from 'node:assert/strict';

const KEY = 'palobox.offline';
const disk = new Map();
let full = false; // simula el cupo de localStorage lleno
globalThis.localStorage = {
  getItem: (k) => disk.get(k) ?? null,
  setItem: (k, v) => { if (full) throw new DOMException('lleno', 'QuotaExceededError'); disk.set(k, String(v)); },
  removeItem: (k) => disk.delete(k),
};
const stores = new Map();
globalThis.caches = {
  open: async (name) => {
    const m = stores.get(name) ?? stores.set(name, new Map()).get(name);
    return { put: async (k, r) => void m.set(k, await r.blob()), match: async (k) => (m.has(k) ? new Response(m.get(k)) : undefined), delete: async (k) => m.delete(k) };
  },
  delete: async (name) => stores.delete(name),
};
globalThis.location = { origin: 'http://palobox.test' };
globalThis.document = { body: { classList: { add() {} } } };
// Un audio limpio de n cuadros AAC con cabecera ADTS (384 bytes por cuadro), como lo entrega el servidor.
const aac = (n) => {
  const b = new Uint8Array(384 * n);
  for (let i = 0; i < n; i++) b.set([0xFF, 0xF1, 0x50, 0x80, 0x30, 0x1F, 0xFC], 384 * i); // cabecera ADTS de un cuadro de 384 bytes
  return b;
};
globalThis.fetch = async (url) => (String(url).includes('/api/music/offline') ? new Response(aac(3)) : new Response('', { status: 404 }));

const track = (n, title, artist, album) => ({ path: `${artist}/${album}/${n}.mp3`, title, artist, album, albumPath: `${artist}/${album}`, artistPath: artist, year: 2020, num: n, dur: 1 });
const A = track(1, 'Amor eterno', 'Alcolirykoz', 'Servicios');
const B = track(2, 'Canción triste', 'Alcolirykoz', 'Servicios');
const C = track(1, 'Biya', 'Otro', 'Disco');
const json = (r) => r.json();

const off = await import('../web/offline.js');
await off.login({ name: 'ana', musicOnly: true });

test('cuenta los cuadros de un audio limpio y salta lo que no es cuadro', () => {
  assert.equal(off.countFrames(aac(5)), 5);
  assert.equal(off.countFrames(new Uint8Array([1, 2, 3, ...aac(2)])), 2);
  assert.equal(off.countFrames(new Uint8Array(100)), 0);
});

test('descarga, lista y responde como el servidor solo con lo descargado', async () => {
  await off.download([A, B, C]);
  assert.equal(off.count(), 3);
  assert.equal(off.bytes(), 3 * 3 * 384);
  assert.deepEqual(off.tracks().map((t) => t.title), ['Amor eterno', 'Canción triste', 'Biya']);
  assert.deepEqual(off.pending(), []);
  const lib = await json(off.handle('/api/music'));
  assert.deepEqual(lib.albums.map((a) => [a.name, a.tracks]), [['Disco', 1], ['Servicios', 2]]);
  assert.deepEqual(lib.artists.map((a) => a.name), ['Alcolirykoz', 'Otro']);
  const al = await json(off.handle(`/api/music/album?path=${encodeURIComponent(A.albumPath)}`));
  assert.deepEqual(al.tracks.map((t) => t.num), [1, 2]);
  assert.equal(off.handle('/api/music/album?path=nada').status, 404);
  assert.equal(off.handle('/api/music/likes', { method: 'POST' }).status, 503); // los cambios necesitan conexión
  assert.equal(off.handle('/api/photos').status, 503);
  const [x] = await off.blobs([A.path]);
  assert.equal(x.blob.size, 3 * 384);
  assert.ok(Math.abs(x.secs - 3 * 1024 / 44100) < 1e-9);
});

// Los mismos casos de internal/music/search_test.go: la búsqueda sin conexión no se puede desviar de la del servidor.
test('la búsqueda tolera lo mismo que la del servidor', () => {
  for (const [q, s, ok] of [
    ['Alkolirykos', 'Anarcolirykoz Alcolirykoz', true], ['alcolirikos', 'Alcolirykoz', true], ['alcolirykz', 'Alcolirykoz', true],
    ['cancion', 'Canción triste', true], ['villa', 'Biya', true], ['amar', 'Amor eterno', false],
    ['queen rock', 'Bohemian Rhapsody Queen', false], ['chico', 'Hico', false],
  ]) assert.equal(off.matcher(q)(s) >= 0, ok, `«${q}» en «${s}»`);
});

test('buscar sin conexión encuentra con errores de tecleo y pone primero el título', async () => {
  const r = await json(off.handle('/api/music/search?q=alkolirykos'));
  assert.deepEqual(r.artists.map((a) => a.name), ['Alcolirykoz']);
  assert.equal(r.tracks.length, 2);
  assert.deepEqual((await json(off.handle('/api/music/search?q=villa'))).tracks.map((t) => t.title), ['Biya']);
  assert.deepEqual((await json(off.handle('/api/music/search?q=a'))).tracks, []);
});

test('una playlist guardada solo muestra lo descargado y se va con su última canción', async () => {
  off.keepPlaylist({ id: 7, name: 'Carro' }, [A, C, track(9, 'Sin bajar', 'X', 'Y')]);
  assert.deepEqual((await json(off.handle('/api/music/playlists/7'))).tracks.map((t) => t.title), ['Amor eterno', 'Biya']);
  await off.remove([A.path]);
  assert.equal(off.has(A.path), false);
  assert.equal(off.keeps(7), true);
  await off.remove([C.path]);
  assert.equal(off.keeps(7), false);
  await assert.rejects(off.blobs([A.path]));
});

test('si no cabe la anotación, la descarga falla y no deja audio huérfano', async () => {
  full = true;
  await assert.rejects(off.download([A]), { name: 'QuotaExceededError' });
  full = false;
  assert.equal(off.has(A.path), false);
  assert.equal(await (await caches.open('palobox-offline-audio')).match(off.audioUrl(A.path)), undefined);
});

test('una pista que el servidor no tiene o llega dañada no frena a las demás; sin red todo queda pendiente', async () => {
  const net = globalThis.fetch;
  const gone = track(5, 'Borrada', 'Otro', 'Disco');
  const broken = track(6, 'Dañada', 'Otro', 'Disco');
  globalThis.fetch = async (url) => (String(url) === off.audioUrl(gone.path) ? new Response('', { status: 404 }) : String(url) === off.audioUrl(broken.path) ? new Response('nada') : net(url));
  assert.equal(await off.download([gone, broken, A]), 2);
  assert.equal(off.has(A.path), true);
  assert.deepEqual(off.pending(), []);
  globalThis.fetch = async () => { throw new TypeError('sin red'); };
  await assert.rejects(off.download([gone, C]));
  assert.deepEqual(off.pending().map((t) => t.title), ['Borrada', 'Biya']);
  globalThis.fetch = async () => new Response('', { status: 502 }); // el túnel responde, el servidor no
  await assert.rejects(off.download([C]));
  assert.deepEqual(off.pending().map((t) => t.title), ['Borrada', 'Biya']);
  globalThis.fetch = net;
});

test('entra sin conexión quien inició sesión, pero no si pasó demasiado tiempo ni otro usuario', async () => {
  assert.equal(off.saved().name, 'ana');
  disk.set(KEY, JSON.stringify({ ...JSON.parse(disk.get(KEY)), seen: Date.now() - 31 * 24 * 3600 * 1000 }));
  const old = await import('../web/offline.js?vencida');
  assert.equal(old.saved(), null);
  await off.login({ name: 'beto', musicOnly: true }); // otro usuario en el mismo celular: no hereda las descargas
  assert.equal(off.count(), 0);
  assert.equal(off.saved().name, 'beto');
});

test('lo guardado con un formato anterior se descarta y la cuenta sigue', async () => {
  disk.set(KEY, JSON.stringify({ v: 1, user: 'ana', musicOnly: true, tracks: { x: {} } }));
  const v1 = await import('../web/offline.js?anterior');
  assert.equal(v1.count(), 0);
  assert.equal(JSON.parse(disk.get(KEY)).user, 'ana');
});

test('el audio unido cubre un tramo de la cola: una antes, la actual y lo que quepa', () => {
  const durs = Array(10).fill(100);
  assert.deepEqual(off.span(durs, 0, 350), [0, 3]); // 100 + 100 + 100 caben; la cuarta ya no
  assert.deepEqual(off.span(durs, 5, 350), [4, 7]); // empieza una antes de la actual
  assert.deepEqual(off.span(durs, 9, 350), [8, 10]); // al final de la cola no se pasa
  assert.deepEqual(off.span([5000, 5000], 1, 100), [0, 2]); // la actual entra aunque sola ya supere el tope
  assert.deepEqual(off.span([0, 0, 0], 0, 500), [0, 2]); // sin duración conocida cada pista cuenta como 4 minutos
});

test('donde hay MediaSource con Opus, lo que se descarga con el celular vacío va en el formato compacto', async () => {
  await off.wipe();
  assert.equal(off.canCompact(), false); // sin descargas no hay nada que pasar
  await off.download([A]); // este «navegador» aún no tiene MediaSource: formato original
  assert.equal(off.compact(), false);
  const asked = [];
  const webm = new Uint8Array(2000); webm.set([0x1A, 0x45, 0xDF, 0xA3]);
  const before = globalThis.fetch;
  globalThis.fetch = async (url) => { if (String(url).includes('/api/music/offline')) asked.push(String(url)); return String(url).includes('fmt=opus') ? new Response(webm) : before(url); };
  globalThis.MediaSource = { isTypeSupported: (t) => t.includes('opus') };
  try {
    assert.equal(off.canCompact(), true); // hay descargas viejas y el celular ya admite el compacto
    await off.download([B]); // pero mientras no se pasen todas, lo nuevo sigue en el formato de las demás
    assert.equal(off.compact(), false);
    assert.ok(!asked.at(-1).includes('fmt=opus'));
    const ts = await off.toCompact();
    assert.deepEqual(ts.map((t) => t.path).sort(), [A.path, B.path].sort());
    assert.equal(off.count(), 0);
    assert.equal(off.pending().length, 2);
    await off.download(ts);
    assert.equal(off.compact(), true);
    assert.equal(off.bytes(), 2 * 2000);
    assert.ok(asked.slice(-2).every((u) => u.includes('fmt=opus')));
    const [x] = await off.blobs([A.path]);
    assert.equal(x.blob.size, 2000);
    assert.equal(x.secs, A.dur);
  } finally {
    delete globalThis.MediaSource;
    globalThis.fetch = before;
    await off.wipe();
  }
});
