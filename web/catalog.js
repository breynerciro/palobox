// withCatalog arma la respuesta completa de /api/music: álbumes y artistas del catálogo común (que el
// navegador guarda en caché) con lo de este usuario (escuchas, guardados) donde lo haya.
export function withCatalog(d, cat) {
  const { catalog, mine, ...rest } = d;
  const put = (list, own) => list.map((x) => ({ ...x, ...own[x.path] })); // copias: el catálogo se reutiliza entre llamadas
  return { ...rest, albums: put(cat.albums, mine.albums), artists: put(cat.artists, mine.artists) };
}
