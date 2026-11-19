package jam

// Flujo continuo: el audio de la sala por una sola respuesta, como una emisora.
//
// iOS deja que una página siga sonando en segundo plano mientras sea el mismo audio, pero a veces bloquea cargar
// uno nuevo, que es lo que pasa al terminar cada canción. Con el flujo el teléfono nunca cambia de fuente: el
// servidor manda la pista de la sala y, cuando termina, la que sigue en la cola, por la misma conexión.
//
// La regla es una sola: en cada momento se manda lo que la sala va a estar sonando dentro de streamLead (el
// colchón del reproductor). Si la sala sigue su curso, eso es continuar con lo que ya se venía mandando; si
// alguien saltó de canción o adelantó, lo que toca ya no es continuación y el envío cambia ahí mismo: quien oye
// en segundo plano nota el cambio cuando se le acaba el colchón, y quien tiene la app abierta reabre el flujo
// (player.js) y lo oye de inmediato.
//
// Para oírlo de inmediato también en segundo plano, el teléfono puede saltarse su colchón sin soltar el audio:
// manda «cut», este envío vuelve a empezar en lo que la sala suena en ese instante (y rellena el colchón), y el
// teléfono se adelanta dentro del mismo audio hasta donde empezó a llegar lo nuevo.

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"palobox/internal/httpx"
	"palobox/internal/music"
)

const (
	streamLead = 6 * time.Second        // cuánto se adelanta el envío a lo que suena: colchón contra cortes de red
	streamTick = 200 * time.Millisecond // cada cuánto se manda audio y se mira la sala
	streamIdle = 30 * time.Minute       // una sala en pausa más de este tiempo suelta el flujo
	streamSlip = 2.5                    // segundos de diferencia entre lo enviado y lo que toca que aún cuentan como continuación
)

// target es lo que la sala estará sonando dentro de lead: índice en la cola y segundo de esa pista. Llamar con h.mu.
func (h *Hub) target(r *jamRoom, now time.Time, lead time.Duration) (int, float64) {
	idx, at := r.pos, r.at+max(now.Sub(r.stamp).Seconds(), 0)+lead.Seconds()
	for idx < len(r.queue) {
		d := r.queue[idx].Dur
		if d <= 0 || at < d {
			break
		}
		if idx+1 >= len(r.queue) {
			h.extend(r) // la cola se acaba dentro del colchón: las sugerencias se piden ya
			if idx+1 >= len(r.queue) {
				return idx, d
			}
		}
		at -= d
		idx++
	}
	return idx, at
}

// Stream manda el audio de la sala al dispositivo hasta que la sala se pausa, se acaba o él cierra la conexión.
func (h *Hub) Stream(w http.ResponseWriter, r *http.Request, hu httpx.User) error {
	fl, ok := w.(http.Flusher)
	if !ok || h.Audio == nil {
		return httpx.Error(404, "no disponible")
	}
	h.mu.Lock()
	u := h.get(hu.Name)
	room := u.room
	if room == nil || len(room.queue) == 0 {
		h.mu.Unlock()
		return errNoRoom
	}
	// Saltar, devolver o adelantar viajan en la misma petición que abre el audio nuevo: el teléfono la hace dentro
	// del toque (lo único que iOS deja con la pantalla bloqueada) y lo que llega ya es la pista pedida, sin esperar
	// una respuesta aparte mientras sigue sonando la anterior. «n» identifica la acción: Safari puede pedir la
	// misma dirección más de una vez y no debe saltar dos canciones.
	q := r.URL.Query()
	device := q.Get("device")
	wake := make(chan struct{}, 1)
	if u.wake == nil {
		u.wake = map[string]chan struct{}{}
	}
	u.wake[device] = wake
	cuts := u.cuts[device]
	if op, n := q.Get("op"), q.Get("n"); op != "" && n != "" && u.nonce != n {
		u.nonce = n
		from, _ := strconv.Atoi(q.Get("from"))
		index, _ := strconv.Atoi(q.Get("index"))
		at, _ := strconv.ParseFloat(q.Get("at"), 64)
		switch {
		case op == "next" && from == room.pos:
			h.advance(room)
		case op == "prev" && from == room.pos:
			room.manual = 0
			room.set(max(room.pos-1, 0), 0, true)
		case op == "jump" && index >= 0 && index < len(room.queue):
			room.manual = max(0, room.manual-(index-room.pos))
			room.set(index, 0, true)
		case op == "seek":
			if d := room.dur(); d > 0 {
				at = min(at, d)
			}
			room.set(room.pos, max(at, 0), true)
		}
		for _, m := range room.members {
			m.poke()
		}
	}
	room.streams++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if u.wake[device] == wake {
			delete(u.wake, device)
		}
		room.streams--
		h.sweep()
		h.mu.Unlock()
	}()

	// Sin Content-Length ni rangos: para el reproductor es una emisión en vivo, no un archivo.
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(200)
	fl.Flush() // la respuesta empieza ya: el reproductor no espera al primer trozo de audio para saber que hay flujo

	var (
		src   io.ReadCloser // la pista que se está mandando
		path  string
		idx   = -1
		at    float64 // segundo de esa pista hasta donde ya se mandó
		rate  float64 // sus bytes por segundo
		dur   float64
		first = true
		gone  bool // el teléfono cerró la conexión
		buf   = make([]byte, 32<<10)
	)
	closeSrc := func() {
		if src != nil {
			src.Close()
			src = nil
		}
	}
	defer closeSrc()
	// open deja lista la pista t desde el segundo from; falso si ya no se puede leer.
	open := func(t music.Track, i int, from float64) bool {
		closeSrc()
		f, br, err := h.Audio(t.Path, t.Dur, from)
		if err != nil || br <= 0 {
			return false
		}
		src, path, idx, at, rate, dur = f, t.Path, i, from, br, t.Dur
		return true
	}
	// pump manda secs segundos de la pista abierta; dice si el archivo se acabó antes.
	pump := func(secs float64) (eof bool) {
		for n := int64(secs * rate); n > 0 && !gone; {
			k, err := src.Read(buf[:min(int64(len(buf)), n)])
			if k > 0 {
				if _, werr := w.Write(buf[:k]); werr != nil {
					gone = true
				}
				n -= int64(k)
				at += float64(k) / rate
			}
			if err != nil {
				at = max(at, dur) // la pista se mandó entera, dure lo que diga el índice
				return true
			}
		}
		return false
	}
	tick := time.NewTicker(streamTick)
	defer tick.Stop()
	full := h.Lead
	if full <= 0 {
		full = streamLead
	}
	leadS := full.Seconds()
	for !gone {
		h.mu.Lock()
		now := time.Now()
		if u.room != room || len(room.queue) == 0 {
			h.mu.Unlock()
			return nil
		}
		// En pausa la conexión se queda abierta sin mandar nada: el teléfono guarda su colchón y, al reanudar,
		// sigue con el mismo audio sin cargar nada (lo que iOS no siempre deja hacer en segundo plano).
		if !room.playing {
			idle := room.stamp
			h.mu.Unlock()
			if now.Sub(idle) > streamIdle {
				return nil
			}
			select {
			case <-tick.C:
				continue
			case <-wake:
				continue
			case <-r.Context().Done():
				return nil
			}
		}
		// La pista de la sala llegó a su final: avanza el servidor (un teléfono en segundo plano no avisa), y con
		// la hora exacta del final para que la sala no se atrase respecto a lo que ya se mandó.
		if d := room.queue[room.pos].Dur; d > 0 && room.at+now.Sub(room.stamp).Seconds() >= d {
			end := room.stamp.Add(time.Duration((d - room.at) * float64(time.Second)))
			h.advance(room)
			if room.playing {
				room.stamp = end
			}
			for _, m := range room.members {
				m.poke()
			}
			if !room.playing { // se acabó la cola
				h.mu.Unlock()
				return nil
			}
		}
		if c := u.cuts[device]; c != cuts {
			cuts, first = c, true // el teléfono se va a saltar su colchón: se vuelve a empezar como al abrir
		}
		lead, fresh := full, first
		if first {
			lead, first = 0, false // al abrir se empieza justo en lo que suena ahora; el colchón se llena enseguida
		}
		ti, tat := h.target(room, now, lead)
		want := room.queue[ti]
		h.mu.Unlock()

		switch {
		case !fresh && src != nil && ti == idx && want.Path == path && tat-at > -streamSlip && tat-at < leadS+streamSlip:
			pump(tat - at) // la misma pista, un poco más adelante
		case !fresh && src != nil && ti == idx+1 && dur-at+tat < leadS+streamSlip:
			pump(dur - at + leadS) // lo que falte de la anterior, hasta que se acabe el archivo…
			if !gone && !open(want, ti, 0) {
				return nil
			}
			pump(tat) // …y el comienzo de la que sigue, por la misma conexión
		default: // primera vez, o alguien saltó o adelantó: se empieza donde toca
			if !open(want, ti, tat) {
				return nil // la pista ya no está: el reproductor reabre el flujo cuando la sala siga
			}
		}
		fl.Flush()
		select {
		case <-tick.C:
		case <-wake:
		case <-r.Context().Done():
			return nil
		}
	}
	return nil
}
