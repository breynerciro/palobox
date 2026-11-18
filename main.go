// PaloBox: fotos, videos y archivos personales en un solo binario.
// Los originales se guardan tal cual en carpetas normales bajo DATA_DIR;
// SQLite solo lleva el índice, así que los archivos siguen accesibles sin la app.
package main

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"palobox/internal/media"
)

//go:embed web
var webFS embed.FS

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
	year INTEGER NOT NULL, disc INTEGER NOT NULL, num INTEGER NOT NULL, dur REAL NOT NULL, fold TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS tracks_album ON tracks(album_dir);
CREATE INDEX IF NOT EXISTS tracks_artist ON tracks(artist_dir);
CREATE TABLE IF NOT EXISTS likes(user_id INTEGER NOT NULL, path TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY(user_id, path));
CREATE TABLE IF NOT EXISTS playlists(id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, name TEXT NOT NULL, updated INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS playlist_tracks(playlist_id INTEGER NOT NULL, path TEXT NOT NULL, pos INTEGER NOT NULL, PRIMARY KEY(playlist_id, path));
CREATE TABLE IF NOT EXISTS plays(user_id INTEGER NOT NULL, path TEXT NOT NULL, at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS plays_user ON plays(user_id, path);`

type app struct {
	db      *sql.DB
	data    string
	sem     chan struct{} // procesos pesados simultáneos (ffmpeg, vips): el servidor tiene 2 núcleos
	thumbs  media.Thumbs
	loginMu sync.Mutex
	upMu    sync.Map   // id de subida -> *sync.Mutex: un POST o PATCH a la vez por subida
	finMu   sync.Mutex // una foto a la vez pasa de "¿duplicado?" a quedar en el índice
	spotdl  module     // descargas de Spotify (servicio spotDL); sin url = sección desactivada
	admin   string     // usuario que ve el panel de administrador
	idxMu   sync.Mutex // un recorrido del índice de música a la vez
	idxAt   time.Time  // último recorrido completo; lo protege idxMu
}

type user struct {
	id   int64
	name string
}

func open(data string) (*app, error) {
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
	sem := make(chan struct{}, 2)
	a := &app{db: db, data: data, sem: sem, thumbs: media.Thumbs{Dir: filepath.Join(data, ".palobox", "thumbs"), Sem: sem}}
	// Subidas abandonadas hace más de una semana.
	if es, err := os.ReadDir(a.tmp("")); err == nil {
		for _, e := range es {
			if i, err := e.Info(); err == nil && time.Since(i.ModTime()) > 7*24*time.Hour {
				os.Remove(a.tmp(e.Name()))
			}
		}
	}
	return a, nil
}

func (a *app) isAdmin(u user) bool           { return u.name == a.admin }
func (a *app) tmp(name string) string        { return filepath.Join(a.data, ".palobox", "tmp", name) }
func (a *app) dir(u user, sub string) string { return filepath.Join(a.data, u.name, sub) }

// safe une rel bajo base sin dejar salir de base: limpiar "/"+rel elimina todos los "..".
func safe(base, rel string) string { return filepath.Join(base, filepath.Clean("/"+rel)) }

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/me", a.auth(func(w http.ResponseWriter, r *http.Request, u user) error {
		return writeJSON(w, 200, map[string]any{"name": u.name, "admin": a.isAdmin(u)})
	}))
	mux.HandleFunc("GET /api/admin", a.auth(a.adminStats))
	mux.HandleFunc("POST /api/uploads", a.auth(a.upStart))
	mux.HandleFunc("PATCH /api/uploads/{id}", a.auth(a.upPart))
	mux.HandleFunc("GET /api/photos", a.auth(a.photos))
	mux.HandleFunc("GET /api/photos/{id}/{v}", a.auth(a.photo))
	mux.HandleFunc("DELETE /api/photos/{id}", a.auth(a.photoDelete))
	mux.HandleFunc("GET /api/files", a.auth(a.filesList))
	mux.HandleFunc("GET /api/files/raw", a.auth(a.filesRaw))
	mux.HandleFunc("POST /api/files/mkdir", a.auth(a.mkdir))
	mux.HandleFunc("DELETE /api/files", a.auth(a.filesDelete))
	mux.HandleFunc("GET /api/media/raw", a.auth(a.mediaRaw))
	mux.HandleFunc("GET /api/media/thumb", a.auth(a.mediaThumb))
	mux.HandleFunc("GET /api/music", a.auth(a.musicLib))
	mux.HandleFunc("GET /api/music/album", a.auth(a.musicAlbum))
	mux.HandleFunc("GET /api/music/artist", a.auth(a.musicArtist))
	mux.HandleFunc("GET /api/music/search", a.auth(a.musicSearch))
	mux.HandleFunc("GET /api/music/likes", a.auth(a.likes))
	mux.HandleFunc("PUT /api/music/likes", a.auth(a.likePut))
	mux.HandleFunc("DELETE /api/music/likes", a.auth(a.likeDelete))
	mux.HandleFunc("POST /api/music/plays", a.auth(a.played))
	mux.HandleFunc("POST /api/music/playlists", a.auth(a.playlistCreate))
	mux.HandleFunc("GET /api/music/playlists/{id}", a.auth(a.playlistGet))
	mux.HandleFunc("PATCH /api/music/playlists/{id}", a.auth(a.playlistRename))
	mux.HandleFunc("DELETE /api/music/playlists/{id}", a.auth(a.playlistDelete))
	mux.HandleFunc("POST /api/music/playlists/{id}/tracks", a.auth(a.playlistAdd))
	mux.HandleFunc("DELETE /api/music/playlists/{id}/tracks", a.auth(a.playlistRemove))
	mux.HandleFunc("PUT /api/music/playlists/{id}/order", a.auth(a.playlistOrder))
	mux.HandleFunc("GET /api/spotdl", a.auth(a.spotdlList))
	mux.HandleFunc("POST /api/spotdl", a.auth(a.spotdlAdd))
	mux.HandleFunc("DELETE /api/spotdl/{id}", a.auth(a.spotdlDelete))
	mux.HandleFunc("POST /api/spotdl/{id}/retry", a.auth(a.spotdlRetry))
	web, _ := fs.Sub(webFS, "web")
	// Huella de la interfaz: la app instalada la compara para saber si debe recargarse.
	h := sha256.New()
	fs.WalkDir(web, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(web, p)
			h.Write(b)
		}
		return nil
	})
	version := hex.EncodeToString(h.Sum(nil))[:12]
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, version) })
	static := http.FileServerFS(web)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache") // sin esto el celular puede quedarse con la interfaz vieja
		// Los archivos incrustados no tienen fecha: sin ETag el navegador no puede revalidar y baja todo en cada apertura.
		w.Header().Set("ETag", `"`+version+`"`)
		static.ServeHTTP(w, r)
	})
	// CSRF: rechaza con 403 cualquier POST/PUT/PATCH/DELETE que el navegador marque como
	// de otro sitio (Sec-Fetch-Site, o Origin distinto del Host en navegadores viejos).
	guarded := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		guarded.ServeHTTP(w, r)
	})
}

func main() {
	// Tipos que Go no trae y que el navegador necesita para mostrar los originales.
	for ext, t := range map[string]string{".heic": "image/heic", ".heif": "image/heif", ".avif": "image/avif", ".dng": "image/x-adobe-dng",
		".mov": "video/quicktime", ".m4v": "video/mp4", ".mkv": "video/x-matroska", ".webmanifest": "application/manifest+json",
		".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac",
		".flac": "audio/flac", ".wav": "audio/wav", ".ogg": "audio/ogg", ".opus": "audio/ogg"} {
		mime.AddExtensionType(ext, t)
	}
	data := os.Getenv("DATA_DIR")
	if data == "" {
		data = "/data"
	}
	a, err := open(data)
	if err != nil {
		log.Fatal(err)
	}
	a.spotdl = loadModule("SPOTDL", "El servicio de descargas de Spotify")
	if a.admin = os.Getenv("ADMIN"); a.admin == "" {
		a.admin = "breyner"
	}
	if len(os.Args) == 4 && os.Args[1] == "adduser" {
		if err := a.addUser(os.Args[2], os.Args[3]); err != nil {
			log.Fatal(err)
		}
		fmt.Println("usuario listo:", os.Args[2])
		return
	}
	go a.syncTracks() // el primer recorrido de una biblioteca grande tarda: que empiece antes de la primera visita
	log.Println("escuchando en :8080, datos en", data)
	// Sin ReadTimeout ni WriteTimeout: hay subidas grandes y reproducción de audio y video.
	srv := &http.Server{Addr: ":8080", Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Fatal(srv.ListenAndServe())
}
