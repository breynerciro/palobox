package jam

import (
	"errors"
	"log"
	"time"

	"palobox/internal/push"
)

// Dispositivos dormidos: la app de Android con la pantalla bloqueada y en silencio pierde su flujo de eventos (el
// sistema la congela), pero dejó un token de push. Sigue en la lista de dispositivos y, si le pasan la música, un
// aviso la despierta (Hub.Push) y ella sola se conecta y suena.

const sleepFor = 30 * 24 * time.Hour // un token que no se renueva en este tiempo es de una app que ya no se usa

type sleeper struct{ name, token string }

// sleepers carga (una vez) los dispositivos con push de u. Se llama con h.mu tomado.
func (h *Hub) sleepers(u *user) map[string]sleeper {
	if u.sleep != nil || h.DB == nil {
		return u.sleep
	}
	u.sleep = map[string]sleeper{}
	rows, err := h.DB.Query(`SELECT device, name, token FROM push_devices WHERE user = ? AND updated > ?`, u.name, time.Now().Add(-sleepFor).Unix())
	if err != nil {
		log.Printf("jam: dispositivos dormidos de %s: %v", u.name, err)
		return u.sleep
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var s sleeper
		if rows.Scan(&id, &s.name, &s.token) == nil {
			u.sleep[id] = s
		}
	}
	return u.sleep
}

// remember guarda el token de push de un dispositivo conectado. Se llama con h.mu tomado.
func (h *Hub) remember(u *user, device, token string) {
	c := u.conns[device]
	if h.DB == nil || c == nil || token == "" || len(token) > 4096 {
		return
	}
	h.sleepers(u)[device] = sleeper{c.name, token}
	if _, err := h.DB.Exec(`INSERT INTO push_devices(user, device, name, token, updated) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(user, device) DO UPDATE SET name = excluded.name, token = excluded.token, updated = excluded.updated`,
		u.name, device, c.name, token, time.Now().Unix()); err != nil {
		log.Printf("jam: guardar token de %s: %v", u.name, err)
	}
}

// reachable: el dispositivo está conectado, o dormido pero se puede despertar.
func (h *Hub) reachable(u *user, device string) bool {
	_, ok := h.sleepers(u)[device]
	return u.conns[device] != nil || (ok && h.Push != nil)
}

// wake despierta el dispositivo de u al que le pasaron la música, si tiene push. Va aunque parezca conectado: tras
// el túnel de Cloudflare una conexión de una app ya cerrada sigue pareciendo viva varios minutos, y a una despierta
// el aviso solo la hace reconectarse. Se llama con h.mu tomado; el envío va aparte para no frenar la sala.
func (h *Hub) wake(u *user, id string) {
	s, ok := h.sleepers(u)[id]
	if h.Push == nil || !ok || !u.active[id] {
		return
	}
	go func() {
		err := h.Push(s.token)
		if err == nil {
			return
		}
		log.Printf("jam: despertar %q de %s: %v", s.name, u.name, err)
		if errors.Is(err, push.ErrGone) {
			h.mu.Lock()
			delete(u.sleep, id)
			h.mu.Unlock()
			h.DB.Exec(`DELETE FROM push_devices WHERE user = ? AND device = ?`, u.name, id)
		}
	}()
}

// asleep: los dispositivos de u que se pueden despertar y no están conectados, para la foto. owner: ver deviceView.User.
func (h *Hub) asleep(u *user, owner string) []deviceView {
	if h.Push == nil {
		return nil
	}
	var out []deviceView
	for id, s := range h.sleepers(u) {
		if u.conns[id] == nil {
			out = append(out, deviceView{id, s.name, u.active[id], owner, true})
		}
	}
	return out
}
