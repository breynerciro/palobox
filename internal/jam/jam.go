// Package jam sincroniza la reproducción entre dispositivos. Una sala guarda la cola, la pista y la
// posición; cada dispositivo conectado la recibe por un flujo de eventos (SSE) y manda sus acciones como
// comandos. Sirve para dos cosas con el mismo mecanismo:
//   - Elegir en qué dispositivo suena tu música: una sala de un solo usuario con varios dispositivos.
//   - Un jam: varios usuarios en la misma sala; cada uno oye en su dispositivo y todos manejan la cola.
//
// Todo vive en memoria: un reinicio de la app termina las salas y cada dispositivo sigue con su cola local.
package jam

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"palobox/internal/httpx"
	"palobox/internal/music"
)

const (
	maxQueue   = 500
	beat       = 25 * time.Second // latido del flujo: mantiene viva la conexión tras un proxy y revisa las salas
	soloGrace  = 2 * time.Minute  // una sala personal se disuelve si a su dueño le queda un solo dispositivo
	emptyGrace = 30 * time.Minute // una sala sin nadie conectado se borra
)

type Hub struct {
	Lookup func(paths []string) ([]music.Track, error)     // pistas del índice, en el orden pedido
	More   func(n int, seed string) ([]music.Track, error) // sugerencias parecidas a seed cuando se acaba la cola
	// Audio abre una pista desde el segundo at para el flujo continuo (stream.go) y dice sus bytes por segundo.
	Audio func(path string, dur, at float64) (io.ReadCloser, float64, error)
	Lead  time.Duration // colchón del flujo continuo; 0 = streamLead (las pruebas lo acortan)

	mu    sync.Mutex
	seq   int64 // crece con cada foto enviada: el cliente descarta las que llegan en desorden
	users map[string]*user
	rooms map[string]*jamRoom
}

// conn es un dispositivo conectado al flujo de eventos.
type conn struct {
	id, name string
	wake     chan struct{} // capacidad 1: "hay cambios"
	gone     chan struct{} // se cierra si el mismo dispositivo abre otra conexión
	qrev     int           // versión de la cola que ya se le envió
}

type user struct {
	name   string
	conns  map[string]*conn
	room   *jamRoom
	active map[string]bool // dispositivos en los que suena para este usuario: uno, o varios a la vez
	nonce  string          // última acción aplicada al abrir un flujo (stream.go): el navegador puede repetir la petición
	// Flujos continuos abiertos, por dispositivo: «cut» le pide al suyo que vuelva a empezar donde va la sala.
	cuts map[string]int
	wake map[string]chan struct{}
	// Dónde empieza lo nuevo tras un «cut», en segundos del flujo: el flujo lo anota y Cmd lo devuelve.
	marks map[string]chan float64
}

type jamRoom struct {
	code    string
	open    bool // jam: admite invitados; las salas personales (elegir dispositivo) no lo están hasta que se invita
	members []*user
	queue   []music.Track
	qrev    int
	shift   int             // pistas ya escuchadas que extend recortó del principio: cada flujo corre su índice lo mismo
	manual  int             // pistas agregadas justo después de la actual: «agregar a la cola» va detrás de ellas
	sugg    map[string]bool // pistas que puso extend: no sirven de semilla para las siguientes sugerencias
	repeat  int             // 0 apagado, 1 toda la cola, 2 solo la pista actual; solo en la sala propia del flujo continuo
	pos     int
	playing bool
	at      float64   // posición en segundos en el momento stamp
	stamp   time.Time // si playing, la posición actual es at + lo transcurrido desde stamp
	streams int       // flujos continuos abiertos (stream.go): mantienen viva la sala aunque no haya otra conexión
	empty   time.Time // desde cuándo no hay nadie conectado
	solo    time.Time // desde cuándo es una sala personal con menos de dos dispositivos
}

func (r *jamRoom) dur() float64 {
	if r.pos >= 0 && r.pos < len(r.queue) {
		return r.queue[r.pos].Dur
	}
	return 0
}

func (r *jamRoom) cur(now time.Time) float64 {
	p := r.at
	if r.playing {
		p += now.Sub(r.stamp).Seconds()
	}
	if d := r.dur(); d > 0 && p > d {
		p = d
	}
	return max(p, 0)
}

// after es la pista que suena cuando termina la i con el modo de repetir rep; -1 si la cola se acaba ahí.
// find devuelve dónde está ahora la pista path que el cliente vio en el puesto i, o -1. La cola pudo recortarse
// (extend) desde la foto del cliente, y un recorte solo la corre hacia atrás. Sin path vale el índice tal cual.
func (r *jamRoom) find(i int, path string) int {
	if path == "" || i >= 0 && i < len(r.queue) && r.queue[i].Path == path {
		if i < 0 || i >= len(r.queue) {
			return -1
		}
		return i
	}
	for k := min(i, len(r.queue)-1); k >= 0; k-- {
		if r.queue[k].Path == path {
			return k
		}
	}
	return -1
}

func (r *jamRoom) after(i, rep int) int {
	switch {
	case rep == 2:
		return i
	case i+1 < len(r.queue):
		return i + 1
	case rep == 1 && len(r.queue) > 0:
		return 0
	}
	return -1
}

func (r *jamRoom) set(pos int, at float64, playing bool) {
	r.pos, r.at, r.playing, r.stamp = pos, at, playing, time.Now()
}

// Lo que recibe cada dispositivo.
type deviceView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}
type memberView struct {
	Name   string `json:"name"`
	Device string `json:"device"` // dónde le suena; vacío si en ninguno
	Online bool   `json:"online"`
}
type roomView struct {
	Code    string         `json:"code"`
	Open    bool           `json:"open"`
	Pos     int            `json:"pos"`
	Playing bool           `json:"playing"`
	At      float64        `json:"at"`
	Stamp   int64          `json:"stamp"`
	Qrev    int            `json:"qrev"`
	Manual  int            `json:"manual"`          // cuántas de las que siguen las agregó alguien a mano; el resto es la lista o sugerencias
	Queue   *[]music.Track `json:"queue,omitempty"` // solo cuando el dispositivo no tiene esta versión
	Members []memberView   `json:"members"`
}
type view struct {
	Seq     int64        `json:"seq"`
	Now     int64        `json:"now"` // reloj del servidor, para que el cliente calcule su desfase
	Me      string       `json:"me"`
	Devices []deviceView `json:"devices"`
	Room    *roomView    `json:"room"`
	Cut     *float64     `json:"cut,omitempty"` // respuesta a «cut»: segundo del flujo en que empieza lo nuevo
}

func (h *Hub) get(name string) *user {
	if h.users == nil {
		h.users, h.rooms = map[string]*user{}, map[string]*jamRoom{}
	}
	u := h.users[name]
	if u == nil {
		u = &user{name: name, conns: map[string]*conn{}}
		h.users[name] = u
	}
	return u
}

// snapshot arma la foto para un dispositivo; have es la versión de la cola que ya tiene.
func (h *Hub) snapshot(u *user, device string, have int) view {
	h.seq++
	now := time.Now()
	v := view{Seq: h.seq, Now: now.UnixMilli(), Me: device, Devices: []deviceView{}}
	for _, c := range u.conns {
		v.Devices = append(v.Devices, deviceView{c.id, c.name, u.active[c.id]})
	}
	sort.Slice(v.Devices, func(i, j int) bool {
		return v.Devices[i].Name+v.Devices[i].ID < v.Devices[j].Name+v.Devices[j].ID
	})
	if r := u.room; r != nil {
		rv := &roomView{Code: r.code, Open: r.open, Pos: r.pos, Playing: r.playing, At: r.cur(now), Stamp: now.UnixMilli(), Qrev: r.qrev, Manual: r.manual}
		if have != r.qrev {
			q := r.queue
			if q == nil {
				q = []music.Track{}
			}
			rv.Queue = &q
		}
		for _, m := range r.members {
			mv := memberView{Name: m.name, Online: len(m.conns) > 0}
			var names []string
			for _, d := range v.Devices { // ya ordenados; para otro miembro se recorren los suyos
				if m == u && d.Active {
					names = append(names, d.Name)
				}
			}
			if m != u {
				for id := range m.active {
					if c := m.conns[id]; c != nil {
						names = append(names, c.name)
					}
				}
				sort.Strings(names)
			}
			mv.Device = strings.Join(names, ", ")
			rv.Members = append(rv.Members, mv)
		}
		v.Room = rv
	}
	return v
}

// listen elige dónde suena: con add, device se suma a los que ya suenan (o se quita si ya estaba);
// sin add, pasa a sonar solo ahí.
func (u *user) listen(device string, add bool) {
	switch {
	case !add:
		u.active = map[string]bool{device: true}
	case u.active[device]:
		delete(u.active, device)
	default:
		for id := range u.active { // de paso se olvidan los que ya no están conectados
			if u.conns[id] == nil {
				delete(u.active, id)
			}
		}
		u.active[device] = true
	}
}

func (u *user) poke() {
	for _, c := range u.conns {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

// notify avisa a todos los dispositivos que ven lo mismo que u.
func notify(u *user) {
	if u.room == nil {
		u.poke()
		return
	}
	for _, m := range u.room.members {
		m.poke()
	}
}

func (h *Hub) leave(u *user) {
	r := u.room
	if r == nil {
		return
	}
	for i, m := range r.members {
		if m == u {
			r.members = append(r.members[:i], r.members[i+1:]...)
			break
		}
	}
	u.room = nil
	u.poke()
	if len(r.members) == 0 {
		delete(h.rooms, r.code)
		return
	}
	for _, m := range r.members {
		m.poke()
	}
}

// sweep disuelve las salas que ya no hacen falta y olvida a los usuarios sin nada pendiente.
func (h *Hub) sweep() {
	now := time.Now()
	for _, r := range h.rooms {
		online := false
		for _, m := range r.members {
			online = online || len(m.conns) > 0
		}
		// Un iPhone en segundo plano pierde el flujo de eventos pero sigue oyendo por el de audio: la sala es suya.
		online = online || r.streams > 0
		lonely := !r.open && len(r.members) == 1 && len(r.members[0].conns) < 2 && r.streams == 0
		switch {
		case online:
			r.empty = time.Time{}
		case r.empty.IsZero():
			r.empty = now
		}
		switch {
		case !lonely:
			r.solo = time.Time{}
		case r.solo.IsZero():
			r.solo = now
		}
		if (!r.empty.IsZero() && now.Sub(r.empty) > emptyGrace) || (lonely && now.Sub(r.solo) > soloGrace) {
			for _, m := range append([]*user(nil), r.members...) {
				h.leave(m)
			}
		}
	}
	for name, u := range h.users {
		if len(u.conns) == 0 && u.room == nil {
			delete(h.users, name)
		}
	}
}

var deviceRe = regexp.MustCompile(`^[a-z0-9]{8,32}$`)

// Events es el flujo de eventos de un dispositivo: una foto al conectarse y otra con cada cambio.
func (h *Hub) Events(w http.ResponseWriter, r *http.Request, hu httpx.User) error {
	id, name := r.URL.Query().Get("device"), strings.TrimSpace(r.URL.Query().Get("name"))
	fl, ok := w.(http.Flusher)
	if !deviceRe.MatchString(id) || !ok {
		return httpx.Error(400, "Dispositivo no válido.")
	}
	if name == "" {
		name = "Dispositivo"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	c := &conn{id: id, name: name, wake: make(chan struct{}, 1), gone: make(chan struct{})}
	h.mu.Lock()
	u := h.get(hu.Name)
	if old := u.conns[id]; old != nil {
		close(old.gone)
	}
	u.conns[id] = c
	h.sweep()
	notify(u)
	<-c.wake // el aviso propio sobra: la primera foto sale enseguida
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if u.conns[id] == c {
			delete(u.conns, id)
			notify(u)
		}
		h.sweep()
		h.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // que un proxy no retenga los eventos
	tick := time.NewTicker(beat)
	defer tick.Stop()
	for {
		h.mu.Lock()
		v := h.snapshot(u, id, c.qrev)
		if v.Room != nil {
			c.qrev = v.Room.Qrev
		} else {
			c.qrev = 0
		}
		h.mu.Unlock()
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return nil
		}
		fl.Flush()
		select {
		case <-c.wake:
		case <-tick.C:
			h.mu.Lock()
			h.sweep()
			h.mu.Unlock()
		case <-c.gone:
			return nil
		case <-r.Context().Done():
			return nil
		}
	}
}

const codeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // sin 0/O ni 1/I: el código se dicta o se escribe a mano

func (h *Hub) newCode() string {
	for {
		b := make([]byte, 6)
		rand.Read(b)
		for i := range b {
			b[i] = codeChars[int(b[i])%len(codeChars)]
		}
		if h.rooms[string(b)] == nil {
			return string(b)
		}
	}
}

var errNoRoom = httpx.Error(409, "Esa sesión ya terminó.")

// extend agrega sugerencias cuando a la cola le quedan menos de tres pistas por delante. Con la cola llena (una
// lista larga) primero recorta las ya escuchadas, dejando unas cuantas para «anterior».
func (h *Hub) extend(r *jamRoom) {
	if r.repeat == 0 && len(r.queue)-(r.pos+1) < 3 && h.More != nil && len(r.queue) > 0 {
		if cut := r.pos - 50; len(r.queue) >= maxQueue && cut > 0 {
			r.queue = append([]music.Track(nil), r.queue[cut:]...) // copia: una foto ya armada puede seguir usando la anterior
			r.pos -= cut
			r.shift += cut
			r.qrev++
		}
		have := map[string]bool{}
		for _, t := range r.queue {
			have[t.Path] = true
		}
		// La semilla es lo último que alguien puso, no una sugerencia: así la cola no se va alejando del género con
		// el que empezó. shortcut: sugg solo crece (una ruta por sugerencia); se vacía sola cuando la sala se cierra.
		last := r.queue[r.pos].Path
		for i := len(r.queue) - 1; i >= 0; i-- {
			if !r.sugg[r.queue[i].Path] {
				last = r.queue[i].Path
				break
			}
		}
		if r.sugg == nil {
			r.sugg = map[string]bool{}
		}
		// Sin semilla salen al azar: es la salida cuando todo lo parecido ya está en la cola.
		for _, seed := range []string{last, ""} {
			more, _ := h.More(40, seed)
			added := 0
			for _, t := range more {
				if added < 20 && !have[t.Path] && len(r.queue) < maxQueue {
					r.queue = append(r.queue, t)
					r.sugg[t.Path] = true
					r.qrev++
					added++
				}
			}
			if added > 0 {
				break
			}
		}
	}
}

// advance pasa a la siguiente pista (ended: porque terminó la actual, no a mano); sin repetir, si la cola se
// acaba pide sugerencias y si no hay, se detiene.
func (h *Hub) advance(r *jamRoom, ended bool) {
	h.extend(r)
	rep := r.repeat
	if !ended && rep == 2 {
		rep = 1 // «siguiente» a mano sí cambia de canción, como en el reproductor local
	}
	n := r.after(r.pos, rep)
	if n < 0 {
		r.set(r.pos, 0, false)
		return
	}
	if n > r.pos {
		r.manual = max(0, r.manual-1)
	} else if n < r.pos {
		r.manual = 0 // volvió al principio: lo agregado a mano quedó atrás
	}
	r.set(n, 0, true)
}

// Cmd aplica una acción de un dispositivo y le devuelve la foto resultante.
func (h *Hub) Cmd(w http.ResponseWriter, r *http.Request, hu httpx.User) error {
	var in struct {
		Device, Op, Code, Target, Path string
		Paths                          []string
		Pos, Index, Qrev, Repeat       int
		Manual                         int
		At                             float64
		Playing, Open, Next, Add       bool
	}
	// Una cola de 500 rutas no cabe en el límite de httpx.ReadJSON.
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || !deviceRe.MatchString(in.Device) || len(in.Paths) > maxQueue {
		return httpx.Error(400, "petición inválida")
	}
	var tracks []music.Track
	if len(in.Paths) > 0 {
		var err error
		if tracks, err = h.Lookup(in.Paths); err != nil {
			return err
		}
	}
	var mark chan float64 // «cut»: el flujo dirá dónde empieza lo nuevo
	// La foto se escribe al teléfono sin el candado: uno con mala señal no debe frenar a los demás.
	v, err := func() (view, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		u := h.get(hu.Name)
		room := u.room
		clampPos := func(n int) int { return max(0, min(in.Pos, n-1)) }
		switch in.Op {
		case "start": // crea la sala con la cola de este dispositivo; target = dónde debe sonar
			if room == nil {
				if len(tracks) > maxQueue {
					tracks = tracks[:maxQueue]
				}
				room = &jamRoom{code: h.newCode(), queue: tracks, qrev: 1}
				h.rooms[room.code] = room
				room.set(clampPos(len(tracks)), max(in.At, 0), in.Playing && len(tracks) > 0)
				room.repeat = max(0, min(in.Repeat, 2))
				room.manual = max(0, min(in.Manual, len(room.queue)-room.pos-1)) // lo que ya se había puesto en cola antes de sonar
				room.members, u.room = []*user{u}, room
				u.listen(in.Device, false)
			}
			if u.conns[in.Target] != nil {
				u.listen(in.Target, in.Add)
			}
			room.open = room.open || in.Open
			if room.open {
				room.repeat = 0 // en un jam no se repite: al acabarse la cola siguen sugerencias
			}
		case "join":
			target := h.rooms[strings.ToUpper(strings.TrimSpace(in.Code))]
			if target == nil || !target.open {
				return view{}, httpx.Error(404, "No hay un jam con ese código.")
			}
			if room != target {
				h.leave(u)
				target.members, u.room = append(target.members, u), target
			}
			u.listen(in.Device, false)
			room = target
		case "leave":
			h.leave(u)
			room = nil
		case "state": // solo la foto: el teléfono en segundo plano pregunta así cuando pierde el flujo de eventos
		case "cut":
			// El dispositivo va a saltarse su colchón (stream.go): su flujo vuelve a empezar en lo que la sala suena
			// ahora mismo, y lo que llegue después de esta respuesta ya es eso.
			if u.cuts == nil {
				u.cuts = map[string]int{}
			}
			u.cuts[in.Device]++
			if u.marks == nil {
				u.marks = map[string]chan float64{}
			}
			mark = make(chan float64, 1)
			u.marks[in.Device] = mark
			if ch := u.wake[in.Device]; ch != nil {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		default:
			if room == nil {
				return view{}, errNoRoom
			}
			now := time.Now()
			switch in.Op {
			case "open":
				room.open, room.repeat = true, 0
			case "repeat":
				if !room.open {
					room.repeat = max(0, min(in.Repeat, 2))
				}
			case "here":
				if u.conns[in.Target] == nil {
					return view{}, httpx.Error(409, "Ese dispositivo ya no está conectado.")
				}
				u.listen(in.Target, in.Add)
			case "queue": // alguien puso a sonar otra lista
				if len(tracks) == 0 {
					return view{}, httpx.Error(404, "Esa música ya no está en la biblioteca.")
				}
				room.queue, room.manual = tracks, 0
				room.qrev++
				room.set(clampPos(len(tracks)), 0, true)
			case "add":
				if len(tracks) == 0 {
					return view{}, httpx.Error(404, "Esa canción ya no está en la biblioteca.")
				}
				if len(room.queue)+len(tracks) > maxQueue {
					return view{}, httpx.Error(409, "La cola está llena.")
				}
				if len(room.queue) == 0 {
					room.queue = tracks
					room.set(0, 0, true)
				} else {
					i := room.pos + 1
					if !in.Next {
						i += room.manual
					}
					i = min(i, len(room.queue))
					room.queue = append(room.queue[:i:i], append(tracks, room.queue[i:]...)...)
					room.manual += len(tracks)
				}
				room.qrev++
			case "remove":
				i := in.Index
				if i <= room.pos || i >= len(room.queue) || room.queue[i].Path != in.Path {
					break // la cola cambió mientras tanto: la foto que vuelve lo muestra
				}
				if i <= room.pos+room.manual {
					room.manual--
				}
				room.queue = append(room.queue[:i:i], room.queue[i+1:]...)
				room.qrev++
			case "tail": // reemplaza lo que sigue después de la pista actual (aleatorio en el flujo continuo)
				if len(room.queue) == 0 {
					break
				}
				keep := room.pos + 1
				if len(tracks) > maxQueue-keep {
					tracks = tracks[:max(0, maxQueue-keep)]
				}
				// manual: cuántas del principio son de la cola del usuario (al reordenar o quitar desde la cola).
				room.queue, room.manual = append(room.queue[:keep:keep], tracks...), max(0, min(in.Manual, len(tracks)))
				room.qrev++
			case "jump":
				i := room.find(in.Index, in.Path)
				if i < 0 {
					break // la cola cambió mientras tanto: la foto que vuelve lo muestra
				}
				room.manual = max(0, room.manual-(i-room.pos))
				room.set(i, 0, true)
			case "next":
				h.advance(room, false)
			case "prev":
				room.manual = 0
				room.set(max(room.pos-1, 0), 0, room.playing || room.pos > 0)
			case "play":
				room.set(room.pos, room.cur(now), len(room.queue) > 0)
			case "pause":
				room.set(room.pos, room.cur(now), false)
			case "seek":
				at := max(in.At, 0)
				if d := room.dur(); d > 0 {
					at = min(at, d)
				}
				room.set(room.pos, at, room.playing)
			default:
				return view{}, httpx.Error(400, "petición inválida")
			}
		}
		h.sweep()
		notify(u)
		return h.snapshot(u, in.Device, in.Qrev), nil
	}()
	if err != nil {
		return err
	}
	if mark != nil {
		select {
		case at := <-mark:
			v.Cut = &at
		case <-time.After(2 * time.Second): // el flujo está trabado o ya no existe: el teléfono salta como antes
		case <-r.Context().Done():
		}
	}
	return httpx.WriteJSON(w, 200, v)
}
