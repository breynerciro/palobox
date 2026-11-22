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
  const title = (text) => el('h3', { className: 'section-title', textContent: text });
  const note = (text) => el('p', { className: 'muted note', textContent: text });
  const btn = (cls, glyph, text, run) => {
    const b = el('button', { className: `btn ${cls}` }, glyph && icon(glyph), text);
    b.onclick = run;
    return b;
  };
  const out = [title(room?.open ? 'Reproducir en (todos los del jam)' : 'Reproducir en')];
  out.push(el('ul', { className: 'list' }, devices.map((d) => {
    const on = room ? d.active : d.me;
    const b = el('button', { className: 'row' }, el('span', { className: 'type', style: `--tc: var(--t-${on ? 'audio' : 'doc'})` }, icon('cast')),
      el('div', { className: 'row-text' }, el('p', { textContent: d.name }), el('p', { className: 'muted', textContent: joinText(d.me && 'Este dispositivo', d.user, on && 'Suena aquí') })),
      on && icon('check'));
    if (on) b.setAttribute('aria-current', 'true');
    b.onclick = () => playOn(d);
    // El botón de la derecha suma o quita este dispositivo sin tocar los demás: así suenan varios a la vez.
    const several = devices.filter((x) => (room ? x.active : x.me)).length > 1;
    const also = (!on || several) && el('button', { className: 'icon-btn' }, icon(on ? 'x' : 'plus'));
    if (also) {
      also.setAttribute('aria-label', on ? `Dejar de sonar en ${d.name}` : `Sonar también en ${d.name}`);
      also.onclick = () => playOn(d, true);
    }
    return el('li', { className: 'row-wrap' }, b, also);
  })));
  out.push(note(devices.length > 1
    ? 'Toca un dispositivo para que suene solo ahí, o + para que suene también ahí, a la vez. El sonido sale por lo que tenga conectado: su parlante, audífonos o un equipo Bluetooth.'
    : 'Abre PaloBox con tu cuenta en otro dispositivo y aparecerá aquí para pasarle la música o controlarla desde este.'));
  if (audio.webkitShowPlaybackTargetPicker) { // Safari: AirPlay y salidas del sistema
    out.push(el('div', { className: 'jam-actions' }, btn('secondary small', 'volume', 'AirPlay y otras salidas', () => audio.webkitShowPlaybackTargetPicker())));
  }

  out.push(title('Jam'));
  if (room?.open) {
    const link = `${location.origin}/#/jam/${room.code}`;
    out.push(note('Comparte este código con otra persona de PaloBox. Cada quien oye en su dispositivo y todos manejan la cola.'),
      el('p', { className: 'jam-code', textContent: room.code }),
      el('div', { className: 'jam-actions' }, btn('primary small', null, navigator.share ? 'Compartir enlace' : 'Copiar enlace', async () => {
        try {
          if (navigator.share) await navigator.share({ title: 'Jam en PaloBox', url: link });
          else { await navigator.clipboard.writeText(link); toast('Enlace copiado.', { kind: 'ok' }); }
        } catch {} // se cerró el menú de compartir
      })),
      el('ul', { className: 'list' }, room.members.map((m) => el('li', {}, el('div', { className: 'row static' },
        el('span', { className: 'type', style: '--tc: var(--t-image)' }, icon('user')),
        el('div', { className: 'row-text' }, el('p', { textContent: m.name }),
          el('p', { className: 'muted', textContent: !m.online ? 'Sin conexión' : m.device ? `Oye en ${m.device}` : 'Conectado, sin sonido' })))))));
  } else {
    out.push(note('Escucha lo mismo con otras personas de PaloBox: cada quien en su dispositivo, al mismo tiempo.'),
      el('div', { className: 'jam-actions' },
        btn('primary small', 'plus', 'Iniciar jam', () => (state().room ? jam.send('open') : startRoom({ open: true }))),
        btn('secondary small', null, 'Unirme con un código', async () => {
          const code = await ask({ heading: 'Unirme a un jam', text: 'Escribe el código de 6 caracteres que te compartieron.', input: '', label: 'Código', ok: 'Unirme' });
          if (code) joinJam(code);
        })));
  }
  if (room?.open) { // la sala propia no se «deja»: es el reproductor
    out.push(el('div', { className: 'jam-actions' }, btn('secondary small', 'logout', 'Salir del jam', () => jam.send('leave'))));
  }
  $('#jam-body').replaceChildren(...out);
}
$('#jam-close').onclick = () => $('#jam').close();
$('#jam').onclick = (e) => e.target === e.currentTarget && e.currentTarget.close(); // clic en el telón
