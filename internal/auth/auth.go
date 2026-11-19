// Package auth maneja usuarios y sesiones.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
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
	tries map[string]*tries // intentos de entrada por IP; lo protege mu
}

// tries lleva los fallos seguidos de una IP. busy: tiene un intento en curso (o pagando la espera del fallo).
type tries struct {
	busy bool
	n    int
	last time.Time
}

// penalty es lo que espera la respuesta tras el fallo n seguido: 1 s los tres primeros (un error de dedo no se
// nota más que antes) y luego el doble cada vez, hasta 30 s. Con un solo intento a la vez por IP, un ataque
// desde una IP queda en ~120 claves por hora.
func penalty(n int) time.Duration {
	if n <= 3 {
		return time.Second
	}
	return min(time.Second<<(n-3), 30*time.Second)
}

// forget: tras este tiempo sin fallar, la IP empieza de cero y su entrada se puede borrar.
const forget = 15 * time.Minute

// begin reserva el turno de ip para un intento con clave. Si ya tiene uno en curso devuelve false: un
// intento a la vez por IP, sin frenar a las demás ni a los aciertos entre sí.
func (s *Service) begin(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tries == nil {
		s.tries = map[string]*tries{}
	}
	now := time.Now()
	if len(s.tries) > 1000 { // limpieza: que el mapa no crezca sin fin con IP que ya no vuelven
		for k, t := range s.tries {
			if !t.busy && now.Sub(t.last) > forget {
				delete(s.tries, k)
			}
		}
	}
	t := s.tries[ip]
	if t == nil {
		t = &tries{}
		s.tries[ip] = t
	}
	if t.busy {
		return false
	}
	if now.Sub(t.last) > forget {
		t.n = 0
	}
	t.busy = true
	return true
}

// end cierra el intento de ip. Un acierto olvida sus fallos; un fallo hace esperar la respuesta, con el turno
// aún tomado, así que esa IP no puede seguir probando mientras tanto.
func (s *Service) end(ip string, ok bool) {
	s.mu.Lock()
	if ok {
		delete(s.tries, ip)
		s.mu.Unlock()
		return
	}
	t := s.tries[ip]
	t.n++
	wait := penalty(t.n)
	s.mu.Unlock()
	time.Sleep(wait)
	s.mu.Lock()
	t.busy, t.last = false, time.Now()
	s.mu.Unlock()
}

// errBusy: la misma IP ya tiene un intento en curso.
var errBusy = httpx.Error(429, "Espera un momento antes de volver a intentar.")

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

// CreateUser crea un usuario nuevo sin contraseña y devuelve el código de su invitación: él mismo la define al
// abrir el enlace. A diferencia de AddUser, falla si ya existe.
func (s *Service) CreateUser(name string, musicOnly bool) (token string, expires int64, err error) {
	if err := ValidName(name); err != nil {
		return "", 0, httpx.Error(400, err.Error())
	}
	// Contraseña vacía: ninguna clave coincide con ella, así que la cuenta no abre hasta que se use la invitación.
	res, err := s.DB.Exec(`INSERT INTO users(name, pass, music_only) VALUES(?, '', ?) ON CONFLICT(name) DO NOTHING`, name, musicOnly)
	if err != nil {
		return "", 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", 0, httpx.Error(409, "Ya existe un usuario con ese nombre.")
	}
	id, _ := res.LastInsertId()
	return s.Invite(id)
}

// InviteTTL es lo que dura un enlace de invitación sin usar.
const InviteTTL = 7 * 24 * time.Hour

func inviteHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Invite genera un código de un solo uso para que el usuario defina su contraseña (cuenta nueva o clave
// olvidada) y anula el anterior. En la base solo queda su huella: el código no se puede volver a mostrar.
func (s *Service) Invite(id int64) (token string, expires int64, err error) {
	b := make([]byte, 32)
	rand.Read(b)
	token, expires = hex.EncodeToString(b), time.Now().Add(InviteTTL).Unix()
	_, err = s.DB.Exec(`INSERT INTO invites(hash, user_id, expires) VALUES(?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET hash = excluded.hash, expires = excluded.expires`, inviteHash(token), id, expires)
	return token, expires, err
}

var errInvite = httpx.Error(404, "Este enlace ya se usó o venció. Pídele uno nuevo al administrador.")

// invited busca la invitación vigente de token, con el mismo freno por IP que Login.
func (s *Service) invited(r *http.Request, token string) (id int64, name string, err error) {
	ip := clientIP(r)
	if !s.begin(ip) {
		return 0, "", errBusy
	}
	err = s.DB.QueryRow(`SELECT u.id, u.name FROM invites i JOIN users u ON u.id = i.user_id
		WHERE i.hash = ? AND i.expires > ? AND u.disabled = 0`, inviteHash(token), time.Now().Unix()).Scan(&id, &name)
	s.end(ip, err == nil)
	if err != nil {
		return 0, "", errInvite
	}
	return id, name, nil
}

// InviteInfo dice para qué usuario es un enlace, antes de pedirle la contraseña.
func (s *Service) InviteInfo(w http.ResponseWriter, r *http.Request) {
	_, name, err := s.invited(r, r.URL.Query().Get("token"))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]string{"name": name})
}

// Accept define la contraseña con una invitación, la gasta, cierra las sesiones que hubiera y deja al usuario dentro.
func (s *Service) Accept(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, Pass string }
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	h, err := hash(in.Pass)
	if err != nil {
		httpx.Fail(w, httpx.Error(400, err.Error()))
		return
	}
	id, name, err := s.invited(r, in.Token)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	// Borrar la invitación primero y comprobar que se borró: dos usos simultáneos del mismo enlace no pasan los dos.
	res, err := s.DB.Exec(`DELETE FROM invites WHERE hash = ?`, inviteHash(in.Token))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpx.Fail(w, errInvite)
		return
	}
	if _, err := s.DB.Exec(`UPDATE users SET pass = ? WHERE id = ?`, h, id); err != nil {
		httpx.Fail(w, err)
		return
	}
	s.CloseSessions(id)
	if err := s.open(w, r, id); err != nil {
		httpx.Fail(w, err)
		return
	}
	s.DB.Exec(`INSERT INTO logins(at, name, ok, ip, agent) VALUES(?, ?, 1, ?, ?)`, time.Now().Unix(), name, clientIP(r), cut(r.UserAgent(), 256)) // también es una entrada
	httpx.WriteJSON(w, 200, map[string]string{"name": name})
}

// open abre una sesión para el usuario id y la deja en la cookie.
func (s *Service) open(w http.ResponseWriter, r *http.Request, id int64) error {
	now := time.Now()
	b := make([]byte, 32)
	rand.Read(b)
	tok, exp := hex.EncodeToString(b), now.Add(sessionTTL)
	s.DB.Exec(`DELETE FROM sessions WHERE expires <= ?`, now.Unix())
	s.DB.Exec(`UPDATE users SET seen = ? WHERE id = ?`, now.Unix(), id)
	if _, err := s.DB.Exec(`INSERT INTO sessions(token, user_id, expires, created, seen, ip, agent) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		tok, id, exp.Unix(), now.Unix(), now.Unix(), clientIP(r), cut(r.UserAgent(), 256)); err != nil {
		return err
	}
	setCookie(w, r, tok, exp)
	return nil
}

// sessionTTL: una sesión dura este tiempo desde su último uso. Quien usa la app no vuelve a pedir la clave; una
// sesión que el servidor ya no reconoce es entonces una cuenta eliminada, cerrada por el administrador o
// abandonada, y el celular borra lo que tenía descargado (web/session.js).
const sessionTTL = 30 * 24 * time.Hour

func setCookie(w http.ResponseWriter, r *http.Request, tok string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{Name: "s", Value: tok, Path: "/", Expires: exp, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
}

// SetDisabled bloquea o desbloquea la entrada; al bloquear cierra las sesiones.
func (s *Service) SetDisabled(id int64, off bool) error {
	if _, err := s.DB.Exec(`UPDATE users SET disabled = ? WHERE id = ?`, off, id); err != nil || !off {
		return err
	}
	return s.CloseSessions(id)
}

// SetMusicOnly limita la cuenta a Música o le devuelve el acceso completo. No borra nada: si tenía fotos o
// archivos, siguen en el disco y vuelven a verse al darle acceso completo.
func (s *Service) SetMusicOnly(id int64, on bool) error {
	_, err := s.DB.Exec(`UPDATE users SET music_only = ? WHERE id = ?`, on, id)
	return err
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
		`DELETE FROM invites WHERE user_id = ?`,
		`DELETE FROM users WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// clientIP es la IP de quien entra: frena los intentos de clave y se muestra en el panel. Las cabeceras del
// proxy solo valen si la conexión viene de él; si no, cualquiera inventaría una IP por intento.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Suposición (compose.yml): el puerto solo se publica en 127.0.0.1 del host y Docker entrega esas conexiones desde
	// la puerta de enlace de su red (privada), así que loopback o privada = tailscale serve, cloudflared u otro contenedor.
	if ip := net.ParseIP(host); ip == nil || !(ip.IsLoopback() || ip.IsPrivate()) {
		return host
	}
	if f := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); f != "" {
		return f
	}
	if f := r.Header.Get("X-Forwarded-For"); f != "" { // el último lo agregó nuestro proxy; los anteriores los manda el cliente
		return strings.TrimSpace(f[strings.LastIndex(f, ",")+1:])
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
		var seen, expires int64
		now := time.Now().Unix()
		c, err := r.Cookie("s")
		if err == nil {
			err = s.DB.QueryRow(`SELECT u.id, u.name, u.music_only, MIN(u.seen, s.seen), s.expires FROM sessions s JOIN users u ON u.id = s.user_id
				WHERE s.token = ? AND s.expires > ? AND u.disabled = 0`, c.Value, now).Scan(&u.ID, &u.Name, &u.MusicOnly, &seen, &expires)
		}
		if err != nil {
			http.Error(w, "no autenticado", 401)
			return
		}
		u.Admin = u.Name == s.Admin
		u.MusicOnly = u.MusicOnly && !u.Admin // el administrador siempre tiene acceso completo
		if now-seen > 60 {                    // último uso del usuario y de esta sesión, para el panel; una escritura por minuto como mucho
			s.DB.Exec(`UPDATE users SET seen = ? WHERE id = ?`, now, u.ID)
			s.DB.Exec(`UPDATE sessions SET seen = ? WHERE token = ?`, now, c.Value)
		}
		if exp := time.Now().Add(sessionTTL); exp.Unix()-expires > 24*3600 { // se renueva con el uso, una vez al día como mucho
			s.DB.Exec(`UPDATE sessions SET expires = ? WHERE token = ?`, exp.Unix(), c.Value)
			setCookie(w, r, c.Value, exp)
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
	// Freno a la fuerza bruta por IP: un intento a la vez y cada fallo hace esperar más (ver penalty).
	ip := clientIP(r)
	if !s.begin(ip) {
		httpx.Fail(w, errBusy)
		return
	}
	err := s.DB.QueryRow(`SELECT id, pass, disabled FROM users WHERE name = ?`, in.Name).Scan(&id, &pass, &off)
	ok := err == nil && bcrypt.CompareHashAndPassword(pass, []byte(in.Pass)) == nil
	s.end(ip, ok)
	now, agent := time.Now(), cut(r.UserAgent(), 256)
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
	if err := s.open(w, r, id); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]string{"name": in.Name})
}

func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("s"); err == nil {
		s.DB.Exec(`DELETE FROM sessions WHERE token = ?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "s", Path: "/", MaxAge: -1})
}
