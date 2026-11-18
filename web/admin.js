// Panel del administrador: estado del servidor, usuarios, sesiones, entradas, música y descargas.
import { $, el, icon, size, fmtDate, plural, joinText, json, toast, ask, menu, fmtLong } from './lib.js';
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

// device resume el navegador y el sistema a partir del User-Agent: «Chrome en Android».
function device(ua) {
  if (!ua) return 'dispositivo sin registrar'; // sesiones abiertas antes de que se guardara el navegador
  const os = [[/iPhone/, 'iPhone'], [/iPad/, 'iPad'], [/Android/, 'Android'], [/Windows/, 'Windows'], [/Mac OS X/, 'Mac'], [/CrOS/, 'ChromeOS'], [/Linux/, 'Linux']].find(([re]) => re.test(ua))?.[1];
  const br = [[/Edg\//, 'Edge'], [/OPR\//, 'Opera'], [/Firefox\/|FxiOS/, 'Firefox'], [/Chrome\/|CriOS/, 'Chrome'], [/Safari\//, 'Safari'], [/curl|Go-http|python/i, 'Script']].find(([re]) => re.test(ua))?.[1];
  return os ? `${br ?? 'Navegador'} en ${os}` : br ?? 'Dispositivo desconocido';
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
  const out = [];
  const disk = 1 - s.diskFree / s.diskTotal;
  if (disk >= 0.9) out.push(`El disco está al ${Math.round(disk * 100)} %: quedan ${size(s.diskFree)}. Las subidas y descargas fallarán cuando se llene.`);
  if (1 - s.memFree / s.memTotal >= 0.9) out.push('La memoria del servidor está casi llena.');
  if (s.failed24h >= 5) out.push(`${plural(s.failed24h, 'intento fallido', 'intentos fallidos')} de entrar en las últimas 24 h. Revisa «Entradas recientes».`);
  if (sp?.enabled && !sp.ok) out.push(sp.error || 'El servicio de descargas de Spotify no responde.');
  if (!out.length) return null;
  return el('ul', { className: 'list alerts' }, out.map((t) => el('li', {}, el('div', { className: 'row static' },
    el('span', { className: 'type', style: '--tc: var(--danger)' }, icon('alert')),
    el('div', { className: 'row-text' }, el('p', { textContent: t }))))));
}

// Usuarios

async function newUser() {
  const name = await ask({ heading: 'Nuevo usuario', text: 'Solo minúsculas, números, - y _. También es el nombre de su carpeta.', input: '', label: 'Usuario', ok: 'Siguiente' });
  if (!name) return;
  const pass = await ask({ heading: `Contraseña de ${name}`, text: 'Mínimo 8 caracteres. Compártela con esa persona por un medio seguro.', input: '', label: 'Contraseña', type: 'password', ok: 'Crear usuario' });
  if (pass) send('/api/admin/users', json('POST', { name, pass }), `Listo: ${name} ya puede entrar.`);
}

function userMenu(u, anchor) {
  const path = `/api/admin/users/${encodeURIComponent(u.name)}`;
  menu(anchor, `Acciones para ${u.name}`, [
    { icon: 'edit', label: 'Cambiar contraseña', run: async () => {
      const pass = await ask({ heading: `Nueva contraseña de ${u.name}`, text: u.admin ? 'Tus otras sesiones se cerrarán; esta sigue abierta.' : 'Se cerrarán sus sesiones abiertas.', input: '', label: 'Contraseña', type: 'password', ok: 'Cambiar' });
      if (pass) send(`${path}/password`, json('PUT', { pass }), 'Contraseña cambiada.');
    } },
    u.sessions > 0 && !u.admin && { icon: 'logout', label: `Cerrar ${plural(u.sessions, 'sesión', 'sesiones')}`, run: () => send(`${path}/sessions`, { method: 'DELETE' }, `${u.name} tendrá que volver a entrar.`) },
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
  const add = el('button', { className: 'btn secondary small' }, icon('plus'), 'Nuevo usuario');
  add.onclick = newUser;
  return [
    heading('Usuarios', `${s.users.filter(online).length} en línea de ${s.users.length}`),
    el('ul', { className: 'list' }, s.users.map((u) => el('li', { className: `row-wrap${u.disabled ? ' off' : ''}` },
      usageRow('image', 'user', joinText(u.name, u.admin && 'administrador'),
        joinText(u.disabled ? 'Desactivado' : ago(u.seen), plural(u.sessions, 'sesión', 'sesiones'), plural(u.media, 'foto', 'fotos'), size(u.bytes)),
        u.bytes / s.diskTotal, online(u)),
      iconBtn('more', `Acciones para ${u.name}`, (b) => userMenu(u, b))))),
    el('div', { className: 'toolbar' }, add),
  ];
}

function sessionsSection(s) {
  return [
    heading('Sesiones abiertas', s.sessions.length),
    el('ul', { className: 'list' }, s.sessions.map((x) => el('li', { className: 'row-wrap' },
      usageRow('doc', 'user', `${x.user} · ${device(x.agent)}`,
        joinText(x.current ? 'Esta sesión' : x.seen ? `Activa ${since(x.seen)}` : 'Sin actividad registrada', x.ip, x.created && `desde ${fmtDate(x.created * 1000)}`)),
      !x.current && iconBtn('x', `Cerrar la sesión de ${x.user} en ${device(x.agent)}`, () => send(`/api/admin/sessions/${x.id}`, { method: 'DELETE' }, 'Sesión cerrada.'))))),
  ];
}

function loginsSection(s) {
  const LIMIT = 15;
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
  const scan = el('button', { className: 'btn secondary small', disabled: m.scanning }, icon('repeat'), m.scanning ? 'Escaneando…' : 'Volver a escanear');
  scan.onclick = async () => {
    if (await send('/api/admin/music/reindex', { method: 'POST' }, 'Escaneando la biblioteca. Las canciones nuevas aparecen en unos minutos.')) scan.disabled = true;
  };
  return [
    heading('Biblioteca de música', size(m.bytes)),
    el('ul', { className: 'list' }, el('li', {}, usageRow('audio', 'music',
      joinText(plural(m.tracks, 'canción', 'canciones'), plural(m.albums, 'álbum', 'álbumes'), plural(m.artists, 'artista', 'artistas')),
      joinText(m.seconds && fmtLong(m.seconds), m.scanning ? 'Escaneando ahora' : m.scanned ? `Escaneada ${since(m.scanned)}` : 'Aún sin escanear',
        m.removed && `${plural(m.removed, 'duplicado borrado', 'duplicados borrados')} desde el último arranque`)))),
    el('div', { className: 'toolbar' }, scan),
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

export async function adminView() {
  const [r, sp] = await Promise.all([
    api('/api/admin'),
    api('/api/admin/spotdl').then((x) => (x.ok ? x.json() : null)).catch(() => null), // las descargas son un extra
  ]);
  if (!r.ok) throw new Error(await r.text());
  const s = await r.json();
  const days = Math.floor(s.uptime / 86400);
  $('#subtitle').textContent = `Servidor encendido hace ${days ? plural(days, 'día', 'días') : plural(Math.floor(s.uptime / 3600), 'hora', 'horas')}`;
  const frag = document.createDocumentFragment();
  frag.append(...[
    alerts(s, sp),
    el('div', { className: 'meters' },
      meter('Procesador', s.load[0] / s.cpus, `Carga ${s.load[0].toFixed(2).replace('.', ',')} en ${plural(s.cpus, 'núcleo', 'núcleos')}`),
      meter('Memoria', 1 - s.memFree / s.memTotal, `${size(s.memTotal - s.memFree)} de ${size(s.memTotal)}`),
      meter('Disco', 1 - s.diskFree / s.diskTotal, `${size(s.diskFree)} libres`)),
    ...usersSection(s),
    ...sessionsSection(s),
    ...loginsSection(s),
    ...musicSection(s.music),
    ...(await downloadsSection(sp)),
    heading('Otro almacenamiento', `de ${size(s.diskTotal)}`),
    el('ul', { className: 'list' }, s.other.map((o) => el('li', {}, usageRow('video', 'folder', OTHER[o.name] ?? o.name, size(o.bytes), o.bytes / s.diskTotal)))),
  ].filter(Boolean));
  return frag;
}
