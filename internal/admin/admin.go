// Package admin es el panel del administrador: quién usa la app, cuánto consume el servidor,
// y el control de usuarios, sesiones y biblioteca de música.
package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"palobox/internal/auth"
	"palobox/internal/httpx"
	"palobox/internal/music"
)

type Handler struct {
	DB    *sql.DB
	Data  string
	Auth  *auth.Service
	Music *music.Library
}

var errOnlyAdmin = httpx.Error(403, "solo para el administrador")

func dirSize(root string) (n int64) {
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if i, err := d.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	if !u.Admin {
		return errOnlyAdmin
	}
	type usage struct {
		Name     string `json:"name"`
		Seen     int64  `json:"seen"` // último uso, en segundos; 0 = nunca
		Sessions int    `json:"sessions"`
		Media    int    `json:"media"`
		Bytes    int64  `json:"bytes"`
		Disabled bool   `json:"disabled,omitempty"`
		Music    bool   `json:"musicOnly,omitempty"`
		Pending  bool   `json:"pending,omitempty"` // aún no ha creado su contraseña
		Invite   int64  `json:"invite,omitempty"`  // vencimiento de su enlace vigente; 0 = no tiene
		Admin    bool   `json:"admin,omitempty"`
	}
	now := time.Now().Unix()
	rows, err := h.DB.Query(`SELECT u.name, u.seen, u.disabled, u.music_only, length(u.pass) = 0,
		COALESCE((SELECT i.expires FROM invites i WHERE i.user_id = u.id AND i.expires > ?), 0),
		(SELECT COUNT(*) FROM sessions s WHERE s.user_id = u.id AND s.expires > ?),
		(SELECT COUNT(*) FROM media m WHERE m.user_id = u.id)
		FROM users u ORDER BY u.seen DESC, u.name`, now, now)
	if err != nil {
		return err
	}
	defer rows.Close()
	users, isUser := []usage{}, map[string]bool{}
	for rows.Next() {
		var x usage
		if err := rows.Scan(&x.Name, &x.Seen, &x.Disabled, &x.Music, &x.Pending, &x.Invite, &x.Sessions, &x.Media); err != nil {
			return err
		}
		x.Admin = x.Name == h.Auth.Admin
		x.Music = x.Music && !x.Admin
		users, isUser[x.Name] = append(users, x), true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// shortcut: recorre todo el disco en cada consulta; guardar en caché si el panel tarda más de un par de segundos.
	for i := range users {
		users[i].Bytes = dirSize(filepath.Join(h.Data, users[i].Name))
	}
	other := []usage{} // carpetas que no son de un usuario: media, .palobox
	es, _ := os.ReadDir(h.Data)
	for _, e := range es {
		if e.IsDir() && !isUser[e.Name()] {
			other = append(other, usage{Name: e.Name(), Bytes: dirSize(filepath.Join(h.Data, e.Name()))})
		}
	}

	// /proc dentro del contenedor muestra los valores del servidor completo, no solo los de la app.
	var load [3]float64
	b, _ := os.ReadFile("/proc/loadavg")
	fmt.Sscan(string(b), &load[0], &load[1], &load[2])
	var up float64
	b, _ = os.ReadFile("/proc/uptime")
	fmt.Sscan(string(b), &up)
	mem := map[string]int64{}
	b, _ = os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		var k string
		var v int64
		if n, _ := fmt.Sscan(l, &k, &v); n == 2 {
			mem[k] = v << 10 // vienen en kB
		}
	}
	var st syscall.Statfs_t
	syscall.Statfs(h.Data, &st)
	sessions, err := h.sessions(r)
	if err != nil {
		return err
	}
	logins, err := h.logins()
	if err != nil {
		return err
	}
	var failed int // entradas fallidas en las últimas 24 h, para la alerta
	h.DB.QueryRow(`SELECT COUNT(*) FROM logins WHERE ok = 0 AND at > ?`, time.Now().Add(-24*time.Hour).Unix()).Scan(&failed)
	lib, err := h.Music.Status()
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, map[string]any{
		"users": users, "other": other, "sessions": sessions, "logins": logins, "failed24h": failed, "music": lib,
		"cpus": runtime.NumCPU(), "load": load, "uptime": int64(up),
		"memTotal": mem["MemTotal:"], "memFree": mem["MemAvailable:"],
		"diskTotal": st.Blocks * uint64(st.Bsize), "diskFree": st.Bavail * uint64(st.Bsize),
	})
}

type session struct {
	ID      int64  `json:"id"`
	User    string `json:"user"`
	Created int64  `json:"created"` // 0 en sesiones abiertas antes de que se registrara
	Seen    int64  `json:"seen"`
	IP      string `json:"ip"`
	Agent   string `json:"agent"`
	Current bool   `json:"current"` // la sesión desde la que se ve el panel
}

func (h *Handler) sessions(r *http.Request) ([]session, error) {
	var tok string
	if c, err := r.Cookie("s"); err == nil {
		tok = c.Value
	}
	rows, err := h.DB.Query(`SELECT s.rowid, u.name, s.created, s.seen, s.ip, s.agent, s.token = ? FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE s.expires > ? ORDER BY s.seen DESC, s.created DESC`, tok, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []session{}
	for rows.Next() {
		var s session
		if err := rows.Scan(&s.ID, &s.User, &s.Created, &s.Seen, &s.IP, &s.Agent, &s.Current); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type login struct {
	At    int64  `json:"at"`
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	IP    string `json:"ip"`
	Agent string `json:"agent"`
}

func (h *Handler) logins() ([]login, error) {
	rows, err := h.DB.Query(`SELECT at, name, ok, ip, agent FROM logins ORDER BY rowid DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []login{}
	for rows.Next() {
		var l login
		if err := rows.Scan(&l.At, &l.Name, &l.OK, &l.IP, &l.Agent); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// target es el usuario {name} de la ruta; protect impide que el administrador se bloquee o se borre a sí mismo.
func (h *Handler) target(r *http.Request, u httpx.User, protect bool) (int64, string, error) {
	if !u.Admin {
		return 0, "", errOnlyAdmin
	}
	name := r.PathValue("name")
	if protect && name == h.Auth.Admin {
		return 0, "", httpx.Error(400, "No puedes hacerle eso a la cuenta de administrador.")
	}
	var id int64
	if err := h.DB.QueryRow(`SELECT id FROM users WHERE name = ?`, name).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return 0, "", httpx.Error(404, "Ese usuario no existe.")
	} else if err != nil {
		return 0, "", err
	}
	return id, name, nil
}

var done = map[string]bool{"ok": true}

// invite responde con el código de la invitación: la interfaz arma con él el enlace que se comparte.
func invite(w http.ResponseWriter, token string, expires int64, err error) error {
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, map[string]any{"token": token, "expires": expires})
}

// CreateUser crea la cuenta sin contraseña y devuelve su invitación.
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	if !u.Admin {
		return errOnlyAdmin
	}
	var in struct {
		Name      string
		MusicOnly bool
	}
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return err
	}
	token, expires, err := h.Auth.CreateUser(in.Name, in.MusicOnly)
	return invite(w, token, expires, err)
}

// Invite genera un enlace nuevo para que el usuario defina su contraseña (invitación perdida o clave olvidada).
func (h *Handler) Invite(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, _, err := h.target(r, u, false)
	if err != nil {
		return err
	}
	token, expires, err := h.Auth.Invite(id)
	return invite(w, token, expires, err)
}

func (h *Handler) SetDisabled(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, _, err := h.target(r, u, true)
	if err != nil {
		return err
	}
	var in struct{ Disabled bool }
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return err
	}
	if err := h.Auth.SetDisabled(id, in.Disabled); err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, done)
}

// SetAccess cambia el nivel de una cuenta: solo Música o acceso completo.
func (h *Handler) SetAccess(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, _, err := h.target(r, u, true)
	if err != nil {
		return err
	}
	var in struct{ MusicOnly bool }
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return err
	}
	if err := h.Auth.SetMusicOnly(id, in.MusicOnly); err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, done)
}

// DeleteUser borra el usuario; con ?files=1 también su carpeta (fotos y archivos). Sin eso la carpeta
// queda en el disco y la recupera un usuario nuevo con el mismo nombre.
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, name, err := h.target(r, u, true)
	if err != nil {
		return err
	}
	if err := h.Auth.DeleteUser(id); err != nil {
		return err
	}
	if r.URL.Query().Get("files") == "1" {
		// name ya pasó por la validación de usuarios: sin "/" ni ".", no sale de la carpeta de datos.
		if err := os.RemoveAll(filepath.Join(h.Data, name)); err != nil {
			return err
		}
	}
	return httpx.WriteJSON(w, 200, done)
}

// CloseUserSessions saca al usuario de todos sus dispositivos.
func (h *Handler) CloseUserSessions(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	id, _, err := h.target(r, u, false)
	if err != nil {
		return err
	}
	if err := h.Auth.CloseSessions(id); err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, done)
}

// CloseSession cierra una sola sesión, por su id de la lista del panel.
func (h *Handler) CloseSession(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	if !u.Admin {
		return errOnlyAdmin
	}
	if _, err := h.DB.Exec(`DELETE FROM sessions WHERE rowid = ?`, r.PathValue("id")); err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, done)
}

// Genres lista los artistas con su género y de dónde salió: lo consultan las pruebas punta a punta (webtest/e2e.mjs).
func (h *Handler) Genres(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	if !u.Admin {
		return errOnlyAdmin
	}
	out, err := h.Music.GenreList()
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, out)
}

// Reindex vuelve a recorrer la biblioteca de música en segundo plano.
func (h *Handler) Reindex(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	if !u.Admin {
		return errOnlyAdmin
	}
	go h.Music.Reindex()
	return httpx.WriteJSON(w, 200, done)
}
