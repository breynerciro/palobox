// Modo TV: la app instalada en el televisor (android/) abre con ?tv y se maneja con las flechas del control remoto.
// Cada flecha lleva el foco al control visible más cercano en esa dirección; OK ya llega como Enter y «atrás» lo
// maneja la app de Android. Sin esto el foco se queda atrapado en los deslizadores y no hay cómo recorrer la página.

// tv: ¿esta es la app del televisor? Se recuerda, porque el ?tv solo viene en la dirección con la que abre.
export const tv = (() => {
  try {
    if (new URLSearchParams(location.search).has('tv')) localStorage.setItem('tv', '1');
    return localStorage.getItem('tv') === '1';
  } catch { return false; } // fuera del navegador (pruebas) o sin almacenamiento
})();

const AXIS = { ArrowLeft: ['x', -1], ArrowRight: ['x', 1], ArrowUp: ['y', -1], ArrowDown: ['y', 1] };

// pick devuelve el índice del rectángulo más cercano a `from` yendo hacia `key`, o -1 si no hay ninguno.
// Primero lo alineado y después lo cercano: desde una fila se llega al vecino de esa fila antes que a algo en diagonal.
// Con `strict` solo vale lo alineado.
export function pick(from, rects, key, strict = false) {
  const [axis, sign] = AXIS[key];
  const [lo, hi, plo, phi] = axis === 'x' ? ['left', 'right', 'top', 'bottom'] : ['top', 'bottom', 'left', 'right'];
  const mid = (r) => (r[lo] + r[hi]) / 2;
  let best = -1, bestScore = Infinity;
  rects.forEach((r, i) => {
    const ahead = sign * (mid(r) - mid(from));
    if (ahead <= 0) return;
    const gap = sign > 0 ? r[lo] - from[hi] : from[lo] - r[hi];
    const off = Math.max(0, r[plo] - from[phi], from[plo] - r[phi]); // 0 si se cruzan en el otro eje
    if (off && (strict || gap < 0)) return; // en diagonal solo vale lo que queda del todo más allá: no un vecino de arriba que apenas sobresale
    const score = (off ? 1e6 : 0) + Math.max(0, gap) + off * 4 + ahead / 100; // lo alineado le gana siempre a lo que está en diagonal
    if (score < bestScore) [best, bestScore] = [i, score];
  });
  return best;
}

if (tv) {
  document.documentElement.classList.add('tv'); // style.css: todo el diseño del televisor cuelga de esta clase
  // «Reproduciendo» sin tocar el control un rato: quedan la carátula y el título (style.css, .idle). La primera tecla
  // solo trae los botones de vuelta, no los oprime.
  const np = document.querySelector('#np');
  let idle;
  const wake = () => {
    np.classList.remove('idle');
    clearTimeout(idle);
    if (np.open) idle = setTimeout(() => np.classList.add('idle'), 10_000);
  };
  addEventListener('pointerdown', wake, true);
  addEventListener('keydown', (e) => {
    const was = np.classList.contains('idle');
    wake();
    if (!was) return;
    e.preventDefault();
    e.stopImmediatePropagation();
  }, true);
  // Atrás: esa tecla no le llega a la página; la app de Android retrocede en el historial y, si no hay a dónde, se
  // cierra. Por eso cada diálogo que se abre deja una entrada (con cuántos hay abiertos): atrás lo cierra en vez de
  // sacar de la app. Con toggle y no beforetoggle: llega después de que un menú ya navegó, y no deshace esa navegación.
  const modals = () => [...document.querySelectorAll('dialog:modal')];
  const depth = () => history.state?.tvDialog ?? 0;
  if (depth()) history.replaceState(null, ''); // recargada con un diálogo abierto: la entrada sobrevive, el diálogo no
  document.addEventListener('toggle', (e) => {
    if (!(e.target instanceof HTMLDialogElement)) return;
    if (e.newState === 'open') {
      history.pushState({ tvDialog: modals().length }, '');
      if (e.target.id === 'np') e.target.querySelector('#np-play').focus(); // lo primero a mano: pausar y reanudar
      wake();
    } else if (depth() > modals().length) history.back(); // se cerró con su botón: su entrada sobra
  }, true);
  addEventListener('popstate', () => {
    const open = modals();
    if (open.length < depth()) return history.back(); // entrada de un diálogo que ya no está (cerró y navegó)
    for (const d of open.slice(depth())) d.close();
  });
  const ZONES = '#me-btn, .topbar, #view, #player, #tabs, #mtabs, #toast, #upbar'; // fuera de ellas (un diálogo) todo es una sola zona
  const SEL = 'a[href], button, input, select, textarea, [tabindex]';
  const sized = (r) => r.width > 1 && r.height > 1; // deja por fuera lo que solo existe para el lector de pantalla
  const usable = (n) => !n.disabled && n.tabIndex >= 0 && !n.closest('[inert]') && n.checkVisibility({ visibilityProperty: true });
  // En un televisor siempre se ve dónde se está: al pintarse una página el foco va a su primer elemento, o a donde
  // estaba si se vuelve a ella (atrás desde un álbum deja en su tarjeta, no al comienzo de la página).
  const view = document.querySelector('#view');
  const items = () => [...view.querySelectorAll(SEL)].filter(usable);
  const last = new Map(); // dirección de la página → puesto, entre sus elementos, del último al que se llegó con el control
  let moved = false; // ¿ya se movió el foco con el control en esta página?
  addEventListener('hashchange', () => { moved = false; });
  const home = () => {
    const all = items();
    return all[last.get(location.hash)] ?? all.find((n) => !n.matches('.section-link')) ?? all[0]; // «Ver todo» va antes que las tarjetas, pero no es lo principal
  };
  const land = (n) => {
    n.focus({ preventScroll: true });
    // Lo que cabe con la página arriba del todo se muestra así (con el título); lo demás, al centro: en el borde
    // quedaría bajo la barra de arriba.
    if (view.contains(n) && n.getBoundingClientRect().bottom + scrollY < innerHeight - 24) scrollTo(0, 0);
    else n.scrollIntoView({ block: 'center', inline: 'center' });
  };
  // La página se pinta por partes (una fila puede llegar después y quedar encima): mientras no se haya movido el foco
  // con el control se vuelve a poner en su sitio. Después solo si se perdió, y nunca se le quita a la barra de arriba.
  new MutationObserver(() => {
    const at = document.activeElement;
    if (modals().length || (at !== document.body && (moved || !view.contains(at)))) return;
    const n = home();
    if (n && n !== at) land(n);
  }).observe(view, { childList: true, subtree: true });
  document.addEventListener('keydown', (e) => {
    if (!AXIS[e.key] || e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey) return; // defaultPrevented: menús y cola ya usan las flechas
    const at = document.activeElement;
    const field = at?.matches('input:not([type=checkbox], [type=radio], [type=button], [type=submit]), textarea, select');
    const x = AXIS[e.key][0] === 'x';
    if (x && (field || at?.closest('#viewer'))) return; // izquierda y derecha: deslizador, texto y pasar de foto
    const root = [...document.querySelectorAll('dialog[open]')].findLast((d) => d.matches(':modal')) ?? document;
    const all = [...root.querySelectorAll(SEL)].filter((n) => n !== at && usable(n));
    const rects = all.map((n) => n.getBoundingClientRect());
    const here = at && at !== document.body && root.contains(at) && usable(at);
    // seen: ¿se ve de verdad? El contenido pasa por debajo del reproductor y del encabezado, que están fijos.
    const seen = (j) => sized(rects[j]) && all[j].contains(document.elementFromPoint((rects[j].left + rects[j].right) / 2, (rects[j].top + rects[j].bottom) / 2));
    const among = (keep, strict) => { // pick entre los candidatos que cumplen `keep`
      const ids = all.map((_, j) => j).filter(keep);
      return ids[pick(at.getBoundingClientRect(), ids.map((j) => rects[j]), e.key, strict)] ?? -1;
    };
    if (!here && root === document) { // sin foco (se cerró un panel cuyo botón ya no está): de vuelta a la página
      const n = home() ?? all.find((_, j) => seen(j));
      if (!n) return;
      e.preventDefault();
      return land(n);
    }
    let i;
    if (here) {
      // Cada zona se recorre por dentro (el contenido se desplaza) y solo al llegar a su borde se pasa a otra, y ahí
      // únicamente a lo que está a la vista. Sin esto el foco saltaba entre el reproductor y las tarjetas que tapa.
      const zone = at.closest(ZONES);
      // A un deslizador solo se llega subiendo o bajando: de lado sería una trampa, porque ahí las flechas lo mueven.
      const fits = (j) => !(x && all[j].matches('input[type=range]'));
      const inside = (j) => fits(j) && all[j].closest(ZONES) === zone;
      const outside = (j) => fits(j) && all[j].closest(ZONES) !== zone && seen(j);
      // Primero lo alineado, en la zona y después fuera de ella; en diagonal solo al subir o bajar, y como último
      // recurso: desde la primera fila, arriba es la barra y no un «Ver todo» que quedó a un lado. De lado nunca se
      // salta en diagonal: del contenido no se cae a la barra.
      i = among(inside, true);
      if (i < 0) i = among(outside, true);
      if (i < 0 && !x) i = among(inside, false);
      if (i < 0 && !x) i = among(outside, false);
    } else i = all.findIndex((_, j) => seen(j)); // un diálogo sin foco: lo primero que tenga a la vista
    const ok = i >= 0 && sized(rects[i]);
    if (ok || field) e.preventDefault(); // sin destino la página se desplaza como siempre, pero un deslizador no cambia de valor
    if (!ok) return;
    land(all[i]);
    moved = true;
    if (view.contains(all[i])) last.set(location.hash, items().indexOf(all[i]));
  });
}
