// Sesión: peticiones autenticadas, entrada y salida.
import { $, json, ask } from './lib.js';
import { render } from './nav.js';
import { restore, reset, goOffline, audio } from './player.js';
import { forget, resumeDownloads } from './music.js';
import * as offline from './offline.js';

// reachable: ¿el servidor contesta? Una sola pregunta para decidir entrar o salir del modo sin conexión.
const reachable = () => fetch('/api/version', { cache: 'no-store', signal: AbortSignal.timeout(5000) }).then((r) => r.ok, () => false);

// Sin conexión (offline.js) las peticiones no van a la red: se responden con lo descargado en el celular.
export async function api(path, opts) {
  if (offline.active) return offline.handle(path, opts);
  let r;
  try {
    r = await fetch(path, opts);
  } catch (e) {
    // La red se cayó con la app abierta: se sigue con lo descargado. Una petición cancelada (el buscador al
    // teclear) o un tropiezo suelto de la red no cuentan: se confirma que el servidor de verdad no contesta.
    if (e.name === 'AbortError' || offline.saved()?.name !== userName || (navigator.onLine && await reachable())) throw e;
    enterOffline();
    setTimeout(render);
    return offline.handle(path, opts);
  }
  if (r.status === 401 && path !== '/api/login') {
    await offline.wipe(); // el servidor ya no reconoce la sesión: ver start()
    showLogin();
    throw new Error('Tu sesión venció. Entra de nuevo.');
  }
  return r;
}

function showLogin() {
  reset();
  forget();
  $('#view').replaceChildren(); // lo del usuario anterior no debe verse mientras carga el siguiente
  $('#app').hidden = true;
  $('#login').hidden = false;
  $('#u').focus();
}

$('#login-form').onsubmit = async (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  const err = $('#login-error');
  if (!f.get('name') || !f.get('pass')) return (err.textContent = 'Escribe tu usuario y contraseña.');
  const btn = $('#login-btn');
  btn.disabled = true;
  btn.textContent = 'Entrando…';
  try {
    const r = await fetch('/api/login', json('POST', { name: f.get('name').trim().toLowerCase(), pass: f.get('pass') }));
    if (!r.ok) {
      err.textContent = r.status === 401 ? 'Usuario o contraseña incorrectos.' : 'No se pudo entrar. Intenta de nuevo.';
      return;
    }
    e.target.reset();
    err.textContent = '';
    start();
  } catch {
    err.textContent = 'Sin conexión con el servidor.';
  } finally {
    btn.disabled = false;
    btn.textContent = 'Entrar';
  }
};

$('#logout').onclick = async () => {
  const n = offline.count();
  const text = offline.active ? 'Estás sin conexión: al salir se borra la música descargada y no podrás entrar hasta volver a conectarte.'
    : n ? 'También se borra de este celular la música descargada.' : '';
  if (!(await ask({ heading: '¿Cerrar sesión?', text, ok: 'Cerrar sesión', danger: true }))) return;
  // Sin red la sesión no se puede cerrar en el servidor: queda anotado y se cierra al volver la conexión (start).
  const out = await fetch('/api/logout', { method: 'POST' }).catch(() => null);
  try { if (!out?.ok) localStorage.setItem(LEAVING, '1'); } catch {}
  await offline.wipe(); // las descargas son de quien inició sesión: no le quedan al siguiente
  if (offline.active) return location.reload();
  showLogin();
};

// enterOffline pasa la app a funcionar con lo descargado y vigila cuándo vuelve la conexión.
function enterOffline() {
  if (offline.active) return;
  offline.enter();
  goOffline();
  const back = async () => {
    if (document.visibilityState !== 'visible') return;
    // Volvió el servidor: la app arranca de nuevo, ya con todo. Si está sonando se espera a la pausa para no cortar.
    if (audio.paused && await reachable()) location.reload();
  };
  setInterval(back, 20000);
  audio.addEventListener('pause', back); // se esperaba a que dejara de sonar
  document.addEventListener('visibilitychange', back);
  window.addEventListener('online', back);
}

// watch detecta sola la caída de la conexión en las cuentas que pueden descargar, sin esperar a que falle un toque.
let watching = false;
function watch() {
  if (watching || !offline.enabled()) return;
  watching = true;
  let misses = 0;
  const drop = () => { if (!offline.active && offline.saved()) { enterOffline(); render(); } };
  window.addEventListener('offline', drop);
  setInterval(async () => {
    if (offline.active || document.visibilityState !== 'visible') return;
    misses = (await reachable()) ? 0 : misses + 1;
    if (misses >= 2) drop(); // dos seguidas: un tropiezo de la red móvil no cambia de modo
  }, 10000);
}

const LEAVING = 'palobox.salir'; // se cerró sesión sin conexión: falta cerrarla en el servidor
export let isAdmin = false;
export let musicOnly = false; // cuenta de invitado: solo ve Música
export let userName = ''; // dueño de lo que se recuerda en este navegador (búsquedas recientes)

// Invitación: el administrador crea la cuenta sin contraseña y comparte un enlace de un solo uso; quien lo abre
// define su contraseña aquí y queda dentro. El mismo enlace sirve para una contraseña olvidada.
const inviteToken = () => location.hash.match(/^#\/invitacion\/([a-f0-9]{64})$/)?.[1];
function leaveInvite() {
  history.replaceState(null, '', '#/musica');
  $('#invite').hidden = true;
  start();
}
async function showInvite(token) {
  reset();
  forget();
  $('#view').replaceChildren();
  $('#app').hidden = $('#login').hidden = true;
  $('#invite').hidden = false;
  const form = $('#invite-form');
  const err = $('#invite-error');
  form.hidden = true;
  form.reset();
  err.textContent = '';
  $('#invite-login').hidden = true;
  $('#invite-text').textContent = 'Revisando el enlace…';
  const r = await fetch(`/api/invite?token=${token}`).catch(() => null);
  if (!r?.ok) {
    $('#invite-text').textContent = r ? await r.text() : 'Sin conexión con el servidor. Revisa tu conexión y vuelve a abrir el enlace.';
    $('#invite-login').hidden = false;
    return;
  }
  const { name } = await r.json();
  $('#invite-user').value = name; // para que el gestor de contraseñas guarde la clave con este usuario
  $('#invite-text').textContent = `Hola, ${name}. Elige la contraseña con la que vas a entrar a PaloBox: mínimo 8 caracteres.`;
  form.hidden = false;
  $('#ip1').focus();
  form.onsubmit = async (e) => {
    e.preventDefault();
    const pass = $('#ip1').value;
    if (pass.length < 8) return (err.textContent = 'La contraseña debe tener al menos 8 caracteres.');
    if (pass !== $('#ip2').value) return (err.textContent = 'Las dos contraseñas no coinciden.');
    const btn = $('#invite-btn');
    btn.disabled = true;
    try {
      const res = await fetch('/api/invite', json('POST', { token, pass }));
      if (!res.ok) return (err.textContent = await res.text());
      leaveInvite();
    } catch {
      err.textContent = 'Sin conexión con el servidor.';
    } finally {
      btn.disabled = false;
    }
  };
}
$('#invite-login').onclick = leaveInvite;
window.addEventListener('hashchange', () => inviteToken() && start()); // el enlace pegado con la app ya abierta

export async function start() {
  const token = inviteToken();
  if (token) return showInvite(token);
  try {
    if (localStorage.getItem(LEAVING) && (await fetch('/api/logout', { method: 'POST', signal: AbortSignal.timeout(6000) })).ok) localStorage.removeItem(LEAVING);
  } catch {}
  const r = await fetch('/api/me', { signal: AbortSignal.timeout(6000) }).catch(() => null); // sin servidor no se queda esperando
  let me = offline.saved();
  if (r?.ok) {
    me = await r.json();
    await offline.login(me);
    // Quien puede descargar necesita que la app abra sin servidor; a los demás no se les instala nada.
    if (offline.enabled() && 'serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
  } else if ((r && r.status < 500) || !me) {
    // El servidor dijo que no hay sesión, o no hay red ni cuenta vigente en este celular. Sin sesión no queda
    // música descargada: la cuenta se eliminó, el administrador la cerró o pasó demasiado tiempo sin conectarse.
    if (r?.status === 401 || !r) await offline.wipe();
    return showLogin();
  }
  else enterOffline(); // sin red: entra quien usó este celular, con lo que tenga descargado
  isAdmin = me.admin;
  musicOnly = !!me.musicOnly;
  document.body.classList.toggle('music-only', musicOnly);
  userName = me.name;
  restore(me.name);
  watch();
  $('#login').hidden = true;
  $('#app').hidden = false;
  render();
  resumeDownloads(); // lo que quedó a medio descargar la última vez
}
