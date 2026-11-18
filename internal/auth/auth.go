// Package auth maneja usuarios y sesiones.
package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
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

// AddUser crea el usuario o, si ya existe, le cambia la contraseña.
func (s *Service) AddUser(name, pass string) error {
	if !userRe.MatchString(name) {
		return errors.New("usuario inválido: solo minúsculas, números, - y _")
	}
	switch name { // carpetas de /srv/data que no son de un usuario
	case "media":
		return errors.New("ese nombre está reservado")
	}
	if len(pass) < 8 {
		return errors.New("la contraseña debe tener al menos 8 caracteres")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO users(name, pass) VALUES(?, ?) ON CONFLICT(name) DO UPDATE SET pass = excluded.pass`, name, h)
	return err
}

// Wrap exige una sesión válida y pasa el usuario a h.
func (s *Service) Wrap(h httpx.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var u httpx.User
		var seen int64
		now := time.Now().Unix()
		c, err := r.Cookie("s")
		if err == nil {
			err = s.DB.QueryRow(`SELECT u.id, u.name, u.seen FROM sessions s JOIN users u ON u.id = s.user_id
				WHERE s.token = ? AND s.expires > ?`, c.Value, now).Scan(&u.ID, &u.Name, &seen)
		}
		if err != nil {
			http.Error(w, "no autenticado", 401)
			return
		}
		u.Admin = u.Name == s.Admin
		if now-seen > 60 { // último uso, para el panel de administrador; una escritura por minuto como mucho
			s.DB.Exec(`UPDATE users SET seen = ? WHERE id = ?`, now, u.ID)
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
	var hash []byte
	// Un intento a la vez, y el fallido cuesta 1 s con el candado tomado: frena la fuerza
	// bruta aunque lleguen en paralelo.
	s.mu.Lock()
	err := s.DB.QueryRow(`SELECT id, pass FROM users WHERE name = ?`, in.Name).Scan(&id, &hash)
	ok := err == nil && bcrypt.CompareHashAndPassword(hash, []byte(in.Pass)) == nil
	if !ok {
		time.Sleep(time.Second)
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "usuario o contraseña incorrectos", 401)
		return
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok, exp := hex.EncodeToString(b), time.Now().Add(30*24*time.Hour)
	if _, err := s.DB.Exec(`INSERT INTO sessions(token, user_id, expires) VALUES(?, ?, ?)`, tok, id, exp.Unix()); err != nil {
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
