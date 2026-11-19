// PaloBox: fotos, videos, archivos personales y música en un solo binario.
// Este archivo solo arma las piezas de internal/ y define las rutas.
package main

import (
	"crypto/sha256"
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
	"time"

	"palobox/internal/admin"
	"palobox/internal/auth"
	"palobox/internal/files"
	"palobox/internal/httpx"
	"palobox/internal/jam"
	"palobox/internal/media"
	"palobox/internal/module"
	"palobox/internal/music"
	"palobox/internal/photos"
	"palobox/internal/spotdl"
	"palobox/internal/store"
	"palobox/internal/uploads"
)

//go:embed web
var webFS embed.FS

type app struct {
	auth    *auth.Service
	admin   *admin.Handler
	files   *files.Handler
	photos  *photos.Handler
	uploads *uploads.Handler
	music   *music.Library
	spotdl  *spotdl.Handler // sin Module.URL = sección desactivada
	jam     *jam.Hub
}

func open(data string) (*app, error) {
	db, err := store.Open(data)
	if err != nil {
		return nil, err
	}
	// Procesos pesados simultáneos (ffmpeg, vips), con cupos separados: carátulas e índice de la música (55
	// cuentas) no pueden hacer esperar las fotos de la familia. Medido: con un cupo compartido, 30 personas
	// abriendo álbumes llevaron la vista grande de una foto de 0,5 a 1,3 s.
	sem, cloudSem := make(chan struct{}, 2), make(chan struct{}, 2)
	dir := filepath.Join(data, ".palobox", "thumbs")
	thumbs, cloudThumbs := media.Thumbs{Dir: dir, Sem: sem}, media.Thumbs{Dir: dir, Sem: cloudSem}
	au := &auth.Service{DB: db}
	lib := &music.Library{DB: db, Data: data, Sem: sem, Thumbs: thumbs}
	return &app{
		auth:    au,
		admin:   &admin.Handler{DB: db, Data: data, Auth: au, Music: lib},
		files:   &files.Handler{Data: data},
		photos:  &photos.Handler{DB: db, Data: data, Thumbs: cloudThumbs},
		uploads: uploads.New(db, data, cloudThumbs),
		music:   lib,
		spotdl:  &spotdl.Handler{Full: lib.Full, Playlist: lib.ImportPlaylist},
		jam:     &jam.Hub{Lookup: lib.ByPaths, More: lib.Random, Audio: lib.OpenStream},
	}, nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	auth := a.auth.Wrap
	// cloud protege Fotos, Archivos y sus subidas: las cuentas de solo Música no entran, aunque pidan la ruta a mano.
	cloud := func(h httpx.Handler) http.HandlerFunc {
		return auth(func(w http.ResponseWriter, r *http.Request, u httpx.User) error {
			if u.MusicOnly {
				return httpx.Error(403, "Tu cuenta solo tiene acceso a Música.")
			}
			return h(w, r, u)
		})
	}
	// Solo las listas JSON van comprimidas: el audio, los eventos y los archivos no se envuelven (ver httpx.Gzip).
	zip := httpx.Gzip
	mux.HandleFunc("POST /api/login", a.auth.Login)
	mux.HandleFunc("POST /api/logout", a.auth.Logout)
	mux.HandleFunc("GET /api/invite", a.auth.InviteInfo)
	mux.HandleFunc("POST /api/invite", a.auth.Accept)
	mux.HandleFunc("GET /api/me", auth(func(w http.ResponseWriter, r *http.Request, u httpx.User) error {
		return httpx.WriteJSON(w, 200, map[string]any{"name": u.Name, "admin": u.Admin, "musicOnly": u.MusicOnly})
	}))
	mux.HandleFunc("GET /api/admin", zip(auth(a.admin.Stats)))
	mux.HandleFunc("POST /api/admin/users", auth(a.admin.CreateUser))
	mux.HandleFunc("POST /api/admin/users/{name}/invite", auth(a.admin.Invite))
	mux.HandleFunc("PUT /api/admin/users/{name}/access", auth(a.admin.SetAccess))
	mux.HandleFunc("PUT /api/admin/users/{name}/disabled", auth(a.admin.SetDisabled))
	mux.HandleFunc("DELETE /api/admin/users/{name}", auth(a.admin.DeleteUser))
	mux.HandleFunc("DELETE /api/admin/users/{name}/sessions", auth(a.admin.CloseUserSessions))
	mux.HandleFunc("DELETE /api/admin/sessions/{id}", auth(a.admin.CloseSession))
	mux.HandleFunc("POST /api/admin/music/reindex", auth(a.admin.Reindex))
	mux.HandleFunc("GET /api/admin/spotdl", auth(a.spotdl.Health))
	mux.HandleFunc("POST /api/uploads", cloud(a.uploads.Start))
	mux.HandleFunc("PATCH /api/uploads/{id}", cloud(a.uploads.Part))
	mux.HandleFunc("GET /api/photos", zip(cloud(a.photos.List)))
	mux.HandleFunc("GET /api/photos/{id}/{v}", cloud(a.photos.Get))
	mux.HandleFunc("DELETE /api/photos/{id}", cloud(a.photos.Delete))
	mux.HandleFunc("GET /api/files", zip(cloud(a.files.List)))
	mux.HandleFunc("GET /api/files/raw", cloud(a.files.Raw))
	mux.HandleFunc("POST /api/files/mkdir", cloud(a.files.Mkdir))
	mux.HandleFunc("DELETE /api/files", cloud(a.files.Delete))
	mux.HandleFunc("GET /api/media/raw", auth(a.music.Raw))
	mux.HandleFunc("GET /api/media/thumb", auth(a.music.Thumb))
	mux.HandleFunc("GET /api/music", zip(auth(a.music.Lib)))
	mux.HandleFunc("GET /api/music/album", zip(auth(a.music.Album)))
	mux.HandleFunc("GET /api/music/artist", zip(auth(a.music.Artist)))
	mux.HandleFunc("GET /api/music/search", zip(auth(a.music.Search)))
	mux.HandleFunc("GET /api/music/radio", zip(auth(a.music.Radio)))
	mux.HandleFunc("GET /api/music/offline", auth(a.music.Offline)) // audio listo para guardar en el celular (web/offline.js)
	mux.HandleFunc("GET /api/music/likes", zip(auth(a.music.Likes)))
	mux.HandleFunc("PUT /api/music/likes", auth(a.music.LikePut))
	mux.HandleFunc("DELETE /api/music/likes", auth(a.music.LikeDelete))
	mux.HandleFunc("POST /api/music/plays", auth(a.music.Played))
	mux.HandleFunc("PUT /api/music/saved", auth(a.music.SavePut))
	mux.HandleFunc("DELETE /api/music/saved", auth(a.music.SaveDelete))
	mux.HandleFunc("POST /api/music/playlists", auth(a.music.PlaylistCreate))
	mux.HandleFunc("GET /api/music/playlists/{id}", zip(auth(a.music.PlaylistGet)))
	mux.HandleFunc("PATCH /api/music/playlists/{id}", auth(a.music.PlaylistRename))
	mux.HandleFunc("DELETE /api/music/playlists/{id}", auth(a.music.PlaylistDelete))
	mux.HandleFunc("POST /api/music/playlists/{id}/tracks", auth(a.music.PlaylistAdd))
	mux.HandleFunc("DELETE /api/music/playlists/{id}/tracks", auth(a.music.PlaylistRemove))
	mux.HandleFunc("PUT /api/music/playlists/{id}/order", auth(a.music.PlaylistOrder))
	mux.HandleFunc("GET /api/jam/events", auth(a.jam.Events))
	mux.HandleFunc("POST /api/jam", auth(a.jam.Cmd))
	mux.HandleFunc("GET /api/jam/stream", auth(a.jam.Stream))
	mux.HandleFunc("GET /api/spotdl", zip(auth(a.spotdl.List)))
	mux.HandleFunc("POST /api/spotdl", auth(a.spotdl.Add))
	mux.HandleFunc("DELETE /api/spotdl/{id}", auth(a.spotdl.Delete))
	mux.HandleFunc("POST /api/spotdl/{id}/retry", auth(a.spotdl.Retry))
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
	a.spotdl.Module = module.Load("SPOTDL", "El servicio de descargas de Spotify")
	a.music.Max = 8000 // tope de canciones para las descargas de quienes no son el administrador
	if v := os.Getenv("MUSIC_MAX"); v != "" {
		if _, err := fmt.Sscan(v, &a.music.Max); err != nil {
			log.Fatal("MUSIC_MAX no es un número: ", v)
		}
	}
	if a.auth.Admin = os.Getenv("ADMIN"); a.auth.Admin == "" {
		a.auth.Admin = "admin"
	}
	if len(os.Args) == 4 && os.Args[1] == "adduser" {
		if err := a.auth.AddUser(os.Args[2], os.Args[3]); err != nil {
			log.Fatal(err)
		}
		fmt.Println("usuario listo:", os.Args[2])
		return
	}
	go a.music.Sync() // el primer recorrido de una biblioteca grande tarda: que empiece antes de la primera visita
	log.Println("escuchando en :8080, datos en", data)
	// Sin ReadTimeout ni WriteTimeout: hay subidas grandes y reproducción de audio y video.
	srv := &http.Server{Addr: ":8080", Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Fatal(srv.ListenAndServe())
}
