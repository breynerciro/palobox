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
// action: { label, run } muestra un botón en el aviso (p. ej. «Actualizar»).
export function toast(text, { kind = '', sticky = false, action = null } = {}) {
  clearTimeout(toastTimer);
  const t = $('#toast');
  t.hidden = false;
  t.className = kind;
  setIcon(t.querySelector('.toast-icon'), kind === 'ok' ? 'check' : kind === 'warn' ? 'alert' : 'upload');
  $('#toast-text').textContent = text;
  const act = $('#toast-act');
  act.hidden = !action;
  act.textContent = action?.label ?? '';
  act.onclick = action?.run ?? null;
  if (!sticky) toastTimer = setTimeout(() => (t.hidden = true), 5000);
}
$('#toast-close').onclick = () => ($('#toast').hidden = true);

// Diálogo de confirmación o nombre (reemplaza confirm/prompt del navegador)

// input: valor inicial del campo (null = sin campo); label y type para pedir algo distinto de un nombre (p. ej. una contraseña).
// choices: opciones excluyentes [{ value, label, hint }] (la primera viene elegida); con ellas se devuelve { text, choice }.
// act: lo que hace el botón principal dentro del propio toque (copiar o compartir exigen el gesto del usuario); con él
// el campo es de solo lectura, para mostrar algo que se va a copiar.
export function ask({ heading, text = '', input = null, ok = 'Aceptar', danger = false, cancel = 'Cancelar', label = 'Nombre', type = 'text', choices = null, act = null }) {
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
  inp.readOnly = !!act;
  $('#ask-ok').onclick = act ? () => act(inp.value) : null;
  $('#ask-label').textContent = label;
  const group = $('#ask-choice');
  group.hidden = !choices;
  group.setAttribute('aria-label', choices ? 'Tipo de acceso' : '');
  group.replaceChildren(...(choices ?? []).map((c, i) => {
    const radio = el('input', { type: 'radio', name: 'ask-choice', value: c.value, checked: i === 0 });
    return el('label', { className: 'ask-option' }, radio, el('span', {}, el('b', { textContent: c.label }), el('small', { className: 'muted', textContent: c.hint })));
  }));
  $('#ask-cancel').textContent = cancel;
  $('#ask-ok').textContent = ok;
  $('#ask-ok').className = `btn ${danger ? 'danger' : 'primary'}`;
  d.returnValue = '';
  d.showModal();
  if (input != null) inp.focus();
  return new Promise((resolve) => {
    d.onclose = () => {
      if (d.returnValue !== 'ok') return resolve(null);
      const text = input != null ? inp.value.trim() : true;
      resolve(choices ? { text, choice: group.querySelector('input:checked')?.value } : text);
    };
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
  const br = [[/PaloBoxApp\//, 'PaloBox'], [/Edg\//, 'Edge'], [/OPR\//, 'Opera'], [/Firefox\/|FxiOS/, 'Firefox'], [/Chrome\/|CriOS/, 'Chrome'], [/Safari\//, 'Safari'], [/curl|Go-http|python/i, 'Script']].find(([re]) => re.test(ua))?.[1];
  return os ? `${br ?? 'Navegador'} en ${os}` : br ?? 'Dispositivo desconocido';
}

// coverColor: el tono medio de una carátula ya cargada, con luz fija (30 %) para que el texto blanco siempre
// se lea encima; null si el canvas falla.
export function coverColor(img) {
  try {
    const c = el('canvas', { width: 8, height: 8 }).getContext('2d');
    c.drawImage(img, 0, 0, 8, 8);
    const px = c.getImageData(0, 0, 8, 8).data;
    let [r, g, b] = [0, 0, 0];
    for (let i = 0; i < px.length; i += 4) { r += px[i]; g += px[i + 1]; b += px[i + 2]; }
    [r, g, b] = [r, g, b].map((v) => v / 64 / 255);
    const max = Math.max(r, g, b);
    const d = max - Math.min(r, g, b);
    const h = !d ? 0 : max === r ? ((g - b) / d) % 6 : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
    const s = d / (1 - Math.abs(2 * max - d - 1) || 1);
    return `hsl(${Math.round(h * 60 + 360) % 360} ${Math.round(Math.min(70, s * 130))}% 30%)`;
  } catch { return null; }
}

// sheetDrag: en celular, una hoja inferior se arrastra con el dedo desde cualquier parte, no solo desde el encabezado.
// Hacia abajo se encoge o se cierra; con grow, hacia arriba pasa de media pantalla a completa (clase .full).
// La lista de adentro solo se desplaza con la hoja completa (o sin grow), y estando arriba el arrastre hacia abajo
// vuelve a ser de la hoja. Son eventos touch y no pointer: Safari no deja repartir el gesto con touch-action.
export function sheetDrag(d, scroller, { grow = false } = {}) {
  const phone = matchMedia('(max-width: 899px)');
  const reduce = matchMedia('(prefers-reduced-motion: reduce)');
  let g = null; // { y, t, h, top, on }
  d.addEventListener('touchstart', (e) => {
    g = phone.matches && e.touches.length === 1 && !e.target.closest('.grip, input')
      ? { y: e.touches[0].clientY, t: e.timeStamp, h: d.offsetHeight, top: scroller.scrollTop <= 0 || !scroller.contains(e.target), on: false } : null;
  }, { passive: true });
  d.addEventListener('touchmove', (e) => {
    if (!g) return;
    const dy = e.touches[0].clientY - g.y;
    const half = grow && !d.classList.contains('full');
    // Se decide en el primer movimiento: después el navegador ya empezó a desplazar y no suelta el gesto.
    if (!g.on && !(half || (dy > 0 && g.top))) return (g = null);
    g.on = true;
    if (e.cancelable) e.preventDefault();
    d.style.transition = 'none';
    d.style.height = half && dy < 0 ? `${Math.min(innerHeight, g.h - dy)}px` : '';
    d.style.transform = `translateY(${Math.max(0, dy)}px)`;
  }, { passive: false });
  const end = async (e) => {
    if (!g?.on) return (g = null);
    const { y, t, h } = g;
    g = null;
    const dy = e.changedTouches[0].clientY - y;
    if (Math.abs(dy) > 10 && e.cancelable) e.preventDefault(); // el botón donde empezó el gesto no se activa
    const ok = e.type === 'touchend';
    const flick = Math.abs(dy) > 30 && Math.abs(dy) / (e.timeStamp - t) > 0.5;
    const ms = reduce.matches ? 0 : 200;
    d.style.transition = `transform ${ms}ms var(--ease), height ${ms}ms var(--ease)`;
    if (ok && grow && dy < 0 && (flick || -dy > 60)) d.classList.add('full');
    else if (ok && dy > 0 && (flick || dy > h / 4)) {
      if (d.classList.contains('full') && dy < h / 2) d.classList.remove('full'); // de completa a media
      else {
        d.style.transform = 'translateY(100%)';
        await sleep(ms);
        d.close();
      }
    }
    d.style.transform = d.style.height = '';
  };
  d.addEventListener('touchend', end);
  d.addEventListener('touchcancel', end);
}
sheetDrag($('#menu'), $('#menu'));
