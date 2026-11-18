package main

// Descargas de Spotify (spotDL) para la biblioteca de música.
// La app solo autentica y reenvía: el servicio spotDL es el dueño de la cola, de su
// SQLite y del proceso, y escribe los MP3 directamente en media/musica.
// El servicio no se publica y exige el token del módulo (ver modules.go).

import (
	"net/http"
	"regexp"
	"strings"
)

// spotifyRe acepta los cuatro tipos de Spotify, con o sin prefijo de idioma, y descarta
// cualquier parámetro de seguimiento (?si=...).
var spotifyRe = regexp.MustCompile(`^https://open\.spotify\.com/(?:intl-[A-Za-z-]{2,10}/)?(track|album|playlist|artist)/([A-Za-z0-9]{16,40})(?:[/?#].*)?$`)
var spotdlIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// spotdlJob es un trabajo tal como lo devuelve el servicio.
type spotdlJob struct {
	ID       string   `json:"id"`
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Status   string   `json:"status"` // queued | downloading | error (reintentable) | finished (solo aviso)
	Current  string   `json:"current"`
	Total    int      `json:"total"`
	Done     int      `json:"done"`
	Progress *float64 `json:"progress"` // nil mientras no se conoce el total
	Error    string   `json:"error"`    // mensaje para el usuario; el detalle técnico queda en el log del servicio
	Created  int64    `json:"created"`
	Owner    string   `json:"owner"` // quién la pidió
	Attempts int      `json:"attempts"`
	Notice   string   `json:"notice"` // con status finished: el aviso que la interfaz muestra una vez
	Mine     bool     `json:"mine"`   // lo calcula la app: puede cancelarla, quitarla o reintentarla
}

// spotifyURL valida el enlace en la app y lo devuelve canónico (sin idioma ni parámetros).
func spotifyURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 {
		return "", httpErr{400, "El enlace no es de Spotify."}
	}
	m := spotifyRe.FindStringSubmatch(raw)
	if m == nil {
		return "", httpErr{400, "El enlace no es de Spotify."}
	}
	return "https://open.spotify.com/" + m[1] + "/" + m[2], nil
}

// spotdlList devuelve la cola. Sin servicio configurado responde deshabilitado para que
// la interfaz oculte la sección sin mostrar un error.
func (a *app) spotdlList(w http.ResponseWriter, r *http.Request, u user) error {
	if a.spotdl.url == "" {
		return writeJSON(w, 200, map[string]any{"enabled": false, "items": []spotdlJob{}})
	}
	items := []spotdlJob{}
	if err := a.spotdl.do(u, a.isAdmin(u), "GET", "/jobs", nil, &items); err != nil {
		return err
	}
	if items == nil {
		items = []spotdlJob{}
	}
	for i := range items {
		items[i].Mine = items[i].Owner == u.name || a.isAdmin(u)
	}
	return writeJSON(w, 200, map[string]any{"enabled": true, "items": items})
}

func (a *app) spotdlAdd(w http.ResponseWriter, r *http.Request, u user) error {
	var in struct {
		URL string `json:"url"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	url, err := spotifyURL(in.URL)
	if err != nil {
		return err
	}
	var job spotdlJob
	if err := a.spotdl.do(u, a.isAdmin(u), "POST", "/jobs", map[string]string{"url": url}, &job); err != nil {
		return err
	}
	job.Mine = job.Owner == u.name || a.isAdmin(u)
	return writeJSON(w, 200, job)
}

// spotdlDelete cancela o quita un trabajo; el servicio solo lo permite a su dueño o al admin.
func (a *app) spotdlDelete(w http.ResponseWriter, r *http.Request, u user) error {
	id := r.PathValue("id")
	if !spotdlIDRe.MatchString(id) {
		return httpErr{400, "Trabajo no válido."}
	}
	if err := a.spotdl.do(u, a.isAdmin(u), "DELETE", "/jobs/"+id, nil, nil); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}

// spotdlRetry vuelve a pedir solo lo que falló; el servicio valida el dueño y los intentos.
func (a *app) spotdlRetry(w http.ResponseWriter, r *http.Request, u user) error {
	id := r.PathValue("id")
	if !spotdlIDRe.MatchString(id) {
		return httpErr{400, "Trabajo no válido."}
	}
	var job spotdlJob
	if err := a.spotdl.do(u, a.isAdmin(u), "POST", "/jobs/"+id+"/retry", nil, &job); err != nil {
		return err
	}
	job.Mine = job.Owner == u.name || a.isAdmin(u)
	return writeJSON(w, 200, job)
}
