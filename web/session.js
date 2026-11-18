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

export async function start() {
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
