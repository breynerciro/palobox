// Subidas reanudables de fotos y archivos.
import { $, sleep, json, plural, toast } from './lib.js';
import { route, render } from './nav.js';
import { api } from './session.js';
import * as offline from './offline.js';

// Máximo 8 peticiones a la vez entre todos los archivos: varios trozos de un video pesado viajan
// en paralelo y mientras el servidor cierra un archivo los demás siguen enviando. La app solo se
// sirve por HTTPS (tailscale serve, HTTP/2), sin el tope de 6 conexiones de HTTP/1.1; no más de 8
// para no acaparar el ancho de subida del celular frente a la interfaz y la música.
const PARALLEL = 8;
let free = PARALLEL;
const queue = [];
async function slot(fn) {
  if (free) free--;
  else await new Promise((r) => queue.push(r));
  try { return await fn(); } finally { const next = queue.shift(); next ? next() : free++; }
}

// CRC32 (IEEE, el mismo de hash/crc32 en Go) de cada trozo: el servidor rechaza el que llegue distinto.
const CRC = Array.from({ length: 256 }, (_, n) => { for (let k = 0; k < 8; k++) n = n & 1 ? 0xedb88320 ^ (n >>> 1) : n >>> 1; return n >>> 0; });
function crc32(buf) {
  let c = -1;
  for (let i = 0; i < buf.length; i++) c = CRC[(c ^ buf[i]) & 255] ^ (c >>> 8);
  return ((c ^ -1) >>> 0).toString(16).padStart(8, '0');
}

async function uploadOne(file, kind, dir, onProgress) {
  const r = await api('/api/uploads', json('POST', { name: file.name, size: file.size, mtime: file.lastModified, kind, dir }));
  if (!r.ok) throw new Error(await r.text());
  const { id, chunk, missing, done, dup } = await r.json();
  if (done) return dup;
  let sent = file.size - missing.reduce((s, i) => s + Math.min(chunk, file.size - i * chunk), 0);
  onProgress(sent / file.size);
  // allSettled: si un trozo falla se espera a los demás, para que el reintento no los mande dos veces a la vez.
  const res = await Promise.allSettled(missing.map((i) => slot(async () => {
    // Se lee una vez: la huella y el envío usan los mismos bytes.
    const part = new Uint8Array(await file.slice(i * chunk, (i + 1) * chunk).arrayBuffer());
    const res = await api(`/api/uploads/${id}?chunk=${i}`, { method: 'PATCH', body: part, headers: { 'X-Chunk-CRC32': crc32(part) } });
    if (!res.ok) throw new Error(await res.text());
    sent += part.length;
    onProgress(sent / file.size);
    return res.json();
  })));
  const bad = res.find((x) => x.status === 'rejected');
  if (bad) throw bad.reason; // el reintento pide de nuevo al servidor qué trozos faltan
  const fin = res.find((x) => x.value.done);
  if (!fin) throw new Error('la subida no se cerró');
  return fin.value.dup === true;
}

// Píldora de progreso: no tapa la vista y se puede minimizar mientras sigue subiendo.
let minimized = false;
function paintBar(done, n, p, note) {
  const pct = Math.round(p * 100);
  const text = note || `${done} de ${n} · ${pct} %`;
  const full = $('#upbar-full');
  $('#upbar').hidden = false;
  full.hidden = minimized;
  $('#upbar-show').hidden = !minimized;
  $('#upbar-text').textContent = text;
  full.setAttribute('aria-valuenow', pct);
  full.setAttribute('aria-valuetext', note || `Subiendo: ${done} de ${n} listos, ${pct} %`);
  full.querySelector('.upbar-line span').style.width = `${pct}%`;
  $('#upbar-pct').textContent = `${pct} %`;
}
function minimize(on) {
  minimized = on;
  $('#upbar-full').hidden = on;
  $('#upbar-show').hidden = !on;
  $(on ? '#upbar-show' : '#upbar-hide').focus();
}
$('#upbar-hide').onclick = () => minimize(true);
$('#upbar-show').onclick = () => minimize(false);

export let uploading = false;
async function upload(files, kind, dir) {
  if (!files.length) return;
  if (uploading) return void toast('Espera a que termine la subida en curso.', { kind: 'warn' });
  uploading = true;
  minimized = false;
  // Sin esto el celular apaga la pantalla y suspende la subida.
  const lock = await navigator.wakeLock?.request('screen').catch(() => null);
  const total = files.reduce((s, f) => s + f.size, 0) || 1;
  const sent = files.map(() => 0); // bytes ya recibidos de cada archivo
  let done = 0;
  let dups = 0;
  let next = 0;
  const failed = [];
  const paint = (note) => paintBar(done, files.length, sent.reduce((a, b) => a + b, 0) / total, note);
  paint();
  const worker = async () => {
    while (next < files.length) {
      const i = next++;
      const f = files[i];
      for (let tries = 1; ; tries++) {
        try {
          if (await uploadOne(f, kind, dir, (p) => { sent[i] = p * f.size; paint(); })) dups++;
          break;
        } catch (e) {
          if (offline.active) { // se cayó la red: la subida espera y sigue donde iba (el servidor sabe qué trozos faltan)
            paint('Sin conexión: la subida sigue cuando vuelva la red…');
            while (offline.active) await sleep(3000);
            tries--;
            continue;
          }
          if (tries === 3) { failed.push(f.name); break; }
          paint(`Conexión inestable, reintentando ${f.name}…`);
          await sleep(2000); // al reintentar, el servidor dice qué trozos faltan
        }
      }
      sent[i] = f.size;
      done++;
      paint();
    }
  };
  await Promise.all(Array.from({ length: Math.min(PARALLEL, files.length) }, worker));
  lock?.release();
  uploading = false;
  $('#upbar').hidden = true;

  const ok = files.length - dups - failed.length;
  let msg = `${plural(ok, 'archivo subido', 'archivos subidos')}.`;
  if (dups) msg += ` ${plural(dups, 'ya estaba', 'ya estaban')} en el servidor.`;
  if (failed.length) msg += ` Falló: ${failed.join(', ')}. Vuelve a elegirlos para reintentar.`;
  // Las fotos de cámara originales se llaman IMG_0001.HEIC o .JPG; ".jpeg" en minúscula es la conversión de iOS.
  const converted = kind === 'photo' && files.some((f) => /^IMG_\d+\.jpeg$/.test(f.name));
  if (converted) msg += ' Tu iPhone convirtió las fotos a JPG. Para subir originales: en el selector toca Opciones → Formato → Actual.';
  toast(msg, { kind: failed.length || converted ? 'warn' : 'ok', sticky: failed.length > 0 || converted });
  render();
}

$('#add').onclick = () => $(route().tab === 'fotos' ? '#pick-media' : '#pick-file').click();
$('#pick-media').onchange = (e) => { upload([...e.target.files], 'photo', ''); e.target.value = ''; };
$('#pick-file').onchange = (e) => { upload([...e.target.files], 'file', route().path); e.target.value = ''; };
