// Panel de dispositivos y jam: en cuál de mis dispositivos suena y escuchar lo mismo con otras personas.
import { $, el, icon, joinText, toast, plural, ask } from './lib.js';
import * as jam from './jam.js';
import { audio, state, startRoom } from './player.js';

export async function joinJam(code) {
  code = code.trim().toUpperCase();
  if (code && await jam.send('join', { code })) toast('Te uniste al jam. Todos pueden agregar canciones y cambiar la que suena.', { kind: 'ok' });
}

// playOn elige en cuál de mis dispositivos suena; con add lo suma a los que ya suenan (o lo quita si ya sonaba).
function playOn(d, add = false) {
  const { room, pos } = state();
  if (room) return void jam.send('here', { target: d.id, user: d.user, add });
  if (d.me) return; // sin sala ya suena aquí
  if (pos < 0) return toast('Pon a sonar algo primero y luego elige el dispositivo.');
  startRoom({ target: d.id, add });
}

export function paintJam() {
  const { room, devices, here } = state();
  const sounding = devices.filter((d) => d.active);
  const active = sounding.length && { name: sounding.map((d) => d.name).join(' y ') };
  const shared = !!room && (room.members.length > 1 || !here);
  for (const b of document.querySelectorAll('[data-act=jam]')) b.classList.toggle('on', shared);
  $('#np-where').textContent = room && !here ? (active ? `Sonando en ${active.name}` : 'En pausa: ningún dispositivo') : room?.members.length > 1 ? `Jam con ${plural(room.members.length - 1, 'persona', 'personas')}` : 'Reproduciendo';
  if (!$('#jam').open) return;
  const label = (text) => el('h3', { className: 'jam-label', textContent: text });
  const note = (text) => el('p', { className: 'muted note', textContent: text });
  const btn = (cls, glyph, text, run) => {
    const b = el('button', { className: `btn ${cls}` }, glyph && icon(glyph), text);
    b.onclick = run;
    return b;
  };
  // Arriba, lo que más importa: dónde está sonando ahora.
  const on = (d) => (room ? d.active : d.me);
  const playing = devices.filter(on);
  const out = [el('div', { className: 'jam-now' }, icon('cast'),
    el('div', { className: 'row-text' }, el('p', { className: 'muted', textContent: room?.open ? 'El jam suena en' : 'Suena en' }),
      el('p', { className: 'ellipsis', textContent: playing.length ? playing.map((d) => (d.me ? 'Este dispositivo' : d.name)).join(' y ') : 'Ningún dispositivo' })))];

  out.push(label(room?.open ? 'Dispositivos del jam' : 'Tus dispositivos'));
  out.push(el('ul', { className: 'list' }, devices.map((d) => {
    const b = el('button', { className: `row${on(d) ? ' playing' : ''}` }, el('span', { className: 'type', style: `--tc: var(--t-${on(d) ? 'audio' : 'doc'})` }, icon('cast')),
      el('div', { className: 'row-text' }, el('p', { textContent: d.name }), el('p', { className: 'muted', textContent: joinText(d.me && 'Este dispositivo', d.user, on(d) ? 'Suena aquí' : 'Toca para que suene aquí') })),
      on(d) && icon('check'));
    if (on(d)) b.setAttribute('aria-current', 'true');
    b.onclick = () => playOn(d);
    // El botón de la derecha suma o quita este dispositivo sin tocar los demás: así suenan varios a la vez.
    const also = (!on(d) || playing.length > 1) && el('button', { className: 'icon-btn' }, icon(on(d) ? 'x' : 'plus'));
    if (also) {
      also.setAttribute('aria-label', on(d) ? `Dejar de sonar en ${d.name}` : `Sonar también en ${d.name}`);
      also.onclick = () => playOn(d, true);
    }
    return el('li', { className: 'row-wrap' }, b, also);
  })));
  out.push(note(devices.length > 1
    ? 'Con + suena también ahí, a la vez. El sonido sale por lo que ese dispositivo tenga conectado: su parlante, audífonos o un equipo Bluetooth.'
    : 'Abre PaloBox con tu cuenta en otro dispositivo y aparecerá aquí para pasarle la música o controlarla desde este.'));
  if (audio.webkitShowPlaybackTargetPicker) { // Safari: AirPlay y salidas del sistema
    out.push(el('div', { className: 'jam-actions' }, btn('secondary small', 'volume', 'AirPlay y otras salidas', () => audio.webkitShowPlaybackTargetPicker())));
  }

  // El jam va en su propia tarjeta: es otra cosa (escuchar con más personas), no un dispositivo más.
  const card = el('section', { className: 'jam-card' });
  if (room?.open) {
    const link = `${location.origin}/#/jam/${room.code}`;
    card.append(el('h3', { textContent: `Jam en curso · ${plural(room.members.length, 'persona', 'personas')}` }),
      el('p', { className: 'muted', textContent: 'Comparte este código con otra persona de PaloBox. Cada quien oye en su dispositivo y todos manejan la cola.' }),
      el('p', { className: 'jam-code', textContent: room.code }),
      btn('primary block', null, navigator.share ? 'Compartir enlace' : 'Copiar enlace', async () => {
        try {
          if (navigator.share) await navigator.share({ title: 'Jam en PaloBox', url: link });
          else { await navigator.clipboard.writeText(link); toast('Enlace copiado.', { kind: 'ok' }); }
        } catch {} // se cerró el menú de compartir
      }),
      el('ul', { className: 'jam-members' }, room.members.map((m) => el('li', {},
        el('span', { className: `avatar${m.online ? '' : ' away'}`, textContent: m.name.slice(0, 1).toUpperCase() }),
        el('div', { className: 'row-text' }, el('p', { className: 'ellipsis', textContent: m.name }),
          el('p', { className: 'muted', textContent: !m.online ? 'Sin conexión' : m.device ? `Oye en ${m.device}` : 'Conectado, sin sonido' }))))),
      btn('ghost block danger-text', 'logout', 'Salir del jam', () => jam.send('leave'))); // la sala propia no se «deja»: es el reproductor
  } else {
    card.append(el('h3', { textContent: 'Escuchen juntos' }),
      el('p', { className: 'muted', textContent: 'Un jam es la misma música con otras personas de PaloBox: cada quien en su dispositivo, al mismo tiempo.' }),
      btn('primary block', 'plus', 'Iniciar jam', () => (state().room ? jam.send('open') : startRoom({ open: true }))),
      btn('secondary block', null, 'Unirme con un código', async () => {
        const code = await ask({ heading: 'Unirme a un jam', text: 'Escribe el código de 6 caracteres que te compartieron.', input: '', label: 'Código', ok: 'Unirme' });
        if (code) joinJam(code);
      }));
  }
  out.push(card);
  $('#jam-body').replaceChildren(...out);
}
$('#jam-close').onclick = () => $('#jam').close();
$('#jam').onclick = (e) => e.target === e.currentTarget && e.currentTarget.close(); // clic en el telón
