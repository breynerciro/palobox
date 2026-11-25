// Package music es la música como app de streaming: índice de pistas con etiquetas, me gusta, playlists e historial.
// Álbum = carpeta que contiene las pistas; artista = carpeta de primer nivel. Las etiquetas solo
// mejoran los nombres: sin ellas todo sale del nombre de archivo y de carpeta.
// shortcut: un álbum repartido en subcarpetas (CD1, CD2) sale como álbumes separados; unirlos por etiqueta si molesta.
package music

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"palobox/internal/httpx"
	"palobox/internal/media"
	"palobox/internal/natsort"
)

type track struct {
	Path       string  `json:"path"`
	Title      string  `json:"title"`
	Artist     string  `json:"artist"`
	Album      string  `json:"album"`
	AlbumPath  string  `json:"albumPath"`
	ArtistPath string  `json:"artistPath"`
	Year       int     `json:"year"`
	Num        int     `json:"num"`
	Dur        float64 `json:"dur"` // segundos; 0 = desconocida
	disc       int
	genre      string
	text       norm // título, artista y álbum, para Buscar; solo en la instantánea
	titleText  norm
}

// Track es una pista del índice tal como la recibe la interfaz (para otros paquetes, como jam).
type Track = track

// ByPaths devuelve las pistas del índice en el orden pedido (con repetidas); las que no existen se omiten.
func (l *Library) ByPaths(paths []string) ([]Track, error) {
	if len(paths) == 0 {
		return []Track{}, nil
	}
	args := make([]any, len(paths))
	for i, p := range paths {
		args[i] = p
	}
	ts, err := l.tracks(`WHERE t.path IN (?`+strings.Repeat(",?", len(paths)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	by := make(map[string]Track, len(ts))
	for _, t := range ts {
		by[t.Path] = t
	}
	out := make([]Track, 0, len(paths))
	for _, p := range paths {
		if t, ok := by[p]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

type album struct {
	Path       string  `json:"path"`
	Name       string  `json:"name"`
	Artist     string  `json:"artist"`
	ArtistPath string  `json:"artistPath"`
	Year       int     `json:"year"`
	Tracks     int     `json:"tracks"`
	Dur        float64 `json:"dur"`
	Added      int64   `json:"added"`  // ms; fecha del archivo más nuevo
	Played     int64   `json:"played"` // ms; última vez que este usuario lo escuchó
	Plays      int     `json:"plays"`
	Saved      int64   `json:"saved"` // ms; cuándo lo guardó este usuario en Tu biblioteca, 0 = no
	text       norm
}

type artist struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Albums int    `json:"albums"`
	Tracks int    `json:"tracks"`
	Added  int64  `json:"added"`
	Played int64  `json:"played"`
	Plays  int    `json:"plays"`
	Saved  int64  `json:"saved"` // ms; cuándo lo empezó a seguir este usuario, 0 = no
	text   norm
}

func (l *Library) musicRoot() string { return filepath.Join(l.Data, "media", "musica") }

// musicRel valida que rel sea una pista real de la biblioteca, escrita en su forma canónica.
func (l *Library) musicRel(rel string) (string, error) {
	root := l.musicRoot()
	p := httpx.Safe(root, rel)
	if st, err := os.Stat(p); err != nil || st.IsDir() || !media.IsAudio(p) {
		return "", fs.ErrNotExist
	}
	// Con ".." o barras de más la ruta limpia difiere de la pedida: no se guarda nada que no sea exacto.
	if clean, _ := filepath.Rel(root, p); filepath.ToSlash(clean) != rel {
		return "", fs.ErrNotExist
	}
	return rel, nil
}

const trackCols = `t.path, t.title, t.artist, t.album, t.album_dir, t.artist_dir, t.year, t.num, t.dur`
const albumOrder = ` ORDER BY t.album_dir, t.disc, t.num, t.path`

// tracks consulta pistas del índice; tail es el resto del SELECT (JOIN, WHERE, ORDER).
func (l *Library) tracks(tail string, args ...any) ([]track, error) {
	rows, err := l.DB.Query(`SELECT `+trackCols+` FROM tracks t `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []track{}
	for rows.Next() {
		var t track
		if err := rows.Scan(&t.Path, &t.Title, &t.Artist, &t.Album, &t.AlbumPath, &t.ArtistPath, &t.Year, &t.Num, &t.Dur); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// artistsOf agrupa los álbumes por artista; lo del usuario lo suma library.
func artistsOf(albums []album) []artist {
	at := map[string]int{}
	out := []artist{}
	for _, al := range albums {
		if al.ArtistPath == "" { // pistas sueltas en la raíz
			continue
		}
		i, ok := at[al.ArtistPath]
		if !ok {
			i = len(out)
			at[al.ArtistPath] = i
			out = append(out, artist{Path: al.ArtistPath, Name: al.ArtistPath})
		}
		ar := &out[i]
		ar.Albums++
		ar.Tracks += al.Tracks
		ar.Plays += al.Plays
		ar.Added, ar.Played = max(ar.Added, al.Added), max(ar.Played, al.Played)
	}
	sort.Slice(out, func(i, j int) bool { return natsort.Less(fold(out[i].Name), fold(out[j].Name)) })
	return out
}

// Lib entrega de una vez lo que necesitan Inicio, Tu biblioteca y la barra lateral (web/music.js library()).
func (l *Library) Lib(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	l.fresh()
	s, err := l.snap()
	if err != nil {
		return err
	}
	albums, artists, err := l.userData(s, u)
	if err != nil {
		return err
	}
	pls, err := l.playlists(u)
	if err != nil {
		return err
	}
	top, err := l.tracks(`JOIN (SELECT path, COUNT(*) c, MAX(at) m FROM plays WHERE user_id = ? GROUP BY path) p ON p.path = t.path
		ORDER BY p.c DESC, p.m DESC LIMIT 20`, u.ID)
	if err != nil {
		return err
	}
	// Inicio va por canciones, no por álbumes: casi todo llega de playlists de Spotify, una canción por álbum.
	recent, err := l.tracks(`JOIN (SELECT path, MAX(rowid) r FROM plays WHERE user_id = ? GROUP BY path) p ON p.path = t.path
		ORDER BY p.r DESC LIMIT 20`, u.ID)
	if err != nil {
		return err
	}
	likes := []string{}
	rows, err := l.DB.Query(`SELECT l.path FROM likes l JOIN tracks t ON t.path = l.path WHERE l.user_id = ?`, u.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		rows.Scan(&p)
		likes = append(likes, p)
	}
	out := map[string]any{"playlists": pls, "likes": likes, "top": top, "recent": recent, "fresh": s.fresh}
	if r.URL.Query().Has("catalog") {
		// Álbumes y artistas van aparte, en /api/music/catalog: aquí solo lo de este usuario.
		out["catalog"], out["mine"] = s.gen, map[string]any{"albums": albums, "artists": artists}
		return httpx.WriteJSON(w, 200, out)
	}
	// Una interfaz anterior (abierta desde antes de actualizar) espera la biblioteca completa en esta respuesta.
	if _, out["albums"], out["artists"], err = l.library(u); err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, out)
}

func (l *Library) Album(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	l.fresh()
	dir := r.URL.Query().Get("path")
	ts, err := l.tracks(`WHERE t.album_dir = ?`+albumOrder, dir)
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		return fs.ErrNotExist
	}
	year := 0
	for _, t := range ts {
		year = max(year, t.Year)
	}
	saved, err := l.savedOf(u, "album")
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, map[string]any{"saved": saved[dir], "path": dir, "name": ts[0].Album, "artist": ts[0].ArtistPath, "artistPath": ts[0].ArtistPath, "year": year, "tracks": ts})
}

func (l *Library) Artist(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	l.fresh()
	dir := r.URL.Query().Get("path")
	ts, err := l.tracks(`WHERE t.artist_dir = ? ORDER BY t.year DESC, t.album_dir, t.disc, t.num, t.path`, dir)
	if err != nil {
		return err
	}
	if len(ts) == 0 || dir == "" {
		return fs.ErrNotExist
	}
	// Sin historial todas empatan en cero y quedan las primeras pistas.
	popular, err := l.tracks(`WHERE t.artist_dir = ? ORDER BY (SELECT COUNT(*) FROM plays p WHERE p.user_id = ? AND p.path = t.path) DESC,
		t.year DESC, t.album_dir, t.disc, t.num, t.path LIMIT 5`, dir, u.ID)
	if err != nil {
		return err
	}
	_, all, _, err := l.library(u)
	if err != nil {
		return err
	}
	mine := []album{}
	for _, al := range all {
		if al.ArtistPath == dir {
			mine = append(mine, al)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].Year > mine[j].Year })
	followed, err := l.savedOf(u, "artist")
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, map[string]any{"saved": followed[dir], "path": dir, "name": dir, "albums": mine, "tracks": ts, "popular": popular})
}

// Radio sugiere qué sigue cuando se acaba la cola: pistas parecidas a ?path= (ver Random); sin él, al azar.
func (l *Library) Radio(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	l.fresh()
	ts, err := l.Random(40, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, ts)
}

// Me gusta e historial

func (l *Library) Likes(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	l.fresh()
	ts, err := l.tracks(`JOIN likes l ON l.path = t.path WHERE l.user_id = ? ORDER BY l.at DESC, l.rowid DESC`, u.ID)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, ts)
}

func (l *Library) LikePut(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	rel, err := l.musicRel(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	_, err = l.DB.Exec(`INSERT OR IGNORE INTO likes(user_id, path, at) VALUES(?, ?, ?)`, u.ID, rel, time.Now().UnixMilli())
	return err
}

// likeDelete no valida contra el disco: debe poder quitarse el me gusta de una pista que ya no existe.
func (l *Library) LikeDelete(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	_, err := l.DB.Exec(`DELETE FROM likes WHERE user_id = ? AND path = ?`, u.ID, r.URL.Query().Get("path"))
	return err
}

// played registra una escucha; la interfaz la envía a los 30 s o al terminar la pista.
func (l *Library) Played(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	var in struct{ Path string }
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return err
	}
	rel, err := l.musicRel(in.Path)
	if err != nil {
		return err
	}
	// Con la música sonando en dos dispositivos de la cuenta llegan dos escuchas seguidas: cuenta una.
	_, err = l.DB.Exec(`INSERT INTO plays(user_id, path, at) SELECT ?1, ?2, ?3
		WHERE NOT EXISTS(SELECT 1 FROM plays WHERE user_id = ?1 AND path = ?2 AND at > ?3 - 30000)`, u.ID, rel, time.Now().UnixMilli())
	return err
}

// Skipped registra que el usuario pasó a otra canción antes de los 30 s (auto: era una sugerencia, no algo que
// él puso). Es la señal para medir si las sugerencias aciertan.
// shortcut: por ahora solo se guarda; cuando haya historial, que Random evite lo que más se salta.
func (l *Library) Skipped(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	var in struct {
		Path string
		Auto bool
	}
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return err
	}
	rel, err := l.musicRel(in.Path)
	if err != nil {
		return err
	}
	_, err = l.DB.Exec(`INSERT INTO skips(user_id, path, at, auto) VALUES(?, ?, ?, ?)`, u.ID, rel, time.Now().UnixMilli(), in.Auto)
	return err
}

// Tu biblioteca: álbumes guardados y artistas seguidos, por usuario. Lo demás de la
// biblioteca compartida se ve en Inicio y Buscar, pero no aparece en Tu biblioteca.

// savedOf devuelve lo que u guardó de un tipo ("album" o "artist"): carpeta -> cuándo (ms).
func (l *Library) savedOf(u httpx.User, kind string) (map[string]int64, error) {
	rows, err := l.DB.Query(`SELECT path, at FROM saved WHERE user_id = ? AND kind = ?`, u.ID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var p string
		var at int64
		if err := rows.Scan(&p, &at); err != nil {
			return nil, err
		}
		out[p] = at
	}
	return out, rows.Err()
}

// savedKind lee ?kind= y devuelve la columna del índice donde vive esa carpeta.
func savedKind(r *http.Request) (kind, col string, err error) {
	switch kind = r.URL.Query().Get("kind"); kind {
	case "album":
		return kind, "album_dir", nil
	case "artist":
		return kind, "artist_dir", nil
	}
	return "", "", httpx.Error(400, "tipo inválido")
}

// SavePut guarda un álbum o sigue a un artista; solo carpetas que existen en el índice.
func (l *Library) SavePut(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	kind, col, err := savedKind(r)
	if err != nil {
		return err
	}
	path := r.URL.Query().Get("path")
	if path == "" { // pistas sueltas en la raíz: no son un álbum ni un artista
		return fs.ErrNotExist
	}
	var one int
	if err := l.DB.QueryRow(`SELECT 1 FROM tracks WHERE `+col+` = ? LIMIT 1`, path).Scan(&one); err != nil {
		return err
	}
	_, err = l.DB.Exec(`INSERT OR IGNORE INTO saved(user_id, kind, path, at) VALUES(?, ?, ?, ?)`, u.ID, kind, path, time.Now().UnixMilli())
	return err
}

// SaveDelete no valida contra el índice: debe poder quitarse algo que ya no está en el disco.
func (l *Library) SaveDelete(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	kind, _, err := savedKind(r)
	if err != nil {
		return err
	}
	_, err = l.DB.Exec(`DELETE FROM saved WHERE user_id = ? AND kind = ? AND path = ?`, u.ID, kind, r.URL.Query().Get("path"))
	return err
}
