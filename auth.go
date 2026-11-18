package main

// Usuarios y sesiones.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var userRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// addUser crea el usuario o, si ya existe, le cambia la contraseña.
func (a *app) addUser(name, pass string) error {
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
	_, err = a.db.Exec(`INSERT INTO users(name, pass) VALUES(?, ?) ON CONFLICT(name) DO UPDATE SET pass = excluded.pass`, name, h)
	return err
}

func (a *app) auth(h func(http.ResponseWriter, *http.Request, user) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var u user
		var seen int64
		now := time.Now().Unix()
		c, err := r.Cookie("s")
		if err == nil {
			err = a.db.QueryRow(`SELECT u.id, u.name, u.seen FROM sessions s JOIN users u ON u.id = s.user_id
				WHERE s.token = ? AND s.expires > ?`, c.Value, now).Scan(&u.id, &u.name, &seen)
		}
		if err != nil {
			http.Error(w, "no autenticado", 401)
			return
		}
		if now-seen > 60 { // último uso, para el panel de administrador; una escritura por minuto como mucho
			a.db.Exec(`UPDATE users SET seen = ? WHERE id = ?`, now, u.id)
		}
		if err := h(w, r, u); err != nil {
			fail(w, err)
		}
	}
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Pass string }
	if err := readJSON(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	var id int64
	var hash []byte
	// Un intento a la vez, y el fallido cuesta 1 s con el candado tomado: frena la fuerza
	// bruta aunque lleguen en paralelo.
	a.loginMu.Lock()
	err := a.db.QueryRow(`SELECT id, pass FROM users WHERE name = ?`, in.Name).Scan(&id, &hash)
	ok := err == nil && bcrypt.CompareHashAndPassword(hash, []byte(in.Pass)) == nil
	if !ok {
		time.Sleep(time.Second)
	}
	a.loginMu.Unlock()
	if !ok {
		http.Error(w, "usuario o contraseña incorrectos", 401)
		return
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok, exp := hex.EncodeToString(b), time.Now().Add(30*24*time.Hour)
	if _, err := a.db.Exec(`INSERT INTO sessions(token, user_id, expires) VALUES(?, ?, ?)`, tok, id, exp.Unix()); err != nil {
		fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "s", Value: tok, Path: "/", Expires: exp, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	writeJSON(w, 200, map[string]string{"name": in.Name})
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("s"); err == nil {
		a.db.Exec(`DELETE FROM sessions WHERE token = ?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "s", Path: "/", MaxAge: -1})
}
