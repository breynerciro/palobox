// Conexión con las salas de reproducción del servidor (/api/jam): un flujo de eventos trae el estado
// (mis dispositivos y, si hay sala, su cola y posición) y las acciones se mandan como comandos.
// player.js decide qué hacer con cada foto; aquí solo vive el transporte.
import { json, toast, deviceName } from './lib.js';
import { api } from './session.js';

// Identidad de este navegador entre mis dispositivos.
export const deviceId = (() => {
  const fresh = () => Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) => b.toString(16).padStart(2, '0')).join('');
  try {
    let id = localStorage.getItem('palobox.device');
    if (!/^[a-z0-9]{8,32}$/.test(id ?? '')) localStorage.setItem('palobox.device', (id = fresh()));
    return id;
  } catch { return fresh(); } // modo privado: vale para esta visita
})();

let source = null;
let handler = () => {};
let lastSeq = 0; // las fotos llegan por dos caminos (flujo y respuesta a un comando): se descarta la que llega vieja
let cache = { rev: 0, queue: [] }; // la cola solo viaja cuando cambia

function take(s) {
  if (s.seq <= lastSeq) return;
  lastSeq = s.seq;
  if (s.room) {
    if (s.room.queue) {
      for (const i of s.room.auto || []) if (s.room.queue[i]) s.room.queue[i].auto = true; // sugerencias del servidor
      cache = { rev: s.room.qrev, queue: s.room.queue };
    }
    s.room.queue = cache.queue;
  } else cache = { rev: 0, queue: [] };
  handler(s);
}

export function connect(onState) {
  close();
  handler = onState;
  source = new EventSource(`/api/jam/events?device=${deviceId}&name=${encodeURIComponent(deviceName(navigator.userAgent))}`);
  source.onopen = () => {
    lastSeq = 0; // tras reconectar (o reiniciarse el servidor) la numeración empieza de nuevo
    // La app de Android deja su token de push: dormida sigue en la lista y un aviso la despierta (internal/jam/sleep.go).
    const token = window.PaloTv?.token?.();
    if (token) send('push', { token }, true);
  };
  source.onmessage = (e) => take(JSON.parse(e.data));
}

export const alive = () => !!source && source.readyState !== EventSource.CLOSED;

export function close() {
  source?.close();
  source = null;
  lastSeq = 0;
  cache = { rev: 0, queue: [] };
}

// send manda una acción y aplica la foto que vuelve; devuelve esa foto, o false si falló.
// quiet: para lo que el reproductor hace solo (preguntar el estado en segundo plano), sin avisos si la red falla.
export async function send(op, data = {}, quiet = false) {
  const r = await api('/api/jam', json('POST', { device: deviceId, op, qrev: cache.rev, ...data })).catch(() => null);
  if (!r?.ok) {
    if (!quiet) toast(r ? await r.text() : 'Sin conexión con el servidor.', { kind: 'warn' });
    return false;
  }
  const s = await r.json();
  take(s);
  return s;
}
