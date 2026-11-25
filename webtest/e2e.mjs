// Prueba punta a punta contra una instancia de prueba de PaloBox (la levanta ../e2e.sh), con Chromium sin
// ventana por su protocolo de depuración: sin dependencias. Uso: E2E_PASS=… node webtest/e2e.mjs URL usuario
// El usuario debe ser el administrador de esa instancia. Sale con código 1 si algo falla.
// E2E_CDP=http://127.0.0.1:PUERTO corre lo mismo en un Chrome ya abierto (el del celular, por adb forward): usa la
// pestaña que ya tenga abierta esta dirección y la cierra al terminar. E2E_SHOTS=carpeta guarda capturas de los pasos que las piden.
// E2E_SIZE=390,844 corre en el navegador de aquí con tamaño de celular.
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const [base, user] = process.argv.slice(2);
const pass = process.env.E2E_PASS;
const profile = mkdtempSync(join(tmpdir(), 'palobox-e2e-'));
const cdp = process.env.E2E_CDP;
const chrome = cdp ? null : spawn(process.env.CHROMIUM || 'chromium', ['--headless=new', '--remote-debugging-port=9334', `--user-data-dir=${profile}`, '--no-first-run', `--window-size=${process.env.E2E_SIZE || '1280,900'}`, 'about:blank'], { stdio: 'ignore' });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let ws, tab;
for (let i = 0; cdp && i < 60 && !ws; i++) { // la pestaña ya la abrió e2e.sh con un intent: Chrome de Android no deja crearla por aquí
  await sleep(250);
  tab = (await fetch(`${cdp}/json`).then((r) => r.json()).catch(() => [])).find((x) => x.type === 'page' && x.url.startsWith(base));
  if (tab) ws = new WebSocket(tab.webSocketDebuggerUrl);
}
if (cdp && !ws) { console.log('e2e: el Chrome del celular no abrió la copia de prueba'); process.exit(1); }
for (let i = 0; !cdp && i < 60 && !ws; i++) {
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

const shot = async (name) => {
  if (!process.env.E2E_SHOTS) return;
  const r = await send('Page.captureScreenshot', { format: 'jpeg', quality: 60 });
  writeFileSync(join(process.env.E2E_SHOTS, name + '.jpg'), Buffer.from(r.result.data, 'base64'));
};
const click = (sel) => js(`const n = document.querySelector(arg); if (!n) throw new Error('no está ' + arg); n.click();`, sel);
// answer acepta el diálogo de confirmación; con texto, lo escribe antes en su campo.
const answer = async (text) => {
  await until(`document.querySelector('#ask').open`, 'el diálogo');
  if (text != null) await js(`document.querySelector('#ask-input').value = arg`, text);
  await click('#ask-ok');
  await until(`!document.querySelector('#ask').open`, 'que cierre el diálogo');
};
// out pide desde fuera del navegador: otra persona, con su propia sesión (cookie) o sin ninguna.
const out = async (path, method = 'GET', body, cookie) => {
  const r = await fetch(new URL(path, base), { method, headers: { ...(body ? { 'Content-Type': 'application/json' } : {}), ...(cookie ? { Cookie: cookie } : {}) }, body: body && JSON.stringify(body) });
  const text = await r.text();
  let data = null; try { data = JSON.parse(text); } catch {}
  return { status: r.status, data, text: text.slice(0, 200), cookie: r.headers.getSetCookie().map((c) => c.split(';')[0]).join('; ') };
};

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

  await step('etiquetas de género: filtran la playlist y vuelven a mostrarla entera', async () => {
    const salsero = Object.keys(genres).find((x) => genres[x] === 'tropical/salsa');
    must(salsero, 'no hay artistas de salsa');
    const salsa = (await api('/api/music/artist?path=' + encodeURIComponent(salsero))).data.tracks[0];
    for (const t of [seed, salsa]) must((await api('/api/music/likes?path=' + encodeURIComponent(t.path), 'PUT')).status === 200, 'me gusta');
    await js(`location.hash = '#/musica/gustan'`);
    await until(`document.querySelectorAll('#view .toolbar .chip').length === 2`, 'las dos etiquetas (Merengue y Salsa)');
    const rows = `document.querySelectorAll('#view .tracks [data-track]').length`;
    must(await js(`return ${rows}`) === 2, 'sin filtro debían verse las dos canciones');
    const label = await js(`const c = [...document.querySelectorAll('#view .toolbar .chip')].find((c) => c.textContent === 'Salsa'); c?.click(); return c?.textContent;`);
    must(label === 'Salsa', 'no salió la etiqueta Salsa');
    const only = await js(`return [...document.querySelectorAll('#view .tracks [data-track]')].map((r) => r.dataset.track)`);
    must(only.length === 1 && only[0] === salsa.path, `con Salsa quedó: ${only}`);
    await js(`document.querySelector('#view .chip[aria-pressed=true]').click()`);
    must(await js(`return ${rows}`) === 2, 'al soltar la etiqueta no volvieron todas');
    for (const t of [seed, salsa]) await api('/api/music/likes?path=' + encodeURIComponent(t.path), 'DELETE');
    return 'Merengue y Salsa';
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

  // ---- El resto de la app, sin reproducir música: Fotos, Archivos, biblioteca, playlists, cuentas y sesión ----
  const mobile = await js(`return matchMedia('(max-width: 899px)').matches`);

  await step('fotos: subir, menú propio, selección múltiple, visor y borrar', async () => {
    await js(`location.hash = '#/fotos'`);
    await until(`${q('#view .empty')} || ${q('#view .tile')}`, 'la galería');
    const before = (await api('/api/photos')).data.length;
    await js(`const dt = new DataTransfer();
      for (const [name, color] of [['e2e-foto.jpg', '#14b8a6'], ['e2e-foto2.jpg', '#b98aff']]) {
        const c = document.createElement('canvas'); c.width = 640; c.height = 480; const g = c.getContext('2d');
        g.fillStyle = color; g.fillRect(0, 0, 640, 480); g.fillStyle = '#090d16'; g.fillRect(200, 150, 240, 180);
        dt.items.add(new File([await new Promise((r) => c.toBlob(r, 'image/jpeg', 0.9))], name, { type: 'image/jpeg' }));
      }
      const i = document.querySelector('#pick-media'); i.files = dt.files; i.dispatchEvent(new Event('change', { bubbles: true }));`);
    await until(`document.querySelectorAll('#view .tile').length === ${before + 2}`, 'las dos fotos en la galería', 60000);
    must(!(await js(`return document.querySelector('#toast-text').textContent`)).includes('convirt'), 'avisó de una conversión de formato que no hubo');
    await shot('fotos');
    const it = (await api('/api/photos')).data.find((p) => p.name === 'e2e-foto.jpg');
    must(it, 'la foto no quedó en /api/photos');
    for (const v of ['t', 'p', 'o']) { // miniatura, vista previa y original
      const r = await js(`const r = await fetch(arg); return { s: r.status, t: r.headers.get('content-type') || '', n: (await r.arrayBuffer()).byteLength };`, `/api/photos/${it.id}/${v}`);
      must(r.s === 200 && r.t.startsWith('image/') && r.n > 500, `versión ${v}: ${r.s} ${r.t} ${r.n} bytes`);
    }
    // Mantener presionada una miniatura (el celular manda contextmenu) abre el menú de la app, no el del navegador.
    const hold = (n) => js(`document.querySelectorAll('#view .tile')[arg].dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))`, n);
    const pickMenu = (text) => js(`[...document.querySelectorAll('#menu .menu-item')].find((b) => b.textContent.includes(arg)).click()`, text);
    await hold(0);
    await until(`${q('#menu')}.open`, 'el menú de la foto');
    must((await js(`return [...document.querySelectorAll('#menu .menu-item')].map((b) => b.textContent).join()`)) === 'Seleccionar,Descargar original,Eliminar', 'el menú de la foto no trae sus tres acciones');
    // Selección múltiple: marcar dos, cancelar, y nada se borra.
    await pickMenu('Seleccionar');
    await until(`document.body.classList.contains('selecting') && document.querySelectorAll('#view .tile[aria-pressed=true]').length === 1 && !${q('#sel-del')}.hidden`, 'la selección con una foto');
    await js(`document.querySelectorAll('#view .tile')[1].click()`);
    must((await js(`return document.querySelector('#subtitle').textContent`)).startsWith('2 seleccionadas'), 'la barra no cuenta dos seleccionadas');
    must(!(await js(`return document.querySelector('#viewer').open`)), 'en selección, tocar una foto abrió el visor');
    await shot('fotos-seleccion');
    await click('#sel-cancel');
    must(!(await js(`return document.body.classList.contains('selecting') || !!document.querySelector('#view .tile[aria-pressed=true]')`)), 'cancelar no quitó la selección');
    // Visor: tira de miniaturas, pasar a la siguiente y esconder las barras con un toque.
    await click('#view .tile');
    await until(`${q('#viewer')}.open && ${q('#stage img')} && ${q('#v-strip .v-thumb[aria-current]')}`, 'el visor con su tira');
    const first = await js(`return document.querySelector('#v-sub').textContent`);
    await shot('fotos-visor');
    await click('#v-next');
    await until(`document.querySelector('#v-sub').textContent !== ${JSON.stringify(first)}`, 'pasar a la siguiente');
    await js(`document.querySelector('#v-strip .v-thumb').click()`); // la primera de la tira: de vuelta
    await until(`document.querySelector('#v-sub').textContent === ${JSON.stringify(first)}`, 'volver con la tira');
    await click('#stage img');
    must(await js(`return document.querySelector('#viewer').classList.contains('bare')`), 'un toque no escondió las barras');
    await click('#stage img');
    await click('#v-del');
    await answer();
    await until(`document.querySelectorAll('#view .tile').length === ${before + 1}`, 'que la foto borrada desde el visor se vaya');
    await js(`document.querySelector('#viewer').close()`);
    // La otra se borra con la selección múltiple.
    const mine = (await api('/api/photos')).data.findIndex((p) => p.name.startsWith('e2e-foto'));
    await hold(mine);
    await until(`${q('#menu')}.open`, 'el menú de la segunda foto');
    await pickMenu('Seleccionar');
    await until(`!${q('#sel-del')}.hidden`, 'el botón de eliminar la selección');
    await click('#sel-del');
    await answer();
    await until(`document.querySelectorAll('#view .tile').length === ${before} && !document.body.classList.contains('selecting')`, 'que la selección se borre');
    must(!(await api('/api/photos')).data.some((p) => p.name.startsWith('e2e-foto')), 'quedaron fotos de prueba en el servidor');
    must((await api(`/api/photos/${it.id}/o`)).status === 404, 'el original sigue disponible después de borrar');
  });

  await step('fotos: un video con los controles de la app (reproducir, avanzar, cambiar de elemento)', async () => {
    await js(`location.hash = '#/fotos'`);
    await until(`${q('#view .empty')} || ${q('#view .tile')}`, 'la galería');
    const before = (await api('/api/photos')).data.length;
    // Un video corto grabado aquí mismo (un lienzo animado) y una foto, para tener a dónde pasar.
    await js(`const c = document.createElement('canvas'); c.width = 360; c.height = 640; const g = c.getContext('2d');
      const rec = new MediaRecorder(c.captureStream(15), { mimeType: 'video/webm' }); const parts = []; rec.ondataavailable = (e) => parts.push(e.data);
      rec.start(); const t0 = performance.now();
      await new Promise((done) => { const draw = () => { const t = (performance.now() - t0) / 1000; g.fillStyle = '#090d16'; g.fillRect(0, 0, 360, 640);
        g.fillStyle = '#14b8a6'; g.fillRect(40 + t * 60, 200, 80, 240); if (t < 3) requestAnimationFrame(draw); else done(); }; draw(); });
      await new Promise((r) => { rec.onstop = r; rec.stop(); });
      g.fillStyle = '#b98aff'; g.fillRect(0, 0, 360, 640);
      const dt = new DataTransfer(); dt.items.add(new File(parts, 'e2e-video.webm', { type: 'video/webm' }));
      dt.items.add(new File([await new Promise((r) => c.toBlob(r, 'image/jpeg', 0.9))], 'e2e-foto3.jpg', { type: 'image/jpeg' }));
      const i = document.querySelector('#pick-media'); i.files = dt.files; i.dispatchEvent(new Event('change', { bubbles: true }));`);
    await until(`document.querySelectorAll('#view .tile').length === ${before + 2}`, 'el video y la foto en la galería', 90000);
    const list = (await api('/api/photos')).data;
    const at = list.findIndex((p) => p.name === 'e2e-video.webm');
    must(at >= 0 && list[at].kind === 'video', 'el video no quedó como video');
    await js(`document.querySelectorAll('#view .tile')[arg].click()`, at);
    await until(`${q('#viewer')}.open && ${q('#stage video')} && !${q('#v-ctl')}.hidden`, 'el visor con los controles del video');
    must(!(await js(`return document.querySelector('#stage video').controls`)), 'el video sigue con los controles del navegador');
    await until(`document.querySelector('#stage video').readyState >= 1`, 'los datos del video', 20000);
    // El video cabe entero entre la barra de arriba y sus controles: nada lo tapa.
    const box = await js(`const v = document.querySelector('#stage').getBoundingClientRect(), f = document.querySelector('.viewer-foot .v-ctl').getBoundingClientRect(), b = document.querySelector('.viewer-bar .icon-btn').getBoundingClientRect(); return { top: v.top, bottom: v.bottom, ctl: f.top, bar: b.bottom };`);
    must(box.bottom <= box.ctl + 1 && box.top >= box.bar - 1, `el video queda bajo las barras: ${JSON.stringify(box)}`);
    await shot('video-pausa');
    await js(`document.querySelector('#stage video').muted = true`); // sin gesto del usuario, el navegador solo deja reproducir en silencio
    await click('#v-play');
    await until(`!document.querySelector('#stage video').paused && document.querySelector('#stage video').currentTime > 0.3 && document.querySelector('#viewer').classList.contains('playing')`, 'que el video avance');
    must((await js(`return document.querySelector('#v-cur').textContent`)) !== '', 'no pinta el tiempo');
    await click('#stage video'); // un toque con las barras a la vista: pausa
    await until(`document.querySelector('#stage video').paused`, 'pausar con un toque');
    await js(`const s = document.querySelector('#v-seek'); s.value = 500; s.dispatchEvent(new Event('input', { bubbles: true }));`);
    must(await js(`const v = document.querySelector('#stage video'); return !isFinite(v.duration) || Math.abs(v.currentTime - v.duration / 2) < 0.6`), 'la barra de avance no movió el video');
    await shot('video-controles');
    // Cambiar de elemento desde un video (antes no se podía) y volver.
    const sub = await js(`return document.querySelector('#v-sub').textContent`);
    await click(at === 0 ? '#v-next' : '#v-prev');
    await until(`document.querySelector('#v-sub').textContent !== ${JSON.stringify(sub)}`, 'pasar del video a otro elemento');
    must(await js(`return document.querySelector('#v-ctl').hidden || !!document.querySelector('#stage video')`), 'los controles del video quedaron sobre una foto');
    await js(`document.querySelector('#viewer').close()`);
    for (const p of (await api('/api/photos')).data.filter((x) => x.name.startsWith('e2e-'))) await api('/api/photos/' + p.id, 'DELETE');
    must(!(await api('/api/photos')).data.some((x) => x.name.startsWith('e2e-')), 'quedaron archivos de prueba');
  });

  await step('archivos: crear carpeta, subir, descargar y borrar', async () => {
    await js(`location.hash = '#/archivos'`);
    await until(`!${q('#mkdir')}.hidden`, 'la vista de archivos');
    if (mobile) { // la barra y el menú de la cuenta son los mismos que en Música
      must(await js(`return getComputedStyle(document.querySelector('#me-btn')).display !== 'none' && getComputedStyle(document.querySelector('#logout')).display === 'none' && getComputedStyle(document.querySelector('#admin-link')).display === 'none'`), 'en Archivos falta el avatar o sobran los botones de arriba');
      await click('#me-btn');
      await until(`${q('#me')}.open`, 'el menú de la cuenta en Archivos');
      const items = await js(`return [...document.querySelectorAll('#me .menu-item')].map((b) => b.textContent)`);
      must(items.join() === 'Música,Administración,Cerrar sesión', `menú de la cuenta en Archivos: ${items}`);
      await shot('archivos-menu');
      await js(`document.querySelector('#me').close()`);
    }
    await shot('archivos-raiz');
    await click('#mkdir');
    await answer('e2e carpeta');
    const row = (name) => `[...document.querySelectorAll('#view .row-text p:first-child')].some((p) => p.textContent === ${JSON.stringify(name)})`;
    await until(row('e2e carpeta'), 'la carpeta nueva');
    await js(`location.hash = '#/archivos/' + encodeURIComponent('e2e carpeta')`);
    await until(q('#view .empty'), 'la carpeta vacía');
    const text = 'hola desde la prueba punta a punta: áéíóú ñ';
    await js(`const dt = new DataTransfer(); dt.items.add(new File([arg], 'nota e2e.txt', { type: 'text/plain' }));
      const i = document.querySelector('#pick-file'); i.files = dt.files; i.dispatchEvent(new Event('change', { bubbles: true }));`, text);
    await until(row('nota e2e.txt'), 'el archivo subido', 60000);
    await shot('archivos-carpeta');
    const got = await js(`const a = [...document.querySelectorAll('#view a.row')].find((a) => a.textContent.includes('nota e2e.txt')); const r = await fetch(a.href); return { s: r.status, body: await r.text() };`);
    must(got.s === 200 && got.body === text, `el archivo no volvió igual: ${got.s} ${got.body.slice(0, 40)}`);
    // Nada fuera de la carpeta del usuario: subir con ../ no sale de ella (muestra la raíz propia) y lo demás no existe.
    const root = (await api('/api/files?path=')).text;
    for (const p of ['../', '../../..']) must((await api('/api/files?path=' + encodeURIComponent(p))).text === root, `${p} no mostró la raíz del usuario`);
    for (const p of ['../../.palobox', '/etc']) {
      const r = await api('/api/files?path=' + encodeURIComponent(p));
      must(r.status >= 400, `listó ${p}: ${r.status} ${r.text}`);
    }
    must((await api('/api/files/raw?path=' + encodeURIComponent('../.palobox/db.sqlite'))).status >= 400, 'entregó un archivo fuera de la carpeta del usuario');
    await js(`[...document.querySelectorAll('#view .row-wrap')].find((li) => li.textContent.includes('nota e2e.txt')).querySelector('.icon-btn').click()`);
    await answer();
    await until(q('#view .empty'), 'que el archivo se vaya');
    await js(`location.hash = '#/archivos'`);
    await until(row('e2e carpeta'), 'la raíz');
    await js(`[...document.querySelectorAll('#view .row-wrap')].find((li) => li.textContent.includes('e2e carpeta')).querySelector('.icon-btn').click()`);
    await answer();
    await until(`!(${row('e2e carpeta')})`, 'que la carpeta se vaya');
  });

  let album, tracks3;
  await step('música: inicio, álbum, artista y búsqueda desde la interfaz', async () => {
    await js(`location.hash = '#/musica'`);
    await until(q('#view .quick-tile, #view .card'), 'el inicio de música');
    await shot('musica-inicio');
    must(await js(`return document.querySelectorAll('#mtabs a').length`) === 3, 'la barra inferior de Música debe tener solo Inicio, Buscar y Tu biblioteca');
    const a = await api('/api/music/artist?path=' + encodeURIComponent(seed.artistPath));
    must(a.status === 200 && a.data.tracks.length, `artista ${a.status}`);
    album = (await api('/api/music/album?path=' + encodeURIComponent(seed.albumPath))).data;
    must(album?.name && album.tracks.length, 'el álbum de la canción de prueba no abrió');
    tracks3 = (await api('/api/music/search?q=merengue')).data.tracks.slice(0, 3);
    must(tracks3.length === 3, 'la búsqueda de prueba trajo menos de 3 canciones');
    await js(`location.hash = '#/musica/album/' + arg`, album.path.split('/').map(encodeURIComponent).join('/'));
    await until(`${q('#view .hero h1')} && ${q('#view .tracks [data-track]')}`, 'la página del álbum');
    must(await js(`return document.querySelector('#view .hero h1').textContent`) === album.name, 'el título del álbum no coincide');
    await shot('musica-album');
    await js(`location.hash = '#/musica/artista/' + encodeURIComponent(arg)`, seed.artistPath);
    await until(`${q('#view .hero h1')} && ${q('#view .tracks [data-track]')}`, 'la página del artista');
    await shot('musica-artista');
    // Buscar escribiendo en el campo, como lo haría una persona.
    await js(`location.hash = '#/musica/buscar'`);
    await until(`!${q('#searchbox')}.hidden`, 'el buscador');
    await js(`const i = document.querySelector('#search'); i.focus(); i.value = arg; i.dispatchEvent(new Event('input', { bubbles: true }));`, seed.title);
    await until(`[...document.querySelectorAll('#view [data-track]')].some((r) => r.dataset.track === ${JSON.stringify(seed.path)})`, `«${seed.title}» en los resultados`);
    if (mobile) must(await js(`return document.body.classList.contains('searching') && getComputedStyle(document.querySelector('#title')).display === 'none' && getComputedStyle(document.querySelector('#search-back')).display !== 'none'`), 'el modo de búsqueda no se activó');
    await shot('musica-buscar');
    // Un enlace (o varios) se ofrece para descargar; en esta copia el servicio de descargas no está activado.
    await js(`const i = document.querySelector('#search'); i.value = arg; i.dispatchEvent(new Event('input', { bubbles: true }));`, 'https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC https://open.spotify.com/track/1ATL5GLyefJaxhQzSPVrLX');
    await until(q('#view .empty, #view .dl-card'), 'la respuesta al enlace pegado');
    const said = await js(`return document.querySelector('#view').textContent`);
    must(/no están activadas|Descargar 2 canciones/.test(said), `respuesta inesperada al pegar enlaces: ${said.slice(0, 120)}`);
    must(!/spotify|youtube/i.test(said), `la vista menciona a Spotify o YouTube: ${said.slice(0, 120)}`);
    await click(mobile ? '#search-back' : '#search-clear');
    await until(`document.querySelector('#search').value === ''`, 'que se borre la búsqueda');
    if (mobile) { await js(`document.querySelector('#search').blur()`); must(!(await js(`return document.body.classList.contains('searching')`)), 'el modo de búsqueda no se cerró'); }
  });

  await step('música: playlist a mano (crear, agregar, ordenar, renombrar, quitar, borrar)', async () => {
    const made = await api('/api/music/playlists', 'POST', { name: 'e2e manual' });
    must(made.status === 200 && made.data.id, `crear: ${made.status} ${made.text}`);
    const base = '/api/music/playlists/' + made.data.id;
    for (const t of tracks3) must((await api(base + '/tracks', 'POST', { path: t.path })).status === 200, 'agregar canción');
    must((await api(base + '/tracks', 'POST', { path: 'no/existe.mp3' })).status >= 400, 'aceptó una canción que no existe');
    const paths = async () => (await api(base)).data.tracks.map((t) => t.path);
    must((await paths()).join() === tracks3.map((t) => t.path).join(), 'las canciones no quedaron en el orden en que se agregaron');
    const flipped = tracks3.map((t) => t.path).reverse();
    must((await api(base + '/order', 'PUT', { paths: flipped })).status === 200, 'ordenar');
    must((await paths()).join() === flipped.join(), 'el orden nuevo no se guardó');
    must((await api(base, 'PATCH', { name: 'e2e renombrada' })).status === 200, 'renombrar');
    await js(`location.hash = '#/musica/playlist/' + arg`, made.data.id);
    await until(`${q('#view .hero h1')}?.textContent === 'e2e renombrada' && document.querySelectorAll('#view .tracks [data-track]').length === 3`, 'la playlist en pantalla');
    await shot('musica-playlist');
    // Mantener presionada una canción abre su menú (el celular manda contextmenu).
    await js(`document.querySelector('#view .tracks > li').dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))`);
    await until(`${q('#menu')}.open && ${q('#menu .menu-item')}`, 'el menú de la canción');
    await js(`document.querySelector('#menu').close()`);
    must((await api(base + '/tracks?path=' + encodeURIComponent(flipped[0]), 'DELETE')).status === 200, 'quitar canción');
    must((await paths()).length === 2, 'la canción no se quitó');
    // En Tu biblioteca aparece, y se borra.
    await js(`location.hash = '#/musica/biblioteca'`);
    await until(`[...document.querySelectorAll('#view .row-text p:first-child, #view .card p:first-of-type')].some((p) => p.textContent === 'e2e renombrada')`, 'la playlist en Tu biblioteca');
    await shot('musica-biblioteca');
    must((await api(base, 'DELETE')).status === 200, 'borrar');
    must((await api(base)).status === 404, 'la playlist borrada sigue ahí');
  });

  await step('música: guardar un álbum, seguir a un artista y me gusta', async () => {
    const saved = async () => { const d = (await api('/api/music?catalog=1')).data; return JSON.stringify(d); };
    for (const [kind, path] of [['album', album.path], ['artist', seed.artistPath]]) {
      const u = `/api/music/saved?kind=${kind}&path=${encodeURIComponent(path)}`;
      must((await api(u, 'PUT')).status === 200, `guardar ${kind}`);
      must((await api(`/api/music/saved?kind=${kind}&path=no-existe`, 'PUT')).status >= 400, `guardó un ${kind} que no existe`);
      must((await api(u, 'DELETE')).status === 200, `quitar ${kind}`);
    }
    must((await api('/api/music/saved?kind=otro&path=x', 'PUT')).status === 400, 'aceptó un tipo inválido');
    const like = '/api/music/likes?path=' + encodeURIComponent(seed.path);
    must((await api(like, 'PUT')).status === 200, 'me gusta');
    must((await api('/api/music/likes')).data.some((t) => t.path === seed.path), 'el me gusta no quedó');
    must((await api(like, 'DELETE')).status === 200, 'quitar me gusta');
    must(!(await api('/api/music/likes')).data.some((t) => t.path === seed.path), 'el me gusta sigue');
    must((await saved()).length > 100, 'la biblioteca no respondió');
  });

  await step('descargas: cada quien ve las suyas y la cola completa es del panel', async () => {
    // En esta copia el servicio no está activado: las dos listas responden vacías y deshabilitadas, sin error.
    for (const u of ['/api/spotdl', '/api/spotdl?all=1']) {
      const r = await api(u);
      must(r.status === 200 && Array.isArray(r.data.items), `${u}: ${r.status} ${r.text}`);
    }
    must((await api('/api/spotdl', 'POST', { url: 'https://example.com/track/4uLU6hMCjMI75M1A2tKUQC' })).status === 400, 'aceptó un enlace que no sirve');
  });

  await step('dispositivos y jam: el panel abre y un código falso no entra', async () => {
    await js(`location.hash = '#/musica'`);
    await until(q('#view .quick-tile, #view .card'), 'el inicio de música');
    if (mobile) {
      must(await js(`return getComputedStyle(document.querySelector('#me-btn')).display !== 'none' && getComputedStyle(document.querySelector('#logout')).display === 'none'`), 'falta el avatar o sobran los botones de arriba');
      await click('#me-btn');
      await until(`${q('#me')}.open && document.querySelectorAll('#me .menu-item').length >= 3`, 'el menú de la cuenta');
      await shot('menu-cuenta');
      await js(`[...document.querySelectorAll('#me .menu-item')].find((b) => b.textContent.includes('jam')).click()`);
    } else await click('#jam-link');
    await until(`${q('#jam')}.open && ${q('#jam-body .row')}`, 'el panel de dispositivos');
    must((await js(`return document.querySelector('#jam-body').textContent`)).includes('Este dispositivo'), 'el panel no muestra este dispositivo');
    must(await js(`return !!document.querySelector('#jam-body .jam-now') && !!document.querySelector('#jam-body .jam-card .btn.primary')`), 'faltan el encabezado de dónde suena o la tarjeta del jam');
    await shot('jam');
    // Iniciar un jam de verdad necesita algo sonando; si la app lo deja abrir sin eso, se revisa la tarjeta y se sale.
    await js(`[...document.querySelectorAll('#jam-body .jam-card .btn')].find((b) => b.textContent.includes('Iniciar')).click()`);
    await sleep(2500);
    if (await js(`return !!document.querySelector('#jam-body .jam-code')`)) {
      must(/^[A-Z0-9]{6}$/.test(await js(`return document.querySelector('#jam-body .jam-code').textContent`)), 'el código del jam no tiene 6 caracteres');
      await shot('jam-abierto');
      await js(`[...document.querySelectorAll('#jam-body .jam-card .btn')].find((b) => b.textContent.includes('Salir')).click()`);
      await until(`!document.querySelector('#jam-body .jam-code')`, 'salir del jam');
    }
    await click('#jam-close');
    const dev = await js(`return localStorage.getItem('palobox.device')`);
    const bad = await api('/api/jam', 'POST', { device: dev, op: 'join', code: 'ZZZZZZ' });
    must(bad.status >= 400, `entró a un jam con un código falso: ${bad.status} ${bad.text}`);
  });

  await step('administración: panel, secciones y géneros', async () => {
    const s = await api('/api/admin');
    must(s.status === 200 && s.data.users.some((u) => u.name === user && u.admin), `/api/admin ${s.status}`);
    for (const sec of ['', 'usuarios', 'actividad', 'musica', 'almacenamiento']) {
      await js(`location.hash = '#/admin' + (arg ? '/' + arg : '')`, sec);
      await until(`${q('#view .chip')} && !${q('#view .skeleton')}`, `la sección ${sec || 'resumen'}`);
      must(!(await js(`return !!document.querySelector('#view .empty .type[style*=danger]')`)), `la sección ${sec} mostró un error`);
    }
    // Actividad: sesiones plegadas por usuario; cerrar una ajena pide confirmación y cancelar no la cierra.
    await js(`location.hash = '#/admin/actividad'`);
    await until(q('#view details.acc'), 'las sesiones por usuario');
    must(!(await js(`return !!document.querySelector('#view details.acc[open]')`)), 'las sesiones debían llegar plegadas');
    const n0 = (await api('/api/admin')).data.sessions.length;
    const close = `document.querySelector('#view details.acc .row-wrap .icon-btn')`;
    const other = await js(`for (const d of document.querySelectorAll('#view details.acc')) d.open = true; return !!${close}`);
    await shot('admin-actividad');
    if (other) { // la copia trae sesiones de otras cuentas
      await js(`${close}.click()`);
      await until(`${q('#ask')}.open`, 'la confirmación para cerrar una sesión');
      must((await js(`return document.querySelector('#ask-title').textContent`)).startsWith('¿Cerrar la sesión de'), 'la confirmación no dice de quién es la sesión');
      await click('#ask-cancel');
      await sleep(400);
      must((await api('/api/admin')).data.sessions.length === n0, 'cancelar cerró la sesión de todos modos');
      await js(`${close}.click()`);
      await answer();
      await until(`(await (await fetch('/api/admin')).json()).sessions.length === ${n0 - 1}`, 'que la sesión confirmada se cierre');
    }
    await js(`[...document.querySelectorAll('#view .toolbar .chip')].find((c) => c.textContent.startsWith('Entradas')).click()`);
    await until(`${q('#view .list .row.static')} && !${q('#view details.acc')}`, 'las entradas recientes');
    await shot('admin-entradas');
    await js(`[...document.querySelectorAll('#view .toolbar .chip')].find((c) => c.textContent.startsWith('Sesiones')).click()`);
    await until(q('#view details.acc'), 'volver a las sesiones');
    await js(`location.hash = '#/admin/usuarios'`);
    await until(`[...document.querySelectorAll('#view .row-text p:first-child')].some((p) => p.textContent.includes(${JSON.stringify(user)}))`, 'la lista de usuarios');
    await shot('admin-usuarios');
  });

  await step('cuentas: invitar, entrar como invitado de solo música, limitar, desactivar y borrar', async () => {
    const name = 'e2einvitado';
    await api('/api/admin/users/' + name, 'DELETE'); // por si quedó de una corrida anterior
    const made = await api('/api/admin/users', 'POST', { name, musicOnly: true });
    must(made.status === 200 && /^[a-f0-9]{64}$/.test(made.data.token), `crear usuario: ${made.status} ${made.text}`);
    must((await api('/api/admin/users', 'POST', { name: 'Nombre Inválido' })).status >= 400, 'aceptó un nombre de usuario inválido');
    const token = made.data.token;
    must((await out('/api/invite?token=' + token)).data?.name === name, 'la invitación no reconoce al invitado');
    must((await out('/api/invite', 'POST', { token, pass: 'corta' })).status >= 400, 'aceptó una contraseña corta');
    const guestPass = 'clave-' + pass;
    const inv = await out('/api/invite', 'POST', { token, pass: guestPass });
    must(inv.status === 200 && inv.cookie, `aceptar invitación: ${inv.status} ${inv.text}`);
    must((await out('/api/invite?token=' + token)).status === 404, 'la invitación sirvió dos veces');
    const me = await out('/api/me', 'GET', null, inv.cookie);
    must(me.data?.name === name && me.data.musicOnly && !me.data.admin, `/api/me del invitado: ${me.text}`);
    // Solo música: entra a la música y a nada más.
    must((await out('/api/music', 'GET', null, inv.cookie)).status === 200, 'el invitado no ve la música');
    for (const u of ['/api/photos', '/api/files', '/api/admin', '/api/admin/genres', '/api/spotdl?all=1']) {
      const r = await out(u, 'GET', null, inv.cookie);
      must(u.startsWith('/api/spotdl') ? r.status === 200 : r.status === 403, `el invitado en ${u}: ${r.status}`);
    }
    must((await out('/api/admin/users', 'POST', { name: 'otro' }, inv.cookie)).status === 403, 'el invitado pudo crear usuarios');
    // Lo de una persona no lo toca otra.
    const mine = await api('/api/music/playlists', 'POST', { name: 'e2e privada' });
    for (const [m, b] of [['GET'], ['PATCH', { name: 'x' }], ['DELETE']]) must((await out('/api/music/playlists/' + mine.data.id, m, b, inv.cookie)).status === 404, `el invitado pudo ${m} una playlist ajena`);
    await api('/api/music/playlists/' + mine.data.id, 'DELETE');
    // Entrar con contraseña: la buena sí, la mala no.
    must((await out('/api/login', 'POST', { name, pass: 'no-es-la-clave' })).status === 401, 'entró con una contraseña equivocada');
    const login = await out('/api/login', 'POST', { name, pass: guestPass });
    must(login.status === 200 && login.cookie, `entrar con contraseña: ${login.status}`);
    // El administrador le da acceso completo, lo desactiva y lo borra.
    const path = '/api/admin/users/' + name;
    must((await api(path + '/access', 'PUT', { musicOnly: false })).status === 200, 'dar acceso completo');
    must((await out('/api/photos', 'GET', null, login.cookie)).status === 200, 'con acceso completo sigue sin ver Fotos');
    must((await api(path + '/disabled', 'PUT', { disabled: true })).status === 200, 'desactivar');
    must((await out('/api/me', 'GET', null, login.cookie)).status === 401, 'la sesión de una cuenta desactivada sigue viva');
    must((await out('/api/login', 'POST', { name, pass: guestPass })).status >= 400, 'una cuenta desactivada pudo entrar');
    must((await api(path + '?files=1', 'DELETE')).status === 200, 'borrar usuario');
    must(!(await api('/api/admin')).data.users.some((u) => u.name === name), 'el usuario borrado sigue en la lista');
    must((await api('/api/admin/users/' + user, 'DELETE')).status >= 400, 'el administrador pudo borrarse a sí mismo');
  });

  await step('sin sesión no se entra a nada', async () => {
    for (const u of ['/api/me', '/api/music', '/api/photos', '/api/files', '/api/admin', '/api/spotdl', '/api/music/offline?path=' + encodeURIComponent(seed.path), '/api/music/likes']) {
      must((await out(u)).status === 401, `${u} respondió sin sesión`);
    }
    must((await out('/api/version')).status === 200, '/api/version debe ser pública');
    const h = (await fetch(base)).headers; // cabeceras de seguridad en cada respuesta
    must(h.get('x-frame-options') === 'DENY' && /frame-ancestors 'none'/.test(h.get('content-security-policy') || '') && h.get('x-content-type-options') === 'nosniff' && h.get('referrer-policy') === 'same-origin', 'faltan cabeceras de seguridad');
    must((await fetch(base, { headers: { 'X-Forwarded-Proto': 'https' } })).headers.get('strict-transport-security'), 'por HTTPS falta la cabecera HSTS');
  });

  await step('sin errores en la consola del navegador', async () => {
    // Las respuestas 4xx que estas pruebas provocan a propósito también salen en la consola: no cuentan.
    const real = errors.filter((e) => !/Failed to load resource/.test(e));
    must(real.length === 0, real.join(' | '));
  });
  await step('cerrar sesión desde la interfaz', async () => {
    await js(`location.hash = '#/musica'`);
    await until(q('#view .quick-tile, #view .card'), 'el inicio de música');
    if (mobile) {
      await click('#me-btn');
      await until(`${q('#me')}.open`, 'el menú de la cuenta');
      await js(`[...document.querySelectorAll('#me .menu-item')].find((b) => b.textContent.includes('Cerrar sesión')).click()`);
    } else await click('#logout');
    await answer();
    await until(`!${q('#login')}.hidden && ${q('#app')}.hidden`, 'la pantalla de entrada');
    await shot('entrada');
    must((await api('/api/me')).status === 401, 'la sesión siguió abierta');
  });
} catch (e) {
  failed++;
  console.log('  FALLA (preparación):', e.message);
}
ws.close();
if (cdp) await fetch(`${cdp}/json/close/${tab.id}`).catch(() => {});
else {
  chrome.kill();
  await new Promise((r) => chrome.on('exit', r)); // el perfil no se puede borrar mientras Chromium sigue cerrando
}
try { rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }); } catch {}
console.log(failed ? `\n${failed} pruebas fallaron.` : '\nTodas las pruebas punta a punta pasaron.');
process.exit(failed ? 1 : 0);
