// Package auth maneja usuarios y sesiones.
package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"palobox/internal/httpx"
)

type Service struct {
	DB    *sql.DB
	Admin string // usuario que ve el panel de administrador
	mu    sync.Mutex
}

var userRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ValidName dice si name sirve como usuario (también es el nombre de su carpeta en los datos).
func ValidName(name string) error {
	if !userRe.MatchString(name) {
		return errors.New("usuario inválido: solo minúsculas, números, - y _")
	}
	switch name { // carpetas de /srv/data que no son de un usuario
	case "media":
		return errors.New("ese nombre está reservado")
	}
	return nil
}

func hash(pass string) ([]byte, error) {
	if len(pass) < 8 {
		return nil, errors.New("la contraseña debe tener al menos 8 caracteres")
	}
	if len(pass) > 72 { // bcrypt ignora lo que pase de 72 bytes
		return nil, errors.New("la contraseña puede tener máximo 72 caracteres")
	}
	return bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
}

// AddUser crea el usuario o, si ya existe, le cambia la contraseña (comando adduser).
func (s *Service) AddUser(name, pass string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	h, err := hash(pass)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO users(name, pass) VALUES(?, ?) ON CONFLICT(name) DO UPDATE SET pass = excluded.pass`, name, h)
	return err
}

// CreateUser crea un usuario nuevo; a diferencia de AddUser, falla si ya existe.
func (s *Service) CreateUser(name, pass string) error {
	if err := ValidName(name); err != nil {
		return httpx.Error(400, err.Error())
	}
	h, err := hash(pass)
	if err != nil {
		return httpx.Error(400, err.Error())
	}
	res, err := s.DB.Exec(`INSERT INTO users(name, pass) VALUES(?, ?) ON CONFLICT(name) DO NOTHING`, name, h)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.Error(409, "Ya existe un usuario con ese nombre.")
	}
	return nil
}

// SetPassword cambia la contraseña y cierra las sesiones abiertas menos keep (la de quien la cambia):
// quien tenía la vieja queda fuera.
func (s *Service) SetPassword(id int64, pass, keep string) error {
	h, err := hash(pass)
	if err != nil {
		return httpx.Error(400, err.Error())
	}
	if _, err := s.DB.Exec(`UPDATE users SET pass = ? WHERE id = ?`, h, id); err != nil {
		return err
	}
	_, err = s.DB.Exec(`DELETE FROM sessions WHERE user_id = ? AND token != ?`, id, keep)
	return err
}

// SetDisabled bloquea o desbloquea la entrada; al bloquear cierra las sesiones.
func (s *Service) SetDisabled(id int64, off bool) error {
	if _, err := s.DB.Exec(`UPDATE users SET disabled = ? WHERE id = ?`, off, id); err != nil || !off {
		return err
	}
	return s.CloseSessions(id)
}

func (s *Service) CloseSessions(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, id)
	return err
}

// DeleteUser borra el usuario y todo lo suyo en la base. Sus archivos en disco los decide quien llama.
func (s *Service) DeleteUser(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM playlist_tracks WHERE playlist_id IN (SELECT id FROM playlists WHERE user_id = ?)`,
		`DELETE FROM playlists WHERE user_id = ?`, `DELETE FROM likes WHERE user_id = ?`, `DELETE FROM plays WHERE user_id = ?`,
		`DELETE FROM saved WHERE user_id = ?`, `DELETE FROM media WHERE user_id = ?`, `DELETE FROM sessions WHERE user_id = ?`,
		`DELETE FROM users WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// clientIP es la IP de quien entra. Detrás de tailscale serve o un proxy llega en X-Forwarded-For;
// el puerto solo escucha en 127.0.0.1, así que ese encabezado lo pone el proxy y solo se usa para mostrar.
func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Wrap exige una sesión válida y pasa el usuario a h.
func (s *Service) Wrap(h httpx.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var u httpx.User
		var seen int64
		now := time.Now().Unix()
		c, err := r.Cookie("s")
		if err == nil {
			err = s.DB.QueryRow(`SELECT u.id, u.name, MIN(u.seen, s.seen) FROM sessions s JOIN users u ON u.id = s.user_id
				WHERE s.token = ? AND s.expires > ? AND u.disabled = 0`, c.Value, now).Scan(&u.ID, &u.Name, &seen)
		}
		if err != nil {
			http.Error(w, "no autenticado", 401)
			return
		}
		u.Admin = u.Name == s.Admin
		if now-seen > 60 { // último uso del usuario y de esta sesión, para el panel; una escritura por minuto como mucho
			s.DB.Exec(`UPDATE users SET seen = ? WHERE id = ?`, now, u.ID)
			s.DB.Exec(`UPDATE sessions SET seen = ? WHERE token = ?`, now, c.Value)
		}
		if err := h(w, r, u); err != nil {
			httpx.Fail(w, err)
		}
	}
}

func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Pass string }
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	var id int64
	var pass []byte
	var off bool
	// Un intento a la vez, y el fallido cuesta 1 s con el candado tomado: frena la fuerza
	// bruta aunque lleguen en paralelo.
	s.mu.Lock()
	err := s.DB.QueryRow(`SELECT id, pass, disabled FROM users WHERE name = ?`, in.Name).Scan(&id, &pass, &off)
	ok := err == nil && bcrypt.CompareHashAndPassword(pass, []byte(in.Pass)) == nil
	if !ok {
		time.Sleep(time.Second)
	}
	s.mu.Unlock()
	now, ip, agent := time.Now(), clientIP(r), cut(r.UserAgent(), 256)
	// Registro de entradas para el panel; se guardan las últimas 1000.
	s.DB.Exec(`INSERT INTO logins(at, name, ok, ip, agent) VALUES(?, ?, ?, ?, ?)`, now.Unix(), cut(in.Name, 64), ok && !off, ip, agent)
	s.DB.Exec(`DELETE FROM logins WHERE rowid <= (SELECT MAX(rowid) - 1000 FROM logins)`)
	if !ok {
		http.Error(w, "usuario o contraseña incorrectos", 401)
		return
	}
	if off { // solo se dice con la contraseña correcta: no revela qué cuentas existen
		http.Error(w, "Esta cuenta está desactivada. Habla con el administrador.", 403)
		return
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok, exp := hex.EncodeToString(b), now.Add(30*24*time.Hour)
	s.DB.Exec(`DELETE FROM sessions WHERE expires <= ?`, now.Unix())
	s.DB.Exec(`UPDATE users SET seen = ? WHERE id = ?`, now.Unix(), id)
	if _, err := s.DB.Exec(`INSERT INTO sessions(token, user_id, expires, created, seen, ip, agent) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		tok, id, exp.Unix(), now.Unix(), now.Unix(), ip, agent); err != nil {
		httpx.Fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "s", Value: tok, Path: "/", Expires: exp, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	httpx.WriteJSON(w, 200, map[string]string{"name": in.Name})
}

func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("s"); err == nil {
		s.DB.Exec(`DELETE FROM sessions WHERE token = ?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "s", Path: "/", MaxAge: -1})
}
