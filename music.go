package main

// Música como app de streaming: índice de pistas con etiquetas, me gusta, playlists e historial.
// Álbum = carpeta que contiene las pistas; artista = carpeta de primer nivel. Las etiquetas solo
// mejoran los nombres: sin ellas todo sale del nombre de archivo y de carpeta.
// shortcut: un álbum repartido en subcarpetas (CD1, CD2) sale como álbumes separados; unirlos por etiqueta si molesta.

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
}

type artist struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Albums int    `json:"albums"`
	Tracks int    `json:"tracks"`
	Added  int64  `json:"added"`
	Played int64  `json:"played"`
	Plays  int    `json:"plays"`
}

type playlist struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Updated int64  `json:"updated"`
	Tracks  int    `json:"tracks"`
	Cover   string `json:"cover"` // carpeta del álbum de su primera pista
}

func (a *app) musicRoot() string { return filepath.Join(a.data, "media", "musica") }

// musicRel valida que rel sea una pista real de la biblioteca, escrita en su forma canónica.
func (a *app) musicRel(rel string) (string, error) {
	root := a.musicRoot()
	p := safe(root, rel)
	if st, err := os.Stat(p); err != nil || st.IsDir() || !mediaLibs["musica"](p) {
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
func (a *app) syncTracks() {
	if !a.idxMu.TryLock() { // ya hay un recorrido en curso: se responde con lo indexado hasta ahora
		return
	}
	defer a.idxMu.Unlock()
	if time.Since(a.idxAt) < idxEvery {
		return
	}
	root := a.musicRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		log.Print("índice de música: ", err)
		return
	}
	type stamp struct{ size, mtime int64 }
	old := map[string]stamp{}
	rows, err := a.db.Query(`SELECT path, size, mtime FROM tracks`)
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
		if d.IsDir() || !mediaLibs["musica"](d.Name()) {
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
		a.sem <- struct{}{}
		// Un archivo corrupto no puede detener el índice.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, _ := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "a",
			"-show_entries", "format=duration:format_tags:stream_tags", "-of", "json", p).Output()
		cancel()
		<-a.sem
		applyTags(&t, out)
		if _, err := a.db.Exec(`INSERT OR REPLACE INTO tracks(path, size, mtime, title, artist, album, album_dir, artist_dir, year, disc, num, dur, fold)
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
		a.db.Exec(`DELETE FROM tracks WHERE path = ?`, rel)
	}
	a.idxAt = time.Now()
}

// fresh actualiza el índice antes de responder, sin hacer esperar más de 2 s por una biblioteca recién copiada.
func (a *app) fresh() {
	done := make(chan struct{})
	go func() { a.syncTracks(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

const trackCols = `t.path, t.title, t.artist, t.album, t.album_dir, t.artist_dir, t.year, t.num, t.dur`
const albumOrder = ` ORDER BY t.album_dir, t.disc, t.num, t.path`

// tracks consulta pistas del índice; tail es el resto del SELECT (JOIN, WHERE, ORDER).
func (a *app) tracks(tail string, args ...any) ([]track, error) {
	rows, err := a.db.Query(`SELECT `+trackCols+` FROM tracks t `+tail, args...)
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
func (a *app) albums(u user) ([]album, error) {
	type stat struct {
		last  int64
		plays int
	}
	heard := map[string]stat{}
	rows, err := a.db.Query(`SELECT t.album_dir, MAX(p.at), COUNT(*) FROM plays p JOIN tracks t ON t.path = p.path WHERE p.user_id = ? GROUP BY t.album_dir`, u.id)
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
	rows, err = a.db.Query(`SELECT album_dir, MIN(album), artist_dir, MAX(year), COUNT(*), SUM(dur), MAX(mtime) FROM tracks GROUP BY album_dir`)
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
		al.Artist, al.Played, al.Plays = al.ArtistPath, heard[al.Path].last, heard[al.Path].plays
		out = append(out, al)
	}
	sort.Slice(out, func(i, j int) bool { return natLess(fold(out[i].Name), fold(out[j].Name)) })
	return out, rows.Err()
}

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
	sort.Slice(out, func(i, j int) bool { return natLess(fold(out[i].Name), fold(out[j].Name)) })
	return out
}

func (a *app) playlists(u user) ([]playlist, error) {
	rows, err := a.db.Query(`SELECT p.id, p.name, p.updated, COUNT(t.path),
		COALESCE((SELECT x.album_dir FROM playlist_tracks f JOIN tracks x ON x.path = f.path WHERE f.playlist_id = p.id ORDER BY f.pos LIMIT 1), '')
		FROM playlists p LEFT JOIN playlist_tracks pt ON pt.playlist_id = p.id LEFT JOIN tracks t ON t.path = pt.path
		WHERE p.user_id = ? GROUP BY p.id ORDER BY p.updated DESC, p.id DESC`, u.id)
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
func (a *app) musicLib(w http.ResponseWriter, r *http.Request, u user) error {
	a.fresh()
	albums, err := a.albums(u)
	if err != nil {
		return err
	}
	pls, err := a.playlists(u)
	if err != nil {
		return err
	}
	top, err := a.tracks(`JOIN (SELECT path, COUNT(*) c, MAX(at) m FROM plays WHERE user_id = ? GROUP BY path) p ON p.path = t.path
		ORDER BY p.c DESC, p.m DESC LIMIT 20`, u.id)
	if err != nil {
		return err
	}
	likes := []string{}
	rows, err := a.db.Query(`SELECT l.path FROM likes l JOIN tracks t ON t.path = l.path WHERE l.user_id = ?`, u.id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		rows.Scan(&p)
		likes = append(likes, p)
	}
	return writeJSON(w, 200, map[string]any{"albums": albums, "artists": artistsOf(albums), "playlists": pls, "likes": likes, "top": top})
}

func (a *app) musicAlbum(w http.ResponseWriter, r *http.Request, u user) error {
	a.fresh()
	dir := r.URL.Query().Get("path")
	ts, err := a.tracks(`WHERE t.album_dir = ?`+albumOrder, dir)
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
	return writeJSON(w, 200, map[string]any{"path": dir, "name": ts[0].Album, "artist": ts[0].ArtistPath, "artistPath": ts[0].ArtistPath, "year": year, "tracks": ts})
}

func (a *app) musicArtist(w http.ResponseWriter, r *http.Request, u user) error {
	a.fresh()
	dir := r.URL.Query().Get("path")
	ts, err := a.tracks(`WHERE t.artist_dir = ? ORDER BY t.year DESC, t.album_dir, t.disc, t.num, t.path`, dir)
	if err != nil {
		return err
	}
	if len(ts) == 0 || dir == "" {
		return fs.ErrNotExist
	}
	// Sin historial todas empatan en cero y quedan las primeras pistas.
	popular, err := a.tracks(`WHERE t.artist_dir = ? ORDER BY (SELECT COUNT(*) FROM plays p WHERE p.user_id = ? AND p.path = t.path) DESC,
		t.year DESC, t.album_dir, t.disc, t.num, t.path LIMIT 5`, dir, u.id)
	if err != nil {
		return err
	}
	all, err := a.albums(u)
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
	return writeJSON(w, 200, map[string]any{"path": dir, "name": dir, "albums": mine, "tracks": ts, "popular": popular})
}

var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// musicSearch busca en el índice: cada palabra debe aparecer, sin importar tildes ni mayúsculas.
func (a *app) musicSearch(w http.ResponseWriter, r *http.Request, u user) error {
	a.fresh()
	out := map[string]any{"tracks": []track{}, "albums": []album{}, "artists": []artist{}, "playlists": []playlist{}}
	words := strings.Fields(fold(r.URL.Query().Get("q")))
	if len(strings.Join(words, "")) < 2 {
		return writeJSON(w, 200, out)
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
	ts, err := a.tracks(`WHERE 1`+where+` ORDER BY instr(t.fold, ?), t.title LIMIT 30`, append(args, words[0])...)
	if err != nil {
		return err
	}
	all, err := a.albums(u)
	if err != nil {
		return err
	}
	albums, artists := []album{}, []artist{}
	for _, al := range all {
		if len(albums) < 12 && has(al.Name+" "+al.Artist) {
			albums = append(albums, al)
		}
	}
	for _, ar := range artistsOf(all) {
		if len(artists) < 8 && has(ar.Name) {
			artists = append(artists, ar)
		}
	}
	pls, err := a.playlists(u)
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
	return writeJSON(w, 200, out)
}

// Me gusta e historial

func (a *app) likes(w http.ResponseWriter, r *http.Request, u user) error {
	a.fresh()
	ts, err := a.tracks(`JOIN likes l ON l.path = t.path WHERE l.user_id = ? ORDER BY l.at DESC, l.rowid DESC`, u.id)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, ts)
}

func (a *app) likePut(w http.ResponseWriter, r *http.Request, u user) error {
	rel, err := a.musicRel(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT OR IGNORE INTO likes(user_id, path, at) VALUES(?, ?, ?)`, u.id, rel, time.Now().UnixMilli())
	return err
}

// likeDelete no valida contra el disco: debe poder quitarse el me gusta de una pista que ya no existe.
func (a *app) likeDelete(w http.ResponseWriter, r *http.Request, u user) error {
	_, err := a.db.Exec(`DELETE FROM likes WHERE user_id = ? AND path = ?`, u.id, r.URL.Query().Get("path"))
	return err
}

// played registra una escucha; la interfaz la envía a los 30 s o al terminar la pista.
func (a *app) played(w http.ResponseWriter, r *http.Request, u user) error {
	var in struct{ Path string }
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	rel, err := a.musicRel(in.Path)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT INTO plays(user_id, path, at) VALUES(?, ?, ?)`, u.id, rel, time.Now().UnixMilli())
	return err
}

// Playlists

func playlistName(w http.ResponseWriter, r *http.Request) (string, error) {
	var in struct{ Name string }
	if err := readJSON(w, r, &in); err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return "", httpErr{400, "Escribe un nombre de hasta 100 caracteres."}
	}
	return name, nil
}

// ownPlaylist devuelve el id de la playlist de la ruta solo si es de este usuario; si no, 404.
func (a *app) ownPlaylist(r *http.Request, u user) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, fs.ErrNotExist
	}
	return id, a.db.QueryRow(`SELECT id FROM playlists WHERE id = ? AND user_id = ?`, id, u.id).Scan(&id)
}

func (a *app) touchPlaylist(id int64) {
	a.db.Exec(`UPDATE playlists SET updated = ? WHERE id = ?`, time.Now().UnixMilli(), id)
}

func (a *app) playlistCreate(w http.ResponseWriter, r *http.Request, u user) error {
	name, err := playlistName(w, r)
	if err != nil {
		return err
	}
	res, err := a.db.Exec(`INSERT INTO playlists(user_id, name, updated) VALUES(?, ?, ?)`, u.id, name, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	return writeJSON(w, 200, map[string]any{"id": id, "name": name})
}

func (a *app) playlistGet(w http.ResponseWriter, r *http.Request, u user) error {
	id, err := a.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	a.fresh()
	var name string
	if err := a.db.QueryRow(`SELECT name FROM playlists WHERE id = ?`, id).Scan(&name); err != nil {
		return err
	}
	ts, err := a.tracks(`JOIN playlist_tracks pt ON pt.path = t.path WHERE pt.playlist_id = ? ORDER BY pt.pos, pt.rowid`, id)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"id": id, "name": name, "tracks": ts})
}

func (a *app) playlistRename(w http.ResponseWriter, r *http.Request, u user) error {
	id, err := a.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	name, err := playlistName(w, r)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`UPDATE playlists SET name = ?, updated = ? WHERE id = ?`, name, time.Now().UnixMilli(), id)
	return err
}

func (a *app) playlistDelete(w http.ResponseWriter, r *http.Request, u user) error {
	id, err := a.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(`DELETE FROM playlist_tracks WHERE playlist_id = ?`, id); err != nil {
		return err
	}
	_, err = a.db.Exec(`DELETE FROM playlists WHERE id = ?`, id)
	return err
}

func (a *app) playlistAdd(w http.ResponseWriter, r *http.Request, u user) error {
	id, err := a.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	var in struct{ Path string }
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	rel, err := a.musicRel(in.Path)
	if err != nil {
		return err
	}
	res, err := a.db.Exec(`INSERT OR IGNORE INTO playlist_tracks(playlist_id, path, pos)
		VALUES(?, ?, (SELECT COALESCE(MAX(pos), -1) + 1 FROM playlist_tracks WHERE playlist_id = ?))`, id, rel, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	a.touchPlaylist(id)
	return writeJSON(w, 200, map[string]any{"added": n == 1}) // false: ya estaba en la playlist
}

func (a *app) playlistRemove(w http.ResponseWriter, r *http.Request, u user) error {
	id, err := a.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`DELETE FROM playlist_tracks WHERE playlist_id = ? AND path = ?`, id, r.URL.Query().Get("path"))
	a.touchPlaylist(id)
	return err
}

// playlistOrder deja las pistas en el orden recibido; todas deben estar ya en la playlist.
func (a *app) playlistOrder(w http.ResponseWriter, r *http.Request, u user) error {
	id, err := a.ownPlaylist(r, u)
	if err != nil {
		return err
	}
	var in struct{ Paths []string }
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	tx, err := a.db.Begin()
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
			return httpErr{400, "El orden no coincide con la playlist."}
		}
	}
	// Dentro de la transacción: desde otra conexión esperaría el candado de escritura que esta tiene tomado.
	if _, err := tx.Exec(`UPDATE playlists SET updated = ? WHERE id = ?`, time.Now().UnixMilli(), id); err != nil {
		return err
	}
	return tx.Commit()
}
