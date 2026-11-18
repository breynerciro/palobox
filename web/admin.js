// Panel del administrador: estado del servidor, usuarios, sesiones, entradas, música y descargas.
import { $, el, icon, size, fmtDate, plural, joinText, json, toast, ask, menu, fmtLong, deviceName as device } from './lib.js';
import { api } from './session.js';
import { render } from './nav.js';
import { jobRow } from './music-views.js';

const OFFLINE = 'Sin conexión con el servidor.';

// since da «hace 5 min» para una fecha en segundos.
function since(sec) {
  const min = Math.floor((Date.now() / 1000 - sec) / 60);
  if (min < 2) return 'ahora';
  if (min < 60) return `hace ${min} min`;
  if (min < 1440) return `hace ${Math.floor(min / 60)} h`;
  return fmtDate(sec * 1000);
}
function ago(sec) {
  if (!sec) return 'Nunca ha entrado';
  const s = since(sec);
  return s === 'ahora' ? 'En línea ahora' : s.startsWith('hace') ? s[0].toUpperCase() + s.slice(1) : `Último uso: ${s}`;
}

// send hace una acción del panel y vuelve a pintarlo; devuelve si salió bien.
async function send(url, opts, done) {
  const r = await api(url, opts).catch(() => null);
  if (!r?.ok) {
    toast(r ? await r.text() : OFFLINE, { kind: 'warn' });
    return false;
  }
  if (done) toast(done, { kind: 'ok' });
  render();
  return true;
}

// Medidor del servidor: el número grande es el dato; la barra cambia de color al acercarse al límite.
function meter(name, used, sub) {
  const pct = Math.min(100, Math.round(used * 100));
  return el('div', { className: `meter${pct >= 90 ? ' crit' : pct >= 75 ? ' warn' : ''}` },
    el('p', { className: 'muted', textContent: name }),
    el('p', { className: 'meter-num', textContent: `${pct} %` }),
    el('div', { className: 'bar' }, el('span', { style: `width: ${pct}%` })),
    el('p', { className: 'muted', textContent: sub }));
}

function usageRow(type, iconName, name, sub, share, online) {
  return el('div', { className: 'row static' },
    el('span', { className: `type${online ? ' online' : ''}`, style: `--tc: var(--t-${type})` }, icon(iconName)),
    el('div', { className: 'row-text' }, el('p', { textContent: name }), el('p', { className: 'muted', textContent: sub }),
      share != null && el('div', { className: 'bar' }, el('span', { style: `width: ${Math.min(100, Math.round(share * 100))}%` }))));
}
const heading = (text, count) => el('h2', { className: 'section-title' }, text, count != null && el('span', { textContent: count }));
const iconBtn = (glyph, label, run) => {
  const b = el('button', { className: 'icon-btn' }, icon(glyph));
  b.setAttribute('aria-label', label);
  b.onclick = () => run(b);
  return b;
};

// Alertas: lo que pide atención, arriba de todo. Sin nada raro no se muestra la sección.
function alerts(s, sp) {
  const out = []; // [texto, sección donde se resuelve]
  const disk = 1 - s.diskFree / s.diskTotal;
  if (disk >= 0.9) out.push([`El disco está al ${Math.round(disk * 100)} %: quedan ${size(s.diskFree)}. Las subidas y descargas fallarán cuando se llene.`, 'almacenamiento']);
  if (1 - s.memFree / s.memTotal >= 0.9) out.push(['La memoria del servidor está casi llena.', '']);
  if (s.failed24h >= 5) out.push([`${plural(s.failed24h, 'intento fallido', 'intentos fallidos')} de entrar en las últimas 24 h.`, 'actividad']);
  if (sp?.enabled && !sp.ok) out.push([sp.error || 'El servicio de descargas de Spotify no responde.', 'musica']);
  if (s.music.max && s.music.tracks >= s.music.max) out.push([`La biblioteca llegó a su tope de ${s.music.max.toLocaleString('es-CO')} canciones: solo tú puedes seguir descargando.`, 'musica']);
  if (!out.length) return null;
  return el('ul', { className: 'list alerts' }, out.map(([t, key]) => {
    const inner = [el('span', { className: 'type', style: '--tc: var(--danger)' }, icon('alert')), el('div', { className: 'row-text' }, el('p', { textContent: t }))];
    return el('li', {}, key ? el('a', { className: 'row', href: link(key) }, ...inner, icon('next', 'chev')) : el('div', { className: 'row static' }, ...inner));
  }));
}

// Usuarios

// Al crear la cuenta se elige su nivel; después se cambia desde el menú del usuario.
async function newUser() {
  const who = await ask({
    heading: 'Nuevo usuario', text: 'El nombre va en minúsculas, con números, - o _.', input: '', label: 'Usuario', ok: 'Siguiente',
    choices: [
      { value: 'full', label: 'Acceso completo', hint: 'Sus propias fotos y archivos, y la música.' },
      { value: 'music', label: 'Solo música', hint: 'Escuchar, armar playlists y pedir descargas.' },
    ],
  });
  if (!who?.text) return;
  const name = who.text;
  const musicOnly = who.choice === 'music';
  const pass = await ask({ heading: `Contraseña de ${name}`, text: 'Mínimo 8 caracteres. Compártela con esa persona por un medio seguro.', input: '', label: 'Contraseña', type: 'password', ok: 'Crear usuario' });
  if (pass) send('/api/admin/users', json('POST', { name, pass, musicOnly }), `Listo: ${name} ya puede entrar${musicOnly ? ', solo a Música' : ''}.`);
}

function userMenu(u, anchor) {
  const path = `/api/admin/users/${encodeURIComponent(u.name)}`;
  menu(anchor, `Acciones para ${u.name}`, [
    { icon: 'edit', label: 'Cambiar contraseña', run: async () => {
      const pass = await ask({ heading: `Nueva contraseña de ${u.name}`, text: u.admin ? 'Tus otras sesiones se cerrarán; esta sigue abierta.' : 'Se cerrarán sus sesiones abiertas.', input: '', label: 'Contraseña', type: 'password', ok: 'Cambiar' });
      if (pass) send(`${path}/password`, json('PUT', { pass }), 'Contraseña cambiada.');
    } },
    u.sessions > 0 && !u.admin && { icon: 'logout', label: `Cerrar ${plural(u.sessions, 'sesión', 'sesiones')}`, run: () => send(`${path}/sessions`, { method: 'DELETE' }, `${u.name} tendrá que volver a entrar.`) },
    !u.admin && (u.musicOnly
      ? { icon: 'folder', label: 'Dar acceso completo', run: () => send(`${path}/access`, json('PUT', { musicOnly: false }), `${u.name} ahora tiene fotos, archivos y música.`) }
      : { icon: 'music', label: 'Limitar a solo música', run: async () => {
        if (await ask({ heading: `¿Limitar a ${u.name} a solo música?`, text: 'Dejará de ver Fotos y Archivos. Lo que tenga guardado no se borra: vuelve a verlo si le das acceso completo.', ok: 'Limitar' })) {
          send(`${path}/access`, json('PUT', { musicOnly: true }), `${u.name} ahora solo entra a Música.`);
        }
      } }),
    !u.admin && (u.disabled
      ? { icon: 'check', label: 'Activar cuenta', run: () => send(`${path}/disabled`, json('PUT', { disabled: false }), `${u.name} ya puede entrar.`) }
      : { icon: 'x', label: 'Desactivar cuenta', run: async () => {
        if (await ask({ heading: `¿Desactivar a ${u.name}?`, text: 'Se cierran sus sesiones y no podrá entrar hasta que la actives. No se borra nada.', ok: 'Desactivar', danger: true })) {
          send(`${path}/disabled`, json('PUT', { disabled: true }), `${u.name} quedó desactivado.`);
        }
      } }),
    !u.admin && { icon: 'trash', label: 'Borrar usuario', danger: true, run: async () => {
      if (!(await ask({ heading: `¿Borrar a ${u.name}?`, text: 'Se borran su cuenta, me gusta, playlists e historial. No se puede deshacer.', ok: 'Siguiente', danger: true }))) return;
      const files = await ask({ heading: `¿Y sus fotos y archivos (${size(u.bytes)})?`, text: 'Si los conservas, quedan en el disco y los recupera un usuario nuevo con el mismo nombre.', ok: 'Borrarlos también', cancel: 'Conservarlos', danger: true });
      send(`${path}${files ? '?files=1' : ''}`, { method: 'DELETE' }, `${u.name} fue borrado.`);
    } },
  ]);
}

function usersSection(s) {
  const online = (u) => !u.disabled && u.seen && Date.now() / 1000 - u.seen < 120;
  const add = el('button', { className: 'btn primary small' }, icon('plus'), 'Nuevo usuario');
  add.onclick = newUser;
  return [
    el('div', { className: 'section-head' }, heading('Usuarios', `${s.users.filter(online).length} en línea de ${s.users.length}`), add),
    el('ul', { className: 'list' }, s.users.map((u) => el('li', { className: `row-wrap${u.disabled ? ' off' : ''}` },
      usageRow(u.musicOnly ? 'audio' : 'image', u.musicOnly ? 'music' : 'user', joinText(u.name, u.admin && 'administrador', u.musicOnly && 'solo música'),
        joinText(u.disabled ? 'Desactivado' : ago(u.seen), plural(u.sessions, 'sesión', 'sesiones'), size(u.bytes)),
        null, online(u)),
      iconBtn('more', `Acciones para ${u.name}`, (b) => userMenu(u, b))))),
  ];
}

// Sesiones agrupadas por usuario (las tuyas primero), cada grupo con su botón para cerrarlas todas.
function sessionsSection(s) {
  const groups = new Map();
  for (const x of s.sessions) groups.set(x.user, [...(groups.get(x.user) ?? []), x]);
  const mine = s.sessions.find((x) => x.current)?.user;
  const users = [...groups.keys()].sort((a, b) => (b === mine) - (a === mine) || groups.get(b)[0].seen - groups.get(a)[0].seen);
  const out = [heading('Sesiones abiertas', `${s.sessions.length} de ${plural(users.length, 'usuario', 'usuarios')}`)];
  if (!users.length) out.push(el('p', { className: 'muted note', textContent: 'No hay sesiones abiertas.' }));
  for (const name of users) {
    const list = groups.get(name);
    const all = name !== mine && list.length > 1 && el('button', { className: 'btn secondary small' }, 'Cerrar todas');
    if (all) all.onclick = () => send(`/api/admin/users/${encodeURIComponent(name)}/sessions`, { method: 'DELETE' }, `${name} tendrá que volver a entrar.`);
    out.push(el('div', { className: 'section-head sub' }, el('h3', { className: 'section-title' }, name, el('span', { textContent: plural(list.length, 'sesión', 'sesiones') })), all),
      el('ul', { className: 'list' }, list.map((x) => el('li', { className: 'row-wrap' },
        usageRow('doc', 'user', device(x.agent),
          joinText(x.current ? 'Esta sesión' : x.seen ? `Activa ${since(x.seen)}` : 'Sin actividad registrada', x.ip, x.created && `desde ${fmtDate(x.created * 1000)}`)),
        !x.current && iconBtn('x', `Cerrar la sesión de ${name} en ${device(x.agent)}`, (b) => {
          b.closest('li').remove(); // se va de una vez; el repintado confirma
          send(`/api/admin/sessions/${x.id}`, { method: 'DELETE' }, 'Sesión cerrada.');
        })))));
  }
  return out;
}

function loginsSection(s) {
  const LIMIT = 30;
  if (!s.logins.length) return [];
  return [
    heading('Entradas recientes', s.failed24h ? `${plural(s.failed24h, 'fallida', 'fallidas')} en 24 h` : null),
    el('ul', { className: 'list' }, s.logins.slice(0, LIMIT).map((l) => el('li', {}, el('div', { className: 'row static' },
      el('span', { className: 'type', style: `--tc: var(${l.ok ? '--success' : '--danger'})` }, icon(l.ok ? 'check' : 'alert')),
      el('div', { className: 'row-text' }, el('p', { textContent: `${l.name || '(sin usuario)'} · ${l.ok ? 'entró' : 'falló'}` }),
        el('p', { className: 'muted', textContent: joinText(since(l.at), device(l.agent), l.ip) })))))),
  ];
}

function musicSection(m) {
  const scan = el('button', { className: 'btn secondary small', disabled: m.scanning }, icon('repeat'), m.scanning ? 'Escaneando…' : 'Escanear');
  scan.onclick = async () => {
    if (await send('/api/admin/music/reindex', { method: 'POST' }, 'Escaneando la biblioteca. Las canciones nuevas aparecen en unos minutos.')) scan.disabled = true;
  };
  return [
    el('div', { className: 'section-head' }, heading('Biblioteca de música'), scan),
    el('ul', { className: 'list' }, el('li', {}, usageRow('audio', 'music',
      joinText(m.max ? `${m.tracks.toLocaleString('es-CO')} de ${m.max.toLocaleString('es-CO')} canciones` : plural(m.tracks, 'canción', 'canciones'), plural(m.albums, 'álbum', 'álbumes'), plural(m.artists, 'artista', 'artistas')),
      joinText(size(m.bytes), m.seconds && fmtLong(m.seconds), m.scanning ? 'Escaneando ahora' : m.scanned ? `Escaneada ${since(m.scanned)}` : 'Aún sin escanear',
        m.removed && `${plural(m.removed, 'duplicado borrado', 'duplicados borrados')} desde el último arranque`)))),
  ];
}

async function downloadsSection(sp) {
  if (!sp?.enabled) return [];
  if (!sp.ok) return [heading('Descargas de Spotify', 'sin respuesta')];
  const r = await api('/api/spotdl').catch(() => null);
  const items = r?.ok ? (await r.json()).items.filter((j) => j.status !== 'finished') : [];
  return [
    heading('Descargas de Spotify', joinText(sp.spotdl && `spotDL ${sp.spotdl}`, plural(sp.active ?? 0, 'activa', 'activas'))),
    items.length
      ? el('ul', { className: 'list' }, items.map((j) => jobRow(j, render)))
      : el('p', { className: 'muted note', textContent: 'No hay descargas en cola ni con errores.' }),
  ];
}

const OTHER = { media: 'Música', '.palobox': 'Miniaturas e índice' };

function storageSection(s) {
  return [
    el('div', { className: 'meters single' }, meter('Disco', 1 - s.diskFree / s.diskTotal, `${size(s.diskFree)} libres de ${size(s.diskTotal)}`)),
    heading('Por usuario'),
    el('ul', { className: 'list' }, [...s.users].sort((a, b) => b.bytes - a.bytes).map((u) => el('li', {},
      usageRow('image', 'user', u.name, joinText(size(u.bytes), plural(u.media, 'foto', 'fotos')), u.bytes / s.diskTotal)))),
    heading('Compartido y del sistema'),
    el('ul', { className: 'list' }, s.other.map((o) => el('li', {}, usageRow('video', 'folder', OTHER[o.name] ?? o.name, size(o.bytes), o.bytes / s.diskTotal)))),
  ];
}

// Secciones del panel: cada una tiene su ruta (#/admin/usuarios), así funcionan «atrás» y los enlaces directos.
const SECTIONS = [
  ['', 'Resumen'], ['usuarios', 'Usuarios'], ['actividad', 'Actividad'], ['musica', 'Música'], ['almacenamiento', 'Almacenamiento'],
];
const link = (key) => '#/admin' + (key ? '/' + key : '');

function sectionNav(current) {
  let active;
  const nav = el('nav', { className: 'toolbar admin-nav' }, SECTIONS.map(([key, text]) => {
    const a = el('a', { className: 'chip', href: link(key), textContent: text });
    if (key === current) a.setAttribute('aria-current', 'page'), (active = a);
    return a;
  }));
  nav.setAttribute('aria-label', 'Secciones de administración');
  // La fila se vuelve a crear en cada sección: sin esto vuelve al inicio y la elegida queda fuera de la pantalla.
  requestAnimationFrame(() => (nav.scrollLeft = active.offsetLeft - (nav.clientWidth - active.offsetWidth) / 2));
  return nav;
}

// Resumen: lo urgente arriba y una tarjeta por sección con su dato clave; el detalle está a un toque.
function summary(s, sp) {
  const online = s.users.filter((u) => !u.disabled && u.seen && Date.now() / 1000 - u.seen < 120).length;
  const card = (key, glyph, type, title, value, sub, warn) => el('a', { className: `stat-card${warn ? ' warn' : ''}`, href: link(key) },
    el('span', { className: 'type', style: `--tc: var(--t-${type})` }, icon(glyph)),
    el('p', { className: 'muted', textContent: title }),
    el('p', { className: 'stat-num', textContent: value }),
    el('p', { className: 'muted', textContent: sub }));
  return [
    alerts(s, sp),
    el('div', { className: 'meters' },
      meter('Procesador', s.load[0] / s.cpus, `Carga ${s.load[0].toFixed(2).replace('.', ',')} en ${plural(s.cpus, 'núcleo', 'núcleos')}`),
      meter('Memoria', 1 - s.memFree / s.memTotal, `${size(s.memTotal - s.memFree)} de ${size(s.memTotal)}`),
      meter('Disco', 1 - s.diskFree / s.diskTotal, `${size(s.diskFree)} libres`)),
    el('div', { className: 'stat-cards' },
      card('usuarios', 'user', 'image', 'Usuarios', String(s.users.length), online ? `${online} en línea ahora` : 'Nadie en línea'),
      card('actividad', 'activity', 'doc', 'Sesiones abiertas', String(s.sessions.length),
        s.failed24h ? `${plural(s.failed24h, 'entrada fallida', 'entradas fallidas')} en 24 h` : 'Sin entradas fallidas', s.failed24h >= 5),
      card('musica', 'music', 'audio', 'Canciones', s.music.tracks.toLocaleString('es-CO'),
        sp?.enabled ? (sp.ok ? plural(sp.active ?? 0, 'descarga activa', 'descargas activas') : 'Descargas sin respuesta') : plural(s.music.albums, 'álbum', 'álbumes'), sp?.enabled && !sp.ok),
      card('almacenamiento', 'folder', 'video', 'Disco libre', size(s.diskFree), `de ${size(s.diskTotal)}`, s.diskFree / s.diskTotal <= 0.1)),
  ];
}

export async function adminView(path = '') {
  const key = SECTIONS.some(([k]) => k === path) ? path : '';
  const needsSpot = key === '' || key === 'musica'; // el estado de las descargas solo se pide donde se usa
  const [r, sp] = await Promise.all([
    api('/api/admin'),
    needsSpot ? api('/api/admin/spotdl').then((x) => (x.ok ? x.json() : null)).catch(() => null) : null, // las descargas son un extra
  ]);
  if (!r.ok) throw new Error(await r.text());
  const s = await r.json();
  const days = Math.floor(s.uptime / 86400);
  $('#subtitle').textContent = `Servidor encendido hace ${days ? plural(days, 'día', 'días') : plural(Math.floor(s.uptime / 3600), 'hora', 'horas')}`;
  const body = key === 'usuarios' ? usersSection(s)
    : key === 'actividad' ? [...sessionsSection(s), ...loginsSection(s)]
    : key === 'musica' ? [...musicSection(s.music), ...(await downloadsSection(sp))]
    : key === 'almacenamiento' ? storageSection(s)
    : summary(s, sp);
  const frag = document.createDocumentFragment();
  frag.append(...[sectionNav(key), ...body].filter(Boolean));
  return frag;
}
