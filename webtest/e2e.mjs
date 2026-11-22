// Prueba punta a punta contra una instancia de prueba de PaloBox (la levanta ../e2e.sh), con Chromium sin
// ventana por su protocolo de depuración: sin dependencias. Uso: E2E_PASS=… node webtest/e2e.mjs URL usuario
// El usuario debe ser el administrador de esa instancia. Sale con código 1 si algo falla.
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const [base, user] = process.argv.slice(2);
const pass = process.env.E2E_PASS;
const profile = mkdtempSync(join(tmpdir(), 'palobox-e2e-'));
const chrome = spawn(process.env.CHROMIUM || 'chromium', ['--headless=new', '--remote-debugging-port=9334', `--user-data-dir=${profile}`, '--no-first-run', '--window-size=1280,900', 'about:blank'], { stdio: 'ignore' });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let ws;
for (let i = 0; i < 60 && !ws; i++) {
  await sleep(250);
  const tabs = await fetch('http://127.0.0.1:9334/json').then((r) => r.json()).catch(() => null);
  const t = tabs?.find((x) => x.type === 'page');
  if (t) ws = new WebSocket(t.webSocketDebuggerUrl);
}
await new Promise((r) => (ws.onopen = r));
let id = 0;
const wait = new Map(), errors = [];
ws.onmessage = (m) => {
  const d = JSON.parse(m.data);
  if (d.id) wait.get(d.id)?.(d);
  if (d.method === 'Runtime.exceptionThrown') errors.push(d.params.exceptionDetails.exception?.description || d.params.exceptionDetails.text);
  if (d.method === 'Runtime.consoleAPICalled' && d.params.type === 'error') errors.push(d.params.args.map((a) => a.value ?? a.description).join(' '));
};
const send = (method, params = {}) => new Promise((r) => { wait.set(++id, r); ws.send(JSON.stringify({ id, method, params })); });
// js corre código en la página (con la sesión del usuario) y devuelve su resultado.
const js = async (body, arg) => {
  const r = await send('Runtime.evaluate', { expression: `(async (arg) => { ${body} })(${JSON.stringify(arg ?? null)})`, awaitPromise: true, returnByValue: true });
  if (r.result.exceptionDetails) throw new Error(r.result.exceptionDetails.exception?.description || 'error en la página');
  return r.result.result.value;
};
const until = async (cond, what, ms = 30000) => {
  for (const t0 = Date.now(); Date.now() - t0 < ms; await sleep(200)) if (await js(`return !!(${cond})`)) return;
  throw new Error('no llegó: ' + what);
};
// api hace una petición desde la página: mismo origen, misma sesión que usaría la interfaz.
const api = (path, method = 'GET', body) => js(`
  const r = await fetch(arg.path, { method: arg.method, headers: arg.body ? { 'Content-Type': 'application/json' } : {}, body: arg.body && JSON.stringify(arg.body) });
  const text = await r.text();
  let data = null; try { data = JSON.parse(text); } catch {}
  return { status: r.status, data, text: text.slice(0, 200), type: r.headers.get('content-type'), bytes: text.length };`, { path, method, body });

let failed = 0;
async function step(name, fn) {
  const t0 = Date.now();
  try {
    const note = await fn();
    console.log(`  ok    ${name}${note ? ' — ' + note : ''} (${((Date.now() - t0) / 1000).toFixed(1)} s)`);
  } catch (e) {
    failed++;
    console.log(`  FALLA ${name}: ${e.message}`);
  }
}
const must = (cond, msg) => { if (!cond) throw new Error(msg); };
const q = (s) => `document.querySelector(${JSON.stringify(s)})`;

let genres, seed, playlistId;
try {
  await send('Runtime.enable');
  await send('Page.enable');
  await send('Page.navigate', { url: base });

  await step('entrar con el usuario de prueba', async () => {
    await until(`${q('#login')} && !${q('#login')}.hidden`, 'pantalla de entrada');
    await js(`document.querySelector('#u').value = arg.user; document.querySelector('#p').value = arg.pass; document.querySelector('#login-btn').click();`, { user, pass });
    await until(`!${q('#app')}.hidden`, 'la app');
    const me = await api('/api/me');
    must(me.data?.name === user && me.data.admin && me.data.ai, `/api/me: ${me.text}`);
  });

  await step('inicio y catálogo de música', async () => {
    const lib = await api('/api/music');
    must(lib.status === 200, `/api/music ${lib.status}`);
    const s = await api('/api/music/search?q=merengue');
    must(s.status === 200 && s.data.tracks, `buscar ${s.status}`);
    return `biblioteca ${Math.round(lib.bytes / 1024)} KB`;
  });

  await step('Inicio: las filas que llegan después no rearman las que ya estaban', async () => {
    // Rearmarlas reinicia la aparición de cada carátula: se ve como un parpadeo de toda la pantalla.
    // Las sugerencias se demoran a propósito y se entra a Inicio de nuevo: así las primeras filas se ven solas.
    await js(`const real = window.fetch; window.fetch = (u, o) => (String(u).includes('/api/music/radio') ? new Promise((r) => setTimeout(r, 2000)).then(() => real(u, o)) : real(u, o)); location.hash = '#/musica/buscar';`);
    await sleep(500);
    await js(`location.hash = '#/musica'`);
    await until(`document.querySelector('#view .card') && ![...document.querySelectorAll('#view h2')].some((h) => h.textContent.includes('Descubre'))`, 'las primeras filas');
    const n = await js(`const cs = document.querySelectorAll('#view .card'); for (const c of cs) c.dataset.first = '1'; return cs.length;`);
    await until(`[...document.querySelectorAll('#view h2')].some((h) => h.textContent.includes('Descubre'))`, 'la fila «Descubre en tu biblioteca»');
    const r = await js(`return { kept: document.querySelectorAll('#view .card[data-first]').length, total: document.querySelectorAll('#view .card').length }`);
    must(r.kept === n, `de ${n} tarjetas iniciales quedaron ${r.kept}: se rearmaron`);
    must(r.total > n, 'no entraron filas nuevas');
    return `${n} tarjetas intactas, ${r.total - n} nuevas`;
  });

  await step('géneros por artista', async () => {
    const g = await api('/api/admin/genres');
    must(g.status === 200 && g.data.genres.length > 20, `géneros ${g.status}`);
    genres = Object.fromEntries(g.data.artists.map((a) => [a.name, a.genre]));
    const none = g.data.artists.filter((a) => !a.genre).length;
    return `${g.data.artists.length} artistas, ${none} sin género`;
  });

  await step('sugerencias: mismo género que la semilla y repartidas', async () => {
    // Semilla: el artista de merengue con más canciones de la biblioteca.
    const artist = Object.keys(genres).filter((a) => genres[a] === 'tropical/merengue')[0];
    must(artist, 'no hay artistas de merengue');
    const a = await api('/api/music/artist?path=' + encodeURIComponent(artist));
    seed = a.data.tracks[0];
    const r = await api('/api/music/radio?path=' + encodeURIComponent(seed.path));
    must(r.status === 200 && r.data.length === 40, `radio ${r.status}, ${r.data?.length} pistas`);
    const first = r.data.slice(0, 20), per = {};
    for (const t of first) {
      must(genres[t.artistPath] === 'tropical/merengue', `sugirió ${t.artistPath} (${genres[t.artistPath] || 'sin género'}) después de un merengue`);
      must((per[t.artistPath] = (per[t.artistPath] || 0) + 1) <= 2, `tres seguidas de ${t.artistPath}`);
    }
    must(r.data.every((t) => (genres[t.artistPath] || '').startsWith('tropical/')), 'salió de la familia tropical');
    must(!r.data.some((t) => t.path === seed.path), 'repitió la semilla');
    return `tras «${seed.title}» (${artist}): 20 merengues de ${Object.keys(per).length} artistas`;
  });

  await step('el audio de una canción se entrega', async () => {
    const r = await js(`const r = await fetch('/api/music/offline?path=' + encodeURIComponent(arg)); const b = await r.arrayBuffer(); return { status: r.status, bytes: b.byteLength };`, seed.path);
    must(r.status === 200 && r.bytes > 100000, `audio ${r.status}, ${r.bytes} bytes`);
    return `${(r.bytes / 1e6).toFixed(1)} MB`;
  });

  await step('registrar una escucha y un salto', async () => {
    must((await api('/api/music/plays', 'POST', { path: seed.path })).status === 200, 'escucha');
    must((await api('/api/music/skips', 'POST', { path: seed.path, auto: true })).status === 200, 'salto');
    must((await api('/api/music/skips', 'POST', { path: 'no/existe.mp3', auto: true })).status === 404, 'aceptó un salto de una pista que no existe');
  });

  await step('playlist por descripción sin IA (solo palabras) no gasta cupo', async () => {
    const before = await api('/api/music/playlists/ai', 'POST', { prompt: 'x' });
    must(before.status === 400, `pedido de una letra: ${before.status}`);
    const artist = seed.artistPath;
    const r = await api('/api/music/playlists/ai', 'POST', { prompt: `canciones de ${artist}` });
    must(r.status === 200 && r.data.tracks.length > 0, `${r.status} ${r.text}`);
    must(r.data.tracks.every((t) => t.artistPath === artist), 'trajo canciones de otro artista');
    return `${r.data.tracks.length} canciones, quedan ${r.data.left} de hoy`;
  });

  await step('playlist por descripción desde la interfaz (con Gemini)', async () => {
    await js(`location.hash = '#/musica'`);
    await until(q('#side-new'), 'botón de crear');
    await sleep(800);
    await js(`${q('#side-new')}.click()`);
    await until(`${q('#menu')}.open`, 'menú de crear');
    const items = await js(`return [...document.querySelectorAll('#menu-list button')].map((b) => b.textContent)`);
    must(items.length === 2, `opciones del menú: ${items}`);
    await js(`[...document.querySelectorAll('#menu-list button')].find((b) => b.textContent.includes('Describe')).click()`);
    await until(`${q('#ask')}.open`, 'diálogo de descripción');
    await js(`${q('#ask-input')}.value = 'salsa romántica de los 90'; ${q('#ask-ok')}.click();`);
    await until(`${q('#ask')}.open && ${q('#ask-ok')}.textContent === 'Guardar'`, 'la propuesta', 60000);
    const name = await js(`return ${q('#ask-input')}.value`);
    await js(`${q('#ask-ok')}.click()`);
    await until(`location.hash.includes('playlist/')`, 'la playlist guardada');
    playlistId = (await js(`return location.hash`)).split('playlist/')[1];
    const p = await api('/api/music/playlists/' + playlistId);
    must(p.status === 200 && p.data.tracks.length >= 5, `playlist guardada: ${p.status}, ${p.data?.tracks?.length} canciones`);
    // La biblioteca real sigue creciendo mientras corre la prueba: se vuelve a leer qué género tiene cada artista.
    genres = Object.fromEntries((await api('/api/admin/genres')).data.artists.map((a) => [a.name, a.genre]));
    const off = p.data.tracks.filter((t) => genres[t.artistPath] !== 'tropical/salsa' || t.year < 1990 || t.year > 1999);
    must(off.length === 0, `fuera del pedido: ${off.slice(0, 3).map((t) => `${t.title} (${t.artistPath}, ${t.year})`).join('; ')}`);
    return `«${name}», ${p.data.tracks.length} canciones de salsa de los 90`;
  });

  await step('borrar la playlist de prueba', async () => {
    must(playlistId, 'no se creó');
    must((await api('/api/music/playlists/' + playlistId, 'DELETE')).status === 200, 'no se pudo borrar');
  });

  await step('sin errores en la consola del navegador', async () => {
    // Las respuestas 4xx que estas pruebas provocan a propósito también salen en la consola: no cuentan.
    const real = errors.filter((e) => !/Failed to load resource/.test(e));
    must(real.length === 0, real.join(' | '));
  });
} catch (e) {
  failed++;
  console.log('  FALLA (preparación):', e.message);
}
ws.close();
chrome.kill();
await new Promise((r) => chrome.on('exit', r)); // el perfil no se puede borrar mientras Chromium sigue cerrando
try { rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }); } catch {}
console.log(failed ? `\n${failed} pruebas fallaron.` : '\nTodas las pruebas punta a punta pasaron.');
process.exit(failed ? 1 : 0);
