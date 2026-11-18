// Package store abre la base SQLite de la app. Los originales se guardan tal cual en
// carpetas normales bajo DATA_DIR; SQLite solo lleva el índice.
package store

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL, pass BLOB NOT NULL, seen INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions(token TEXT PRIMARY KEY, user_id INTEGER NOT NULL, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS media(
	id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, sha TEXT NOT NULL, name TEXT NOT NULL,
	size INTEGER NOT NULL, kind TEXT NOT NULL, taken INTEGER NOT NULL, path TEXT NOT NULL,
	UNIQUE(user_id, sha));
CREATE INDEX IF NOT EXISTS media_taken ON media(user_id, taken DESC);
CREATE TABLE IF NOT EXISTS tracks(
	path TEXT PRIMARY KEY, size INTEGER NOT NULL, mtime INTEGER NOT NULL,
	title TEXT NOT NULL, artist TEXT NOT NULL, album TEXT NOT NULL, album_dir TEXT NOT NULL, artist_dir TEXT NOT NULL,
	year INTEGER NOT NULL, disc INTEGER NOT NULL, num INTEGER NOT NULL, dur REAL NOT NULL, fold TEXT NOT NULL, genre TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS tracks_album ON tracks(album_dir);
CREATE INDEX IF NOT EXISTS tracks_artist ON tracks(artist_dir);
CREATE TABLE IF NOT EXISTS likes(user_id INTEGER NOT NULL, path TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY(user_id, path));
CREATE TABLE IF NOT EXISTS playlists(id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, name TEXT NOT NULL, updated INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS playlist_tracks(playlist_id INTEGER NOT NULL, path TEXT NOT NULL, pos INTEGER NOT NULL, PRIMARY KEY(playlist_id, path));
CREATE TABLE IF NOT EXISTS plays(user_id INTEGER NOT NULL, path TEXT NOT NULL, at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS plays_user ON plays(user_id, path);
CREATE TABLE IF NOT EXISTS saved(user_id INTEGER NOT NULL, kind TEXT NOT NULL, path TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY(user_id, kind, path));
CREATE TABLE IF NOT EXISTS logins(at INTEGER NOT NULL, name TEXT NOT NULL, ok INTEGER NOT NULL, ip TEXT NOT NULL, agent TEXT NOT NULL);`

// Open crea las carpetas internas de data/.palobox y abre la base con el esquema al día.
func Open(data string) (*sql.DB, error) {
	for _, d := range []string{"tmp", "thumbs"} {
		if err := os.MkdirAll(filepath.Join(data, ".palobox", d), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(data, ".palobox", "db.sqlite")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	// Bases creadas antes del panel de administrador; falla sin daño si la columna ya existe.
	db.Exec(`ALTER TABLE users ADD COLUMN seen INTEGER NOT NULL DEFAULT 0`)
	// Columnas del control de usuarios y sesiones; mismas reglas que la anterior.
	for _, col := range []string{"users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0", "users ADD COLUMN music_only INTEGER NOT NULL DEFAULT 0",
		"sessions ADD COLUMN created INTEGER NOT NULL DEFAULT 0", "sessions ADD COLUMN seen INTEGER NOT NULL DEFAULT 0",
		"sessions ADD COLUMN ip TEXT NOT NULL DEFAULT ''", "sessions ADD COLUMN agent TEXT NOT NULL DEFAULT ''"} {
		db.Exec(`ALTER TABLE ` + col)
	}
	// Índices anteriores al género: la columna nace vacía y mtime = -1 hace que Sync vuelva a leer las etiquetas.
	if _, err := db.Exec(`ALTER TABLE tracks ADD COLUMN genre TEXT NOT NULL DEFAULT ''`); err == nil {
		db.Exec(`UPDATE tracks SET mtime = -1`)
	}
	return db, nil
}
