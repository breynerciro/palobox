package music

// Playlists de cada usuario: las que arma en la app y las de Spotify que descargó spotDL.

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"palobox/internal/httpx"
)

type playlist struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Updated int64  `json:"updated"`
	Tracks  int    `json:"tracks"`
	Cover   string `json:"cover"` // carpeta del álbum de su primera pista
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

func playlistName(w http.ResponseWriter, r *http.Request) (string, error) {
	name, _, err := playlistInput(w, r)
	return name, err
}

// playlistInput lee el nombre y, al crear, las canciones con las que nace (las de una playlist por descripción).
func playlistInput(w http.ResponseWriter, r *http.Request) (string, []string, error) {
	var in struct {
		Name  string
		Paths []string
	}
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return "", nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 || len(in.Paths) > 500 {
		return "", nil, httpx.Error(400, "Escribe un nombre de hasta 100 caracteres.")
	}
	return name, in.Paths, nil
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

// ImportPlaylist deja a owner la playlist de Spotify que acaba de descargar (trabajo job), con sus canciones en
// orden. Una sola vez por trabajo: si después la borra o la cambia, no vuelve. Si ya tiene una con ese nombre
// (la pidió antes), le agrega al final las que falten en vez de duplicarla.
func (l *Library) ImportPlaylist(owner, job, name string, paths []string) {
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	if name == "" || len(paths) == 0 {
		return
	}
	// Se consulta antes: el mismo trabajo se vuelve a anunciar en cada consulta y no vale una escritura cada vez.
	var one int
	if l.DB.QueryRow(`SELECT 1 FROM spotdl_done WHERE job = ?`, job).Scan(&one) == nil {
		return
	}
	if err := l.importPlaylist(owner, job, name, paths); err != nil {
		log.Print("playlist de Spotify: ", err)
		return
	}
	l.inherit(paths)
}

// importPlaylist hace la marca del trabajo y la playlist en una sola transacción: si algo falla a la mitad no
// queda nada, y el trabajo se vuelve a intentar.
func (l *Library) importPlaylist(owner, job, name string, paths []string) error {
	tx, err := l.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT OR IGNORE INTO spotdl_done(job) VALUES(?)`, job)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	var uid, id int64
	if err := tx.QueryRow(`SELECT id FROM users WHERE name = ?`, owner).Scan(&uid); err == sql.ErrNoRows {
		return tx.Commit() // la cuenta ya no existe: queda solo la marca
	} else if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if err := tx.QueryRow(`SELECT id FROM playlists WHERE user_id = ? AND name = ?`, uid, name).Scan(&id); err == sql.ErrNoRows {
		res, err := tx.Exec(`INSERT INTO playlists(user_id, name, updated) VALUES(?, ?, ?)`, uid, name, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
	} else if err != nil {
		return err
	}
	for _, p := range paths {
		if t := l.twin(tx, p); t != "" {
			p = t
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO playlist_tracks(playlist_id, path, pos)
			VALUES(?, ?, (SELECT COALESCE(MAX(pos), -1) + 1 FROM playlist_tracks WHERE playlist_id = ?))`, id, p, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE playlists SET updated = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

// twin devuelve, para una ruta que no está en el índice, la pista del mismo artista con el mismo nombre de archivo
// ("" si no hay): la limpieza de duplicados pudo borrar justo la copia que spotDL anunció. Si el archivo sigue en
// disco es que aún no se indexó, y la del mismo nombre puede ser otra canción (otro álbum): se deja la anunciada.
func (l *Library) twin(q interface {
	QueryRow(string, ...any) *sql.Row
}, p string) string {
	if _, err := os.Stat(filepath.Join(l.musicRoot(), p)); err == nil {
		return ""
	}
	artist, _, ok := strings.Cut(p, "/")
	if !ok {
		return ""
	}
	var t string
	// Igualdad sobre el final de la ruta y no LIKE: los nombres traen % y _.
	q.QueryRow(`SELECT path FROM tracks WHERE artist_dir = ?1 AND substr(path, -length(?2)) = ?2
		AND NOT EXISTS(SELECT 1 FROM tracks WHERE path = ?3) ORDER BY path LIMIT 1`, artist, "/"+path.Base(p), p).Scan(&t)
	return t
}

// repairPlaylists pasa a su gemela las canciones de playlist que apuntan a una copia ya borrada; si la gemela ya
// está en la playlist, la fila queda quieta (no se ve).
func (l *Library) repairPlaylists() {
	rows, err := l.DB.Query(`SELECT DISTINCT path FROM playlist_tracks WHERE path NOT IN (SELECT path FROM tracks)`)
	if err != nil {
		log.Print("limpieza de música: ", err)
		return
	}
	var lost []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		lost = append(lost, p)
	}
	rows.Close()
	for _, p := range lost {
		if t := l.twin(l.DB, p); t != "" {
			l.DB.Exec(`UPDATE OR IGNORE playlist_tracks SET path = ? WHERE path = ?`, t, p)
		}
	}
}

func (l *Library) PlaylistCreate(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	name, paths, err := playlistInput(w, r)
	if err != nil {
		return err
	}
	tx, err := l.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO playlists(user_id, name, updated) VALUES(?, ?, ?)`, u.ID, name, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	for i, p := range paths { // solo entran pistas del índice: lo demás se ignora
		if _, err := tx.Exec(`INSERT OR IGNORE INTO playlist_tracks(playlist_id, path, pos) SELECT ?, path, ? FROM tracks WHERE path = ?`, id, i, p); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
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
	// 1 MiB y no los 64 KB de ReadJSON: el orden de una playlist de más de mil canciones no cabe.
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
		return httpx.Error(400, "petición inválida")
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
