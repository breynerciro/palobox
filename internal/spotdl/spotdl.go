// Package spotdl atiende las descargas de Spotify (spotDL) para la biblioteca de música.
// La app solo autentica y reenvía: el servicio spotDL es el dueño de la cola, de su
// SQLite y del proceso, y escribe los MP3 directamente en media/musica.
// El servicio no se publica y exige el token del módulo (ver internal/module).
package spotdl

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"palobox/internal/httpx"
	"palobox/internal/module"
)

type Handler struct {
	Module module.Module // sin URL = sección desactivada
	Full   func() bool   // la biblioteca llegó a su tope de canciones: solo el administrador sigue descargando
	// Playlist crea, a quien pidió la descarga, la playlist de Spotify que terminó de bajar (una vez por trabajo).
	Playlist func(owner, job, name string, paths []string)
}

// room rechaza una descarga nueva cuando la biblioteca está llena. Se revisa al pedirla: una playlist grande
// pedida justo antes del tope puede pasarlo.
func (h *Handler) room(u httpx.User) error {
	if !u.Admin && h.Full != nil && h.Full() {
		return httpx.Error(409, "La biblioteca llegó a su tope de canciones. Pídele al administrador que haga espacio.")
	}
	return nil
}

// spotifyRe acepta los cuatro tipos de Spotify, con o sin prefijo de idioma, y descarta
// cualquier parámetro de seguimiento (?si=...).
var spotifyRe = regexp.MustCompile(`^https://open\.spotify\.com/(?:intl-[A-Za-z-]{2,10}/)?(track|album|playlist|artist)/([A-Za-z0-9]{16,40})(?:[/?#].*)?$`)
var idRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Job es un trabajo tal como lo devuelve el servicio.
type Job struct {
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
	Notice   string   `json:"notice"`           // con status finished: el aviso que la interfaz muestra una vez
	Mine     bool     `json:"mine"`             // lo calcula la app: puede cancelarla, quitarla o reintentarla
	Tracks   []string `json:"tracks,omitempty"` // del servicio a la app: las canciones de una playlist terminada, en orden
}

// spotifyURL valida el enlace en la app y lo devuelve canónico (sin idioma ni parámetros).
func spotifyURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 {
		return "", httpx.Error(400, "El enlace no es de Spotify.")
	}
	m := spotifyRe.FindStringSubmatch(raw)
	if m == nil {
		return "", httpx.Error(400, "El enlace no es de Spotify.")
	}
	return "https://open.spotify.com/" + m[1] + "/" + m[2], nil
}

// List devuelve las descargas de quien pregunta; la cola completa solo al administrador y solo si la pide
// (?all=1, el panel de administración). Sin servicio configurado responde deshabilitado para que
// la interfaz oculte la sección sin mostrar un error.
func (h *Handler) List(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	if h.Module.URL == "" {
		return httpx.WriteJSON(w, 200, map[string]any{"enabled": false, "items": []Job{}})
	}
	items := []Job{}
	if err := h.Module.Do(u, "GET", "/jobs", nil, &items); err != nil {
		return err
	}
	all := u.Admin && r.URL.Query().Get("all") == "1"
	out := []Job{}
	for i := range items {
		items[i].Mine = items[i].Owner == u.Name || u.Admin
		// La playlist se crea aunque la lista la pida otro: no depende de que su dueño vuelva a entrar.
		if j := items[i]; len(j.Tracks) > 0 && h.Playlist != nil && j.Status == "finished" && strings.Contains(j.URL, "/playlist/") {
			h.Playlist(j.Owner, j.ID, j.Title, j.Tracks)
		}
		items[i].Tracks = nil // a la interfaz no le sirven
		if all || items[i].Owner == u.Name {
			out = append(out, items[i])
		}
	}
	return httpx.WriteJSON(w, 200, map[string]any{"enabled": true, "items": out})
}

func (h *Handler) Add(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	var in struct {
		URL string `json:"url"`
	}
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return err
	}
	url, err := spotifyURL(in.URL)
	if err != nil {
		return err
	}
	if err := h.room(u); err != nil {
		return err
	}
	var job Job
	if err := h.Module.Do(u, "POST", "/jobs", map[string]string{"url": url}, &job); err != nil {
		return err
	}
	job.Mine = job.Owner == u.Name || u.Admin
	return httpx.WriteJSON(w, 200, job)
}

// Delete cancela o quita un trabajo; el servicio solo lo permite a su dueño o al admin.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		return httpx.Error(400, "Trabajo no válido.")
	}
	if err := h.Module.Do(u, "DELETE", "/jobs/"+id, nil, nil); err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, map[string]bool{"ok": true})
}

// Retry vuelve a pedir solo lo que falló; el servicio valida el dueño y los intentos.
func (h *Handler) Retry(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		return httpx.Error(400, "Trabajo no válido.")
	}
	if err := h.room(u); err != nil {
		return err
	}
	var job Job
	if err := h.Module.Do(u, "POST", "/jobs/"+id+"/retry", nil, &job); err != nil {
		return err
	}
	job.Mine = job.Owner == u.Name || u.Admin
	return httpx.WriteJSON(w, 200, job)
}

// Health es el estado del servicio para el panel de administrador: si responde, versiones y trabajos activos.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	if !u.Admin {
		return httpx.Error(403, "solo para el administrador")
	}
	if h.Module.URL == "" {
		return httpx.WriteJSON(w, 200, map[string]any{"enabled": false})
	}
	out := map[string]any{}
	if err := h.Module.Do(u, "GET", "/health", nil, &out); err != nil {
		var he httpx.Err
		errors.As(err, &he)
		return httpx.WriteJSON(w, 200, map[string]any{"enabled": true, "ok": false, "error": he.Msg})
	}
	out["enabled"] = true
	return httpx.WriteJSON(w, 200, out)
}
