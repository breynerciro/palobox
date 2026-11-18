// Panel del administrador.
import { $, el, icon, size, fmtDate, plural, joinText } from './lib.js';
import { api } from './session.js';

function ago(sec) {
  if (!sec) return 'Nunca ha entrado';
  const min = Math.floor((Date.now() / 1000 - sec) / 60);
  if (min < 2) return 'En línea ahora';
  if (min < 60) return `Hace ${min} min`;
  if (min < 1440) return `Hace ${Math.floor(min / 60)} h`;
  return `Último uso: ${fmtDate(sec * 1000)}`;
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
  return el('li', {}, el('div', { className: 'row static' },
    el('span', { className: `type${online ? ' online' : ''}`, style: `--tc: var(--t-${type})` }, icon(iconName)),
    el('div', { className: 'row-text' }, el('p', { textContent: name }), el('p', { className: 'muted', textContent: sub }),
      el('div', { className: 'bar' }, el('span', { style: `width: ${Math.min(100, Math.round(share * 100))}%` })))));
}

const OTHER = { media: 'Música', '.palobox': 'Miniaturas e índice' };

export async function adminView() {
  const r = await api('/api/admin');
  if (!r.ok) throw new Error(await r.text());
  const s = await r.json();
  const days = Math.floor(s.uptime / 86400);
  $('#subtitle').textContent = `Servidor encendido hace ${days ? plural(days, 'día', 'días') : plural(Math.floor(s.uptime / 3600), 'hora', 'horas')}`;
  const online = (u) => u.seen && Date.now() / 1000 - u.seen < 120;
  const frag = document.createDocumentFragment();
  frag.append(el('div', { className: 'meters' },
    meter('Procesador', s.load[0] / s.cpus, `Carga ${s.load[0].toFixed(2).replace('.', ',')} en ${plural(s.cpus, 'núcleo', 'núcleos')}`),
    meter('Memoria', 1 - s.memFree / s.memTotal, `${size(s.memTotal - s.memFree)} de ${size(s.memTotal)}`),
    meter('Disco', 1 - s.diskFree / s.diskTotal, `${size(s.diskFree)} libres`)));
  frag.append(el('h2', { className: 'section-title' }, 'Usuarios', el('span', { textContent: `${s.users.filter(online).length} en línea de ${s.users.length}` })),
    el('ul', { className: 'list' }, s.users.map((u) => usageRow('image', 'user', u.name,
      joinText(ago(u.seen), plural(u.sessions, 'sesión', 'sesiones'), plural(u.media, 'foto', 'fotos'), size(u.bytes)), u.bytes / s.diskTotal, online(u)))));
  frag.append(el('h2', { className: 'section-title' }, 'Otro almacenamiento', el('span', { textContent: `de ${size(s.diskTotal)}` })),
    el('ul', { className: 'list' }, s.other.map((o) => usageRow('video', 'folder', OTHER[o.name] ?? o.name, size(o.bytes), o.bytes / s.diskTotal))));
  return frag;
}
