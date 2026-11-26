// pick (web/tv.js): a dónde salta el foco con cada flecha del control remoto del televisor.
import test from 'node:test';
import assert from 'node:assert/strict';

const { pick } = await import('../web/tv.js');
const r = (left, top, w = 100, h = 50) => ({ left, top, right: left + w, bottom: top + h });

test('salta al vecino alineado antes que a uno más cercano en diagonal', () => {
  const from = r(0, 0);
  const rects = [r(300, 0), r(110, 60), r(0, 200), r(-200, 0)]; // derecha lejos, diagonal cerca, abajo, izquierda
  assert.equal(pick(from, rects, 'ArrowRight'), 0);
  assert.equal(pick(from, rects, 'ArrowDown'), 2);
  assert.equal(pick(from, rects, 'ArrowLeft'), 3);
  assert.equal(pick(from, rects, 'ArrowUp'), -1); // nada arriba: el foco se queda
});

test('entre dos alineados gana el más cercano', () => {
  assert.equal(pick(r(0, 0), [r(0, 300), r(0, 100)], 'ArrowDown'), 1);
});

test('en diagonal no salta a algo que apenas sobresale hacia ese lado', () => {
  const more = r(1858, 420, 40, 40); // último botón de una fila; el de cerrar sesión queda arriba y un poco a la derecha
  assert.equal(pick(more, [r(1870, 10, 40, 40)], 'ArrowRight'), -1);
  assert.equal(pick(more, [r(1870, 10, 40, 40)], 'ArrowUp'), 0);
});

test('con strict solo vale lo alineado', () => {
  assert.equal(pick(r(0, 0), [r(300, 200)], 'ArrowRight'), 0);
  assert.equal(pick(r(0, 0), [r(300, 200)], 'ArrowRight', true), -1);
  assert.equal(pick(r(0, 0), [r(300, 200), r(600, 10)], 'ArrowRight', true), 1);
});
