// Utilidades de interfaz sin estado de la app: nodos, formato de textos, avisos y diálogos.

// Migración única de las preferencias del nombre anterior (ciro.*). lib.js es el primer módulo que corre.
// shortcut: se puede borrar cuando ningún navegador tenga claves ciro.* guardadas.
try {
  for (const k of Object.keys(localStorage)) {
    if (!k.startsWith('ciro.')) continue;
    const nk = 'palobox.' + k.slice(5);
    if (localStorage.getItem(nk) === null) localStorage.setItem(nk, localStorage.getItem(k));
    localStorage.removeItem(k);
  }
} catch {}

export const $ = (s) => document.querySelector(s);
export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// el('div', {className: 'x'}, hijo1, hijo2...): crea nodos sin innerHTML (los nombres vienen del usuario).
export function el(tag, props = {}, ...kids) {
  const n = Object.assign(document.createElement(tag), props);
  n.append(...kids.flat().filter((k) => k != null && k !== false));
  return n;
}
export function icon(name, cls) {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  s.setAttribute('aria-hidden', 'true');
  if (cls) s.setAttribute('class', cls);
  const u = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  u.setAttribute('href', '#i-' + name);
  s.append(u);
  return s;
}
export const setIcon = (btn, name) => btn.querySelector('use').setAttribute('href', '#i-' + name);

export const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

export function size(n) {
  for (const u of ['B', 'KB', 'MB', 'GB', 'TB']) {
    if (n < 1024 || u === 'TB') return `${n < 10 && u !== 'B' ? n.toFixed(1).replace('.', ',') : Math.round(n)} ${u}`;
    n /= 1024;
  }
}
export const fmtDate = (ms) => new Date(ms).toLocaleDateString('es-CO', { day: 'numeric', month: 'short', year: 'numeric' });
export const plural = (n, one, many) => `${n.toLocaleString('es-CO')} ${n === 1 ? one : many}`;
// clock: 3:07 para una pista; fmtLong: «1 h 12 min» para un álbum entero.
export const clock = (s) => (isFinite(s) ? `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, '0')}` : '0:00');
export const fmtLong = (s) => (s >= 3600 ? `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min` : `${Math.max(1, Math.round(s / 60))} min`);
export const join = (...parts) => parts.filter(Boolean).join('/');
export const joinText = (...parts) => parts.filter(Boolean).join(' · ');

// Imagen con aparición suave; si no carga, se queda el ícono de respaldo que hay debajo.
export function lazyImg(src, alt = '') {
  const img = el('img', { src, alt, loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer' });
  img.onload = () => img.classList.add('ok');
  img.onerror = () => img.remove();
  return img;
}

const EXT = {
  image: /\.(jpe?g|png|heic|heif|gif|webp|avif|dng|tiff?)$/i,
  video: /\.(mov|mp4|m4v|webm|mkv|avi|3gp)$/i,
  audio: /\.(mp3|m4a|aac|flac|wav|ogg|opus)$/i,
  doc: /\.(pdf|docx?|xlsx?|pptx?|txt|md|csv|odt|ods|pages|numbers|key)$/i,
};
export function fileType(name) {
  for (const [t, re] of Object.entries(EXT)) if (re.test(name)) return t;
  return 'other';
}
const TYPE_ICON = { folder: 'folder', image: 'image', video: 'film', audio: 'music', doc: 'file-text', other: 'file' };
export function typeBadge(t) {
  return el('span', { className: 'type', style: `--tc: ${t === 'other' ? 'var(--muted)' : `var(--t-${t})`}` }, icon(TYPE_ICON[t]));
}

// Avisos (éxito y errores; el progreso de subida tiene su propia píldora en uploads.js)

let toastTimer;
export function toast(text, { kind = '', sticky = false } = {}) {
  clearTimeout(toastTimer);
  const t = $('#toast');
  t.hidden = false;
  t.className = kind;
  setIcon(t.querySelector('.toast-icon'), kind === 'ok' ? 'check' : kind === 'warn' ? 'alert' : 'upload');
  $('#toast-text').textContent = text;
  if (!sticky) toastTimer = setTimeout(() => (t.hidden = true), 5000);
}
$('#toast-close').onclick = () => ($('#toast').hidden = true);

// Diálogo de confirmación o nombre (reemplaza confirm/prompt del navegador)

// input: valor inicial del campo (null = sin campo); label y type para pedir algo distinto de un nombre (p. ej. una contraseña).
export function ask({ heading, text = '', input = null, ok = 'Aceptar', danger = false, cancel = 'Cancelar', label = 'Nombre', type = 'text' }) {
  const d = $('#ask');
  $('#ask-title').textContent = heading;
  $('#ask-text').textContent = text;
  $('#ask-text').hidden = !text;
  const inp = $('#ask-input');
  inp.hidden = input == null;
  inp.value = input ?? '';
  inp.placeholder = input != null ? label : '';
  inp.type = type;
  inp.autocomplete = type === 'password' ? 'new-password' : 'off';
  $('#ask-label').textContent = label;
  $('#ask-cancel').textContent = cancel;
  $('#ask-ok').textContent = ok;
  $('#ask-ok').className = `btn ${danger ? 'danger' : 'primary'}`;
  d.returnValue = '';
  d.showModal();
  if (input != null) inp.focus();
  return new Promise((resolve) => {
    d.onclose = () => resolve(d.returnValue === 'ok' ? (input != null ? inp.value.trim() : true) : null);
  });
}
// Enter en el campo acepta: el envío implícito del formulario usaría su primer botón, que es Cancelar.
$('#ask-input').onkeydown = (e) => {
  if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); $('#ask-ok').click(); }
};

// Menú de acciones anclado a un botón. Es un <dialog> modal: Escape lo cierra y el foco vuelve al botón.
export function menu(anchor, label, items) {
  const d = $('#menu');
  if (d.open) d.close();
  d.setAttribute('aria-label', label);
  $('#menu-list').replaceChildren(...items.filter(Boolean).map((it) => {
    const b = el('button', { className: `menu-item${it.danger ? ' danger' : ''}` }, icon(it.icon), it.label);
    b.setAttribute('role', 'menuitem');
    b.onclick = () => { d.close(); it.run(); };
    const li = el('li', {}, b);
    li.setAttribute('role', 'none');
    return li;
  }));
  d.showModal();
  // En escritorio se abre junto al botón; en celular es una hoja inferior (style.css).
  const wide = matchMedia('(min-width: 900px)').matches;
  const r = anchor.getBoundingClientRect();
  d.style.left = wide ? `${Math.max(8, Math.min(r.right - d.offsetWidth, innerWidth - d.offsetWidth - 8))}px` : '';
  d.style.top = wide ? `${Math.max(8, Math.min(r.bottom + 4, innerHeight - d.offsetHeight - 8))}px` : '';
}
$('#menu').onclick = (e) => e.target === e.currentTarget && e.currentTarget.close(); // clic en el telón
$('#menu').onkeydown = (e) => {
  if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
  e.preventDefault();
  const items = [...e.currentTarget.querySelectorAll('.menu-item')];
  items.at((items.indexOf(document.activeElement) + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length).focus();
};

export function empty(iconName, heading, text, action) {
  return el('div', { className: 'empty' },
    el('span', { className: 'type', style: '--tc: var(--accent)' }, icon(iconName)),
    el('h2', { textContent: heading }),
    typeof text === 'string' ? el('p', { className: 'muted', textContent: text }) : text,
    action);
}
export const uploadButton = () => {
  const b = el('button', { className: 'btn primary' }, icon('upload'), 'Subir');
  b.onclick = () => $('#add').click();
  return b;
};

// device resume el navegador y el sistema a partir del User-Agent: «Chrome en Android».
export function deviceName(ua) {
  if (!ua) return 'Dispositivo sin registrar'; // sesiones abiertas antes de que se guardara el navegador
  const os = [[/iPhone/, 'iPhone'], [/iPad/, 'iPad'], [/Android/, 'Android'], [/Windows/, 'Windows'], [/Mac OS X/, 'Mac'], [/CrOS/, 'ChromeOS'], [/Linux/, 'Linux']].find(([re]) => re.test(ua))?.[1];
  const br = [[/Edg\//, 'Edge'], [/OPR\//, 'Opera'], [/Firefox\/|FxiOS/, 'Firefox'], [/Chrome\/|CriOS/, 'Chrome'], [/Safari\//, 'Safari'], [/curl|Go-http|python/i, 'Script']].find(([re]) => re.test(ua))?.[1];
  return os ? `${br ?? 'Navegador'} en ${os}` : br ?? 'Dispositivo desconocido';
}
