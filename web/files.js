// Archivos: carpetas del usuario.
import { $, el, icon, json, size, fmtDate, plural, join, joinText, fileType, typeBadge, toast, ask, empty, uploadButton } from './lib.js';
import { route, href, go, rawUrl, render } from './nav.js';
import { api } from './session.js';

$('#mkdir').onclick = async () => {
  const name = await ask({ heading: 'Nueva carpeta', input: '', ok: 'Crear' });
  if (!name) return;
  const r = await api('/api/files/mkdir', json('POST', { path: join(route().path, name.replaceAll('/', '-')) }));
  if (!r.ok) return toast('No se pudo crear la carpeta.', { kind: 'warn' });
  render();
};

export async function filesView(path) {
  const r = await api(`/api/files?path=${encodeURIComponent(path)}`);
  if (r.status === 404 && path) { go('archivos', ''); return document.createDocumentFragment(); } // la carpeta ya no existe
  if (!r.ok) throw new Error('Intenta de nuevo en un momento.');
  const items = await r.json();
  if (!items.length) return empty('folder', path ? 'Carpeta vacía' : 'Aún no hay archivos', 'Sube documentos o cualquier archivo que quieras guardar.', uploadButton());
  const dirs = items.filter((i) => i.dir).length;
  $('#subtitle').textContent = joinText(dirs && plural(dirs, 'carpeta', 'carpetas'), items.length - dirs && plural(items.length - dirs, 'archivo', 'archivos'));
  return el('ul', { className: 'list' }, items.map((it) => fileRow(it, join(path, it.name))));
}

function fileRow(it, path) {
  const t = it.dir ? 'folder' : fileType(it.name);
  const info = it.dir ? 'Carpeta' : `${size(it.size)} · ${fmtDate(it.mtime)}`;
  const main = it.dir
    ? el('a', { className: 'row', href: href('archivos', path) }, typeBadge(t),
        el('div', { className: 'row-text' }, el('p', { textContent: it.name }), el('p', { className: 'muted', textContent: info })), icon('next'))
    : el('a', { className: 'row', href: rawUrl('archivos', path) }, typeBadge(t),
        el('div', { className: 'row-text' }, el('p', { textContent: it.name }), el('p', { className: 'muted', textContent: info })));
  const del = el('button', { className: 'icon-btn' }, icon('trash'));
  del.setAttribute('aria-label', `Eliminar ${it.name}`);
  del.onclick = async () => {
    const ok = await ask({ heading: `¿Eliminar «${it.name}»?`, text: it.dir ? 'Se borra la carpeta con todo su contenido.' : 'Esta acción no se puede deshacer.', ok: 'Eliminar', danger: true });
    if (!ok) return;
    const r = await api(`/api/files?path=${encodeURIComponent(path)}`, { method: 'DELETE' });
    if (!r.ok) return toast('No se pudo eliminar.', { kind: 'warn' });
    render();
  };
  return el('li', { className: 'row-wrap' }, main, del);
}
