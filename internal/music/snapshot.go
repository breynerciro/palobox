package music

// Instantánea del índice: Inicio y Buscar leían y agregaban toda la tabla tracks en cada petición,
// y con miles de canciones eso saturaba la CPU. Lo que no depende del usuario se calcula una vez por
// cambio del índice; lo del usuario (escuchas, guardados) se sigue pidiendo a SQLite y se combina.

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"palobox/internal/httpx"
	"palobox/internal/natsort"
)

// snapshot es inmutable: un cambio publica otra y las peticiones en curso siguen con la que tomaron.
type snapshot struct {
	genre    map[string]string // artista (carpeta, sin tildes ni mayúsculas) → «familia/género» (genres.go)
	tracks   []track           // por título, con su texto de búsqueda
	fresh    []track           // las 40 más nuevas
	albums   []album           // por nombre, sin datos del usuario
	artists  []artist
	albumAt  map[string]int // carpeta -> posición en albums
	artistAt map[string]int
	gen      string // huella de catalog: la misma biblioteca da la misma URL, aunque la app se reinicie
	catalog  []byte // {albums, artists} en JSON, ya comprimido con gzip
}

func (l *Library) build() (*snapshot, error) {
	s := &snapshot{albumAt: map[string]int{}, artistAt: map[string]int{}}
	var err error
	if s.tracks, err = l.tracks(`ORDER BY t.title`); err != nil {
		return nil, err
	}
	if s.genre, err = l.genres(); err != nil {
		return nil, err
	}
	for i := range s.tracks {
		t := &s.tracks[i]
		t.text, t.titleText = normOf(t.Title+" "+t.Artist+" "+t.Album), normOf(t.Title)
	}
	if s.fresh, err = l.tracks(`ORDER BY t.mtime DESC, t.path LIMIT 40`); err != nil {
		return nil, err
	}
	rows, err := l.DB.Query(`SELECT album_dir, MIN(album), artist_dir, MAX(year), COUNT(*), SUM(dur), MAX(mtime) FROM tracks GROUP BY album_dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s.albums = []album{}
	for rows.Next() {
		var al album
		if err := rows.Scan(&al.Path, &al.Name, &al.ArtistPath, &al.Year, &al.Tracks, &al.Dur, &al.Added); err != nil {
			return nil, err
		}
		al.Artist = al.ArtistPath
		al.text = normOf(al.Name + " " + al.Artist)
		s.albums = append(s.albums, al)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(s.albums, func(i, j int) bool { return natsort.Less(fold(s.albums[i].Name), fold(s.albums[j].Name)) })
	s.artists = artistsOf(s.albums)
	for i, al := range s.albums {
		s.albumAt[al.Path] = i
	}
	for i := range s.artists {
		s.artists[i].text = normOf(s.artists[i].Name)
		s.artists[i].Genre = s.genre[fold(s.artists[i].Path)]
		s.artistAt[s.artists[i].Path] = i
	}
	// Se comprime una vez por instantánea, al máximo: cada /api/music repetía 2 MB de JSON y su gzip.
	raw, err := json.Marshal(map[string]any{"albums": s.albums, "artists": s.artists})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	s.gen = hex.EncodeToString(sum[:8])
	var b bytes.Buffer
	z, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	z.Write(raw)
	if err := z.Close(); err != nil {
		return nil, err
	}
	s.catalog = b.Bytes()
	return s, nil
}

// Catalog entrega álbumes y artistas sin nada del usuario: son iguales para todos y solo cambian con el
// índice, así que el navegador los guarda hasta que /api/music anuncie otra versión (?v=).
func (l *Library) Catalog(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	s, err := l.snap()
	if err != nil {
		return err
	}
	h := w.Header()
	if r.URL.Query().Get("v") != s.gen { // una versión vieja no se responde: quedaría guardada como si fuera esa
		h.Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/api/music/catalog?v="+s.gen, http.StatusFound)
		return nil
	}
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("Vary", "Accept-Encoding")
	if httpx.AcceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		h.Set("Content-Length", strconv.Itoa(len(s.catalog)))
		_, err = w.Write(s.catalog)
		return err
	}
	zr, err := gzip.NewReader(bytes.NewReader(s.catalog)) // casi ningún cliente llega aquí: no vale guardar otra copia
	if err != nil {
		return err
	}
	_, err = io.Copy(w, zr)
	return err
}

// snap devuelve la instantánea vigente; la primera petición la arma.
func (l *Library) snap() (*snapshot, error) {
	if s := l.snapshot.Load(); s != nil {
		return s, nil
	}
	l.snapMu.Lock() // con 200 usuarios abriendo a la vez, que solo uno la arme
	defer l.snapMu.Unlock()
	if s := l.snapshot.Load(); s != nil {
		return s, nil
	}
	s, err := l.build()
	if err != nil {
		return nil, err
	}
	l.snapshot.Store(s)
	return s, nil
}

// rebuild publica una instantánea nueva tras un cambio en la tabla tracks.
func (l *Library) rebuild() {
	l.snapMu.Lock()
	defer l.snapMu.Unlock()
	s, err := l.build()
	if err != nil { // que la próxima petición lo reintente en vez de servir una vieja
		log.Print("instantánea de música: ", err)
	}
	l.snapshot.Store(s)
}

// mine es lo de un usuario sobre un álbum o artista; /api/music lo manda solo donde hay algo.
type mine struct {
	Played int64 `json:"played"`
	Plays  int   `json:"plays"`
	Saved  int64 `json:"saved"`
}

// userData devuelve lo que u ha escuchado y guardado, por carpeta de álbum y de artista de la instantánea.
func (l *Library) userData(s *snapshot, u httpx.User) (albums, artists map[string]mine, err error) {
	albums, artists = map[string]mine{}, map[string]mine{}
	rows, err := l.DB.Query(`SELECT t.album_dir, MAX(p.at), COUNT(*) FROM plays p JOIN tracks t ON t.path = p.path WHERE p.user_id = ? GROUP BY t.album_dir`, u.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dir string
		var last int64
		var plays int
		rows.Scan(&dir, &last, &plays)
		i, ok := s.albumAt[dir]
		if !ok { // la escucha es de algo que la instantánea aún no tiene
			continue
		}
		m := albums[dir]
		m.Played, m.Plays = last, plays
		albums[dir] = m
		if ar := s.albums[i].ArtistPath; ar != "" {
			m := artists[ar]
			m.Plays += plays
			m.Played = max(m.Played, last)
			artists[ar] = m
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for _, k := range []struct {
		kind string
		at   map[string]int
		out  map[string]mine
	}{{"album", s.albumAt, albums}, {"artist", s.artistAt, artists}} {
		saved, err := l.savedOf(u, k.kind)
		if err != nil {
			return nil, nil, err
		}
		for dir, when := range saved {
			if _, ok := k.at[dir]; ok {
				m := k.out[dir]
				m.Saved = when
				k.out[dir] = m
			}
		}
	}
	return albums, artists, nil
}

// library devuelve los álbumes y artistas de la instantánea con lo que u ha escuchado y guardado.
func (l *Library) library(u httpx.User) (*snapshot, []album, []artist, error) {
	s, err := l.snap()
	if err != nil {
		return nil, nil, nil, err
	}
	ma, mr, err := l.userData(s, u)
	if err != nil {
		return nil, nil, nil, err
	}
	albums, artists := slices.Clone(s.albums), slices.Clone(s.artists)
	for dir, m := range ma {
		al := &albums[s.albumAt[dir]]
		al.Played, al.Plays, al.Saved = m.Played, m.Plays, m.Saved
	}
	for dir, m := range mr {
		ar := &artists[s.artistAt[dir]]
		ar.Played, ar.Plays, ar.Saved = m.Played, m.Plays, m.Saved
	}
	return s, albums, artists, nil
}

// Random devuelve hasta n pistas para seguir después de seed (una ruta): primero al azar entre las de su
// género (hasta la mitad), luego entre las de su familia, y de toda la biblioteca solo si no alcanzan. Sin
// seed, o si su artista no tiene género, todas salen al azar. Reparte: dos por artista mientras haya de dónde.
func (l *Library) Random(n int, seed string) ([]Track, error) {
	s, err := l.snap()
	if err != nil {
		return nil, err
	}
	return s.similar(n, seed, nil), nil
}

// similar es Random sobre las pistas que cumplen keep (nil = todas).
func (s *snapshot) similar(n int, seed string, keep func(t *track) bool) []Track {
	out, used, perArtist := []Track{}, map[int]bool{}, map[string]int{}
	perm := rand.Perm(len(s.tracks))
	fill := func(max int, ok func(genre string) bool) {
		for _, each := range []int{2, len(perm)} { // sin el tope, un artista con muchas canciones llena la cola
			for _, i := range perm {
				if len(out) >= max {
					return
				}
				t := s.tracks[i]
				if !used[i] && t.Path != seed && perArtist[t.ArtistPath] < each && ok(s.genreOf(t.Path)) && (keep == nil || keep(&t)) {
					used[i] = true
					perArtist[t.ArtistPath]++
					out = append(out, t)
				}
			}
		}
	}
	if g := s.genreOf(seed); g != "" {
		family, _, _ := strings.Cut(g, "/")
		fill(n/2, func(x string) bool { return x == g })
		fill(n, func(x string) bool { return strings.HasPrefix(x, family+"/") })
	}
	fill(n, func(string) bool { return true })
	return out
}
