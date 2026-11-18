// Sesión: peticiones autenticadas, entrada y salida.
import { $, json, ask } from './lib.js';
import { render } from './nav.js';
import { restore, reset } from './player.js';
import { forget } from './music.js';

export async function api(path, opts) {
  const r = await fetch(path, opts);
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
  if (!(await ask({ heading: '¿Cerrar sesión?', ok: 'Cerrar sesión', danger: true }))) return;
  await fetch('/api/logout', { method: 'POST' });
  showLogin();
};

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
  if (!r?.ok) return showLogin();
  const me = await r.json();
  isAdmin = me.admin;
  musicOnly = !!me.musicOnly;
  document.body.classList.toggle('music-only', musicOnly);
  userName = me.name;
  restore(me.name);
  $('#login').hidden = true;
  $('#app').hidden = false;
  render();
}
