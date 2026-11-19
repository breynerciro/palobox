// withCatalog (web/catalog.js): catálogo común más lo del usuario da la misma biblioteca que /api/music completo.
import test from 'node:test';
import assert from 'node:assert/strict';

const { withCatalog } = await import('../web/catalog.js');

test('combina el catálogo con lo del usuario sin tocar el catálogo', () => {
  const al = (path, artistPath) => ({ path, name: path, artist: artistPath, artistPath, year: 2020, tracks: 1, dur: 3, added: 5, played: 0, plays: 0, saved: 0 });
  const ar = (path) => ({ path, name: path, albums: 1, tracks: 1, added: 5, played: 0, plays: 0, saved: 0 });
  const cat = { albums: [al('A/Uno', 'A'), al('B/Dos', 'B'), al('', '')], artists: [ar('A'), ar('B')] };
  const frozen = structuredClone(cat);
  const d = { catalog: 'abc', mine: { albums: { 'A/Uno': { played: 9, plays: 2, saved: 0 }, '': { played: 4, plays: 1, saved: 0 } }, artists: { B: { played: 0, plays: 0, saved: 7 } } },
    playlists: [], likes: ['x'], top: [], recent: [], fresh: [] };
  const lib = withCatalog(d, cat);
  assert.deepEqual(lib, {
    playlists: [], likes: ['x'], top: [], recent: [], fresh: [],
    albums: [{ ...al('A/Uno', 'A'), played: 9, plays: 2 }, al('B/Dos', 'B'), { ...al('', ''), played: 4, plays: 1 }],
    artists: [ar('A'), { ...ar('B'), saved: 7 }],
  });
  assert.deepEqual(Object.keys(lib.albums[0]), Object.keys(cat.albums[0])); // mismo orden de campos que la respuesta vieja
  assert.deepEqual(cat, frozen);
});
