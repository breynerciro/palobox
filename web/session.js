// Sesión: peticiones autenticadas, entrada y salida.
import { $, json, ask, toast } from './lib.js';
import { render } from './nav.js';
import { restore, reset, goOffline } from './player.js';
import { forget, resumeDownloads } from './music.js';
import * as offline from './offline.js';

// Sin conexión (offline.js) las peticiones no van a la red: se responden con lo descargado en el celular.
export async function api(path, opts) {
  if (offline.active) return offline.handle(path, opts);
  let r;
  try {
    r = await fetch(path, opts);
  } catch (e) {
    // La red se cayó con la app abierta: si hay música descargada, se sigue con ella.
    if (!offline.saved() || offline.saved().name !== userName) throw e;
    enterOffline();
    setTimeout(render);
    return offline.handle(path, opts);
  }
  if (r.status === 401 && path !== '/api/login') {
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
  await fetch('/api/logout', { method: 'POST' }).catch(() => {});
  await offline.wipe(); // las descargas son de quien inició sesión: no le quedan al siguiente
  if (offline.active) return location.reload();
  showLogin();
};

// enterOffline pasa la app a funcionar con lo descargado y vigila cuándo vuelve la conexión.
function enterOffline() {
  if (offline.active) return;
  offline.enter();
  goOffline();
  toast('Sin conexión: se muestra tu música descargada.', { kind: 'warn' });
  const back = async () => {
    if (document.visibilityState !== 'visible') return;
    const r = await fetch('/api/version', { cache: 'no-store' }).catch(() => null);
    if (r?.ok) location.reload(); // volvió el servidor: la app arranca de nuevo, ya con todo
  };
  setInterval(back, 20000);
  document.addEventListener('visibilitychange', back);
  window.addEventListener('online', back);
}

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
  const r = await fetch('/api/me').catch(() => null);
  let me = offline.saved();
  if (r?.ok) {
    me = await r.json();
    await offline.login(me);
    // Quien puede descargar necesita que la app abra sin servidor; a los demás no se les instala nada.
    if (offline.enabled() && 'serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
  } else if (r || !me) return showLogin(); // el servidor respondió que no hay sesión, o no hay red ni nada descargado
  else enterOffline(); // sin red, pero con música descargada: entra quien la descargó
  isAdmin = me.admin;
  musicOnly = !!me.musicOnly;
  document.body.classList.toggle('music-only', musicOnly);
  userName = me.name;
  restore(me.name);
  $('#login').hidden = true;
  $('#app').hidden = false;
  render();
  resumeDownloads(); // lo que quedó a medio descargar la última vez
}
