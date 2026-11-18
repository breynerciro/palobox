// Package music es la música como app de streaming: índice de pistas con etiquetas, me gusta, playlists e historial.
// Álbum = carpeta que contiene las pistas; artista = carpeta de primer nivel. Las etiquetas solo
// mejoran los nombres: sin ellas todo sale del nombre de archivo y de carpeta.
// shortcut: un álbum repartido en subcarpetas (CD1, CD2) sale como álbumes separados; unirlos por etiqueta si molesta.
package music

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
}

type playlist struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Updated int64  `json:"updated"`
	Tracks  int    `json:"tracks"`
	Cover   string `json:"cover"` // carpeta del álbum de su primera pista
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

// Índice de pistas

var leadNum = regexp.MustCompile(`^(\d{1,3})[\s._-]+(.+)$`)

func leadInt(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	v, _ := strconv.Atoi(s[:n])
	return v
}

// fromPath deduce los datos de una pista de su ruta: Artista / Álbum / 01 - Título.mp3.
func fromPath(rel string) track {
	parts := strings.Split(rel, "/")
	t := track{Path: rel, Title: strings.TrimSuffix(parts[len(parts)-1], path.Ext(rel)), Album: "Sin álbum"}
	if m := leadNum.FindStringSubmatch(t.Title); m != nil {
		t.Num, t.Title = leadInt(m[1]), m[2]
	}
	if len(parts) > 1 {
		t.AlbumPath, t.ArtistPath = path.Dir(rel), parts[0]
		t.Album, t.Artist = parts[len(parts)-2], parts[0]
	}
	return t
}

// applyTags pisa los datos de la ruta con las etiquetas que ffprobe haya encontrado.
func applyTags(t *track, probe []byte) {
	var p struct {
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if json.Unmarshal(probe, &p) != nil {
		return
	}
	tags := map[string]string{}
	for _, s := range p.Streams { // Ogg y Opus llevan las etiquetas en la pista, no en el contenedor
		for k, v := range s.Tags {
			tags[strings.ToLower(k)] = strings.TrimSpace(v)
		}
	}
	for k, v := range p.Format.Tags {
		tags[strings.ToLower(k)] = strings.TrimSpace(v)
	}
	for k, dst := range map[string]*string{"title": &t.Title, "artist": &t.Artist, "album": &t.Album} {
		if tags[k] != "" {
			*dst = tags[k]
		}
	}
	if n := leadInt(tags["track"]); n > 0 { // "3/12"
		t.Num = n
	}
	t.disc = leadInt(tags["disc"])
	if t.Year = leadInt(tags["date"]); t.Year == 0 {
		t.Year = leadInt(tags["year"])
	}
	t.Dur, _ = strconv.ParseFloat(p.Format.Duration, 64)
}

const idxEvery = 20 * time.Second // el recorrido solo hace stat, pero no vale la pena en cada petición

// syncTracks pone el índice al día con el disco: solo pasa por ffprobe lo nuevo o cambiado.
func (l *Library) Sync() {
	if !l.idxMu.TryLock() { // ya hay un recorrido en curso: se responde con lo indexado hasta ahora
		return
	}
	defer l.idxMu.Unlock()
	if time.Since(l.idxAt) < idxEvery {
		return
	}
	root := l.musicRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		log.Print("índice de música: ", err)
		return
	}
	type stamp struct{ size, mtime int64 }
	old := map[string]stamp{}
	rows, err := l.DB.Query(`SELECT path, size, mtime FROM tracks`)
	if err != nil {
		log.Print("índice de música: ", err)
		return
	}
	for rows.Next() {
		var p string
		var s stamp
		rows.Scan(&p, &s.size, &s.mtime)
		old[p] = s
	}
	rows.Close()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != root {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !media.IsAudio(d.Name()) {
			return nil
		}
		i, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		now := stamp{i.Size(), i.ModTime().UnixMilli()}
		was, ok := old[rel]
		delete(old, rel)
		if ok && was == now {
			return nil
		}
		t := fromPath(rel)
		l.Sem <- struct{}{}
		// Un archivo corrupto no puede detener el índice.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, _ := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "a",
			"-show_entries", "format=duration:format_tags:stream_tags", "-of", "json", p).Output()
		cancel()
		<-l.Sem
		applyTags(&t, out)
		if _, err := l.DB.Exec(`INSERT OR REPLACE INTO tracks(path, size, mtime, title, artist, album, album_dir, artist_dir, year, disc, num, dur, fold)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rel, now.size, now.mtime, t.Title, t.Artist, t.Album, t.AlbumPath, t.ArtistPath,
			t.Year, t.disc, t.Num, t.Dur, fold(t.Title+" "+t.Artist+" "+t.Album)); err != nil {
			log.Print("índice de música: ", err)
		}
		return nil
	})
	if err != nil { // disco sin montar o ilegible: mejor un índice viejo que uno vacío
		log.Print("índice de música: ", err)
		return
	}
	for rel := range old {
		l.DB.Exec(`DELETE FROM tracks WHERE path = ?`, rel)
	}
	l.idxAt = time.Now()
}

// fresh actualiza el índice antes de responder, sin hacer esperar más de 2 s por una biblioteca recién copiada.
func (l *Library) fresh() {
	done := make(chan struct{})
	go func() { l.Sync(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
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

// albums agrupa el índice por carpeta y le suma lo que este usuario ha escuchado.
func (l *Library) albums(u httpx.User) ([]album, error) {
	type stat struct {
		last  int64
		plays int
	}
	heard := map[string]stat{}
	rows, err := l.DB.Query(`SELECT t.album_dir, MAX(p.at), COUNT(*) FROM plays p JOIN tracks t ON t.path = p.path WHERE p.user_id = ? GROUP BY t.album_dir`, u.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var dir string
		var s stat
		rows.Scan(&dir, &s.last, &s.plays)
		heard[dir] = s
	}
	rows.Close()
	saved, err := l.savedOf(u, "album")
	if err != nil {
		return nil, err
	}
	rows, err = l.DB.Query(`SELECT album_dir, MIN(album), artist_dir, MAX(year), COUNT(*), SUM(dur), MAX(mtime) FROM tracks GROUP BY album_dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []album{}
	for rows.Next() {
		var al album
		if err := rows.Scan(&al.Path, &al.Name, &al.ArtistPath, &al.Year, &al.Tracks, &al.Dur, &al.Added); err != nil {
			return nil, err
		}
		al.Artist, al.Played, al.Plays, al.Saved = al.ArtistPath, heard[al.Path].last, heard[al.Path].plays, saved[al.Path]
		out = append(out, al)
	}
	sort.Slice(out, func(i, j int) bool { return natsort.Less(fold(out[i].Name), fold(out[j].Name)) })
	return out, rows.Err()
}

// artistsOf agrupa los álbumes por artista; followed son los que sigue el usuario (ver savedOf).
func artistsOf(albums []album, followed map[string]int64) []artist {
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
			out = append(out, artist{Path: al.ArtistPath, Name: al.ArtistPath, Saved: followed[al.ArtistPath]})
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

func (l *Library) playlists(u httpx.User) ([]playlist, error) {
	rows, err := l.DB.Query(`SELECT p.id, p.name, p.updated, COUNT(t.path),
		COALESCE((SELECT x.album_dir FROM playlist_tracks f JOIN tracks x ON x.path = f.path WHERE f.playlist_id = p.id ORDER BY f.pos LIMIT 1), '')
		FROM playlists p LEFT JOIN playlist_tracks pt ON pt.playlist_id = p.id LEFT JOIN tracks t ON t.path = pt.path
		WHERE p.user_id = ? GROUP BY p.id ORDER BY p.updated DESC, p.id DESC`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []playlist{}
	for rows.Next() {
		var p playlist
		if err := rows.Scan(&p.ID, &p.Name, &p.Updated, &p.Tracks, &p.Cover); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// musicLib entrega de una vez lo que necesitan Inicio, Tu biblioteca y la barra lateral.
func (l *Library) Lib(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	l.fresh()
	albums, err := l.albums(u)
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
	followed, err := l.savedOf(u, "artist")
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, map[string]any{"albums": albums, "artists": artistsOf(albums, followed), "playlists": pls, "likes": likes, "top": top})
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
	all, err := l.albums(u)
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

var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// musicSearch busca en el índice: cada palabra debe aparecer, sin importar tildes ni mayúsculas.
func (l *Library) Search(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	l.fresh()
	out := map[string]any{"tracks": []track{}, "albums": []album{}, "artists": []artist{}, "playlists": []playlist{}}
	words := strings.Fields(fold(r.URL.Query().Get("q")))
	if len(strings.Join(words, "")) < 2 {
		return httpx.WriteJSON(w, 200, out)
	}
	has := func(s string) bool {
		s = fold(s)
		for _, w := range words {
			if !strings.Contains(s, w) {
				return false
			}
		}
		return true
	}
	where, args := "", []any{}
	for _, w := range words {
		where += ` AND t.fold LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscape.Replace(w)+"%")
	}
	// fold empieza por el título: lo que coincide en el título sale antes que lo que coincide en el artista.
	ts, err := l.tracks(`WHERE 1`+where+` ORDER BY instr(t.fold, ?), t.title LIMIT 30`, append(args, words[0])...)
	if err != nil {
		return err
	}
	all, err := l.albums(u)
	if err != nil {
		return err
	}
	followed, err := l.savedOf(u, "artist")
	if err != nil {
		return err
	}
	albums, artists := []album{}, []artist{}
	for _, al := range all {
		if len(albums) < 12 && has(al.Name+" "+al.Artist) {
			albums = append(albums, al)
		}
	}
	for _, ar := range artistsOf(all, followed) {
		if len(artists) < 8 && has(ar.Name) {
			artists = append(artists, ar)
		}
	}
	pls, err := l.playlists(u)
	if err != nil {
		return err
	}
	mine := []playlist{}
	for _, p := range pls {
		if has(p.Name) {
			mine = append(mine, p)
		}
	}
	out["tracks"], out["albums"], out["artists"], out["playlists"] = ts, albums, artists, mine
	return httpx.WriteJSON(w, 200, out)
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
	_, err = l.DB.Exec(`INSERT INTO plays(user_id, path, at) VALUES(?, ?, ?)`, u.ID, rel, time.Now().UnixMilli())
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

// Playlists

func playlistName(w http.ResponseWriter, r *http.Request) (string, error) {
	var in struct{ Name string }
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return "", httpx.Error(400, "Escribe un nombre de hasta 100 caracteres.")
	}
	return name, nil
}

// ownPlaylist devuelve el id de la playlist de la ruta solo si es de este usuario; si no, 404.
func (l *Library) ownPlaylist(r *http.Request, u httpx.User) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, fs.ErrNotExist
	}
	return id, l.DB.QueryRow(`SELECT id FROM playlists WHERE id = ? AND user_id = ?`, id, u.ID).Scan(&id)
}

func (l *Library) touchPlaylist(id int64) {
	l.DB.Exec(`UPDATE playlists SET updated = ? WHERE id = ?`, time.Now().UnixMilli(), id)
}

func (l *Library) PlaylistCreate(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	name, err := playlistName(w, r)
	if err != nil {
		return err
	}
	res, err := l.DB.Exec(`INSERT INTO playlists(user_id, name, updated) VALUES(?, ?, ?)`, u.ID, name, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	return httpx.WriteJSON(w, 200, map[string]any{"id": id, "name": name})
}

func (l *Library) PlaylistGet(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, err := l.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	l.fresh()
	var name string
	if err := l.DB.QueryRow(`SELECT name FROM playlists WHERE id = ?`, id).Scan(&name); err != nil {
		return err
	}
	ts, err := l.tracks(`JOIN playlist_tracks pt ON pt.path = t.path WHERE pt.playlist_id = ? ORDER BY pt.pos, pt.rowid`, id)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, map[string]any{"id": id, "name": name, "tracks": ts})
}

func (l *Library) PlaylistRename(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, err := l.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	name, err := playlistName(w, r)
	if err != nil {
		return err
	}
	_, err = l.DB.Exec(`UPDATE playlists SET name = ?, updated = ? WHERE id = ?`, name, time.Now().UnixMilli(), id)
	return err
}

func (l *Library) PlaylistDelete(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, err := l.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	if _, err := l.DB.Exec(`DELETE FROM playlist_tracks WHERE playlist_id = ?`, id); err != nil {
		return err
	}
	_, err = l.DB.Exec(`DELETE FROM playlists WHERE id = ?`, id)
	return err
}

func (l *Library) PlaylistAdd(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, err := l.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	var in struct{ Path string }
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return err
	}
	rel, err := l.musicRel(in.Path)
	if err != nil {
		return err
	}
	res, err := l.DB.Exec(`INSERT OR IGNORE INTO playlist_tracks(playlist_id, path, pos)
		VALUES(?, ?, (SELECT COALESCE(MAX(pos), -1) + 1 FROM playlist_tracks WHERE playlist_id = ?))`, id, rel, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	l.touchPlaylist(id)
	return httpx.WriteJSON(w, 200, map[string]any{"added": n == 1}) // false: ya estaba en la playlist
}

func (l *Library) PlaylistRemove(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, err := l.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	_, err = l.DB.Exec(`DELETE FROM playlist_tracks WHERE playlist_id = ? AND path = ?`, id, r.URL.Query().Get("path"))
	l.touchPlaylist(id)
	return err
}

// playlistOrder deja las pistas en el orden recibido; todas deben estar ya en la playlist.
func (l *Library) PlaylistOrder(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, err := l.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	var in struct{ Paths []string }
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return err
	}
	tx, err := l.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, p := range in.Paths {
		// Posiciones negativas: las pistas que el cliente no ve (borradas del disco) quedan al final sin chocar.
		res, err := tx.Exec(`UPDATE playlist_tracks SET pos = ? WHERE playlist_id = ? AND path = ?`, i-len(in.Paths), id, p)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return httpx.Error(400, "El orden no coincide con la playlist.")
		}
	}
	// Dentro de la transacción: desde otra conexión esperaría el candado de escritura que esta tiene tomado.
	if _, err := tx.Exec(`UPDATE playlists SET updated = ? WHERE id = ?`, time.Now().UnixMilli(), id); err != nil {
		return err
	}
	return tx.Commit()
}
