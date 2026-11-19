package music

// Instantánea del índice: Inicio y Buscar leían y agregaban toda la tabla tracks en cada petición,
// y con miles de canciones eso saturaba la CPU. Lo que no depende del usuario se calcula una vez por
// cambio del índice; lo del usuario (escuchas, guardados) se sigue pidiendo a SQLite y se combina.

import (
	"log"
	"math/rand/v2"
	"slices"
	"sort"

	"palobox/internal/httpx"
	"palobox/internal/natsort"
)

// snapshot es inmutable: un cambio publica otra y las peticiones en curso siguen con la que tomaron.
type snapshot struct {
	tracks   []track // por título, con su texto de búsqueda
	fresh    []track // las 40 más nuevas
	albums   []album // por nombre, sin datos del usuario
	artists  []artist
	albumAt  map[string]int // carpeta -> posición en albums
	artistAt map[string]int
}

func (l *Library) build() (*snapshot, error) {
	s := &snapshot{albumAt: map[string]int{}, artistAt: map[string]int{}}
	var err error
	if s.tracks, err = l.tracks(`ORDER BY t.title`); err != nil {
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
		s.artistAt[s.artists[i].Path] = i
	}
	return s, nil
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

// library devuelve los álbumes y artistas de la instantánea con lo que u ha escuchado y guardado.
func (l *Library) library(u httpx.User) (*snapshot, []album, []artist, error) {
	s, err := l.snap()
	if err != nil {
		return nil, nil, nil, err
	}
	type stat struct {
		last  int64
		plays int
	}
	heard := map[string]stat{}
	rows, err := l.DB.Query(`SELECT t.album_dir, MAX(p.at), COUNT(*) FROM plays p JOIN tracks t ON t.path = p.path WHERE p.user_id = ? GROUP BY t.album_dir`, u.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var dir string
		var h stat
		rows.Scan(&dir, &h.last, &h.plays)
		heard[dir] = h
	}
	rows.Close()
	saved, err := l.savedOf(u, "album")
	if err != nil {
		return nil, nil, nil, err
	}
	followed, err := l.savedOf(u, "artist")
	if err != nil {
		return nil, nil, nil, err
	}
	albums, artists := slices.Clone(s.albums), slices.Clone(s.artists)
	for dir, h := range heard {
		i, ok := s.albumAt[dir]
		if !ok { // la escucha es de algo que la instantánea aún no tiene
			continue
		}
		albums[i].Played, albums[i].Plays = h.last, h.plays
		if j, ok := s.artistAt[albums[i].ArtistPath]; ok {
			artists[j].Plays += h.plays
			artists[j].Played = max(artists[j].Played, h.last)
		}
	}
	for dir, at := range saved {
		if i, ok := s.albumAt[dir]; ok {
			albums[i].Saved = at
		}
	}
	for dir, at := range followed {
		if j, ok := s.artistAt[dir]; ok {
			artists[j].Saved = at
		}
	}
	return s, albums, artists, nil
}

// Random devuelve hasta n pistas al azar de toda la biblioteca, menos not.
func (l *Library) Random(n int, not string) ([]Track, error) {
	s, err := l.snap()
	if err != nil {
		return nil, err
	}
	out := []Track{}
	for _, i := range rand.Perm(len(s.tracks)) {
		if len(out) == n {
			break
		}
		if s.tracks[i].Path != not {
			out = append(out, s.tracks[i])
		}
	}
	return out, nil
}
