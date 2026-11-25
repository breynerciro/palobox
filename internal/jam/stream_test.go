package jam

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"palobox/internal/httpx"
	"palobox/internal/music"
)

// streamHub arma una sala de ana con la cola q sonando desde pos y le abre un flujo continuo. El audio es de
// mentira, a 1000 bytes por segundo; las rutas que empiezan con missing no están en disco y una pista sin duración en el
// índice dura 0,3 s; cada byte es la primera letra de su ruta. Devuelve la sala, las rutas que el flujo abrió, en orden,
// y lo que el teléfono ha recibido.
func streamHub(t *testing.T, h *Hub, q []music.Track, pos int, missing string) (*jamRoom, func() []string, func() []byte) {
	var mu sync.Mutex
	var opened []string
	h.Lead = 100 * time.Millisecond
	h.Audio = func(path string, dur, at float64) (io.ReadCloser, float64, error) {
		if missing != "" && strings.HasPrefix(path, missing) {
			return nil, 0, os.ErrNotExist
		}
		mu.Lock()
		opened = append(opened, path)
		mu.Unlock()
		if dur <= 0 {
			dur = 0.3
		}
		return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{path[0]}, int(max(dur-at, 0)*1000)))), 1000, nil
	}
	h.mu.Lock()
	u := h.get("ana")
	room := &jamRoom{code: "PRUEBA", queue: q, qrev: 1, members: []*user{u}}
	room.set(pos, 0, true)
	h.rooms[room.code], u.room = room, room
	h.mu.Unlock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Stream(w, r, httpx.User{Name: "ana"})
	}))
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "?device=anatelefono")
	var got bytes.Buffer
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := res.Body.Read(buf)
			mu.Lock()
			got.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return room, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(opened) },
		func() []byte { mu.Lock(); defer mu.Unlock(); return bytes.Clone(got.Bytes()) }
}

// waitRoom espera hasta 5 s a que ok (llamada con h.mu) se cumpla.
func waitRoom(t *testing.T, h *Hub, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		h.mu.Lock()
		done := ok()
		h.mu.Unlock()
		if done {
			return
		}
	}
	t.Fatal("la sala no llegó al estado esperado")
}

// Una pista que ya no está en disco se salta: el flujo sigue con la siguiente en vez de cerrarse.
func TestStreamSkipsMissing(t *testing.T) {
	h := &Hub{}
	room, opened, _ := streamHub(t, h, []music.Track{{Path: "borrada.mp3", Dur: 1}, {Path: "b.mp3", Dur: 5}}, 0, "borrada.mp3")
	waitRoom(t, h, func() bool { return room.pos == 1 && room.playing && room.streams == 1 })
	waitRoom(t, h, func() bool { return slices.Equal(opened(), []string{"b.mp3"}) })
}

// Con el disco de música desmontado ninguna pista abre: tras tres seguidas el flujo se cierra en vez de saltar sin
// fin por toda la cola (y las sugerencias) cargando el servidor y a los miembros.
func TestStreamStopsWhenDiskGone(t *testing.T) {
	h := &Hub{}
	q := []music.Track{{Path: "borrada1.mp3", Dur: 5}, {Path: "borrada2.mp3", Dur: 5}, {Path: "borrada3.mp3", Dur: 5}, {Path: "borrada4.mp3", Dur: 5}}
	room, opened, _ := streamHub(t, h, q, 0, "borrada")
	waitRoom(t, h, func() bool { return room.streams == 0 && room.pos == 2 })
	if got := opened(); len(got) != 0 {
		t.Fatalf("abrió %v", got)
	}
}

// Una pista sin duración en el índice avanza cuando su archivo se acaba, y se abre una sola vez.
func TestStreamNoDuration(t *testing.T) {
	h := &Hub{}
	room, opened, _ := streamHub(t, h, []music.Track{{Path: "a.mp3"}, {Path: "b.mp3", Dur: 5}}, 0, "")
	waitRoom(t, h, func() bool { return room.pos == 1 && room.playing && room.streams == 1 })
	waitRoom(t, h, func() bool { return slices.Equal(opened(), []string{"a.mp3", "b.mp3"}) })
}

// Con la cola llena (una lista de 500) se recortan las ya escuchadas para que sigan llegando sugerencias, y el
// flujo en curso no reabre la pista que estaba mandando.
func TestStreamTrimFullQueue(t *testing.T) {
	n := 0
	h := &Hub{More: func(k int, seed string) ([]music.Track, error) {
		var out []music.Track
		for range k {
			n++
			out = append(out, music.Track{Path: fmt.Sprintf("sug%d.mp3", n), Dur: 5})
		}
		return out, nil
	}}
	q := make([]music.Track, maxQueue)
	for i := range q {
		q[i] = music.Track{Path: fmt.Sprintf("%03d.mp3", i), Dur: 0.3}
	}
	room, opened, _ := streamHub(t, h, q, maxQueue-3, "")
	waitRoom(t, h, func() bool {
		return room.shift > 0 && room.streams == 1 && len(room.queue[room.pos].Path) > 3 && room.queue[room.pos].Path[:3] == "sug"
	})
	got := opened()
	if want := []string{"497.mp3", "498.mp3", "499.mp3", "sug1.mp3"}; !slices.Equal(got, want) {
		t.Fatalf("el flujo abrió %v; quería %v, sin reabrir ninguna", got, want)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(room.queue) > maxQueue || room.queue[0].Path != "447.mp3" || room.pos != 53 {
		t.Fatalf("recorte: pos %d, %d pistas, primera %s", room.pos, len(room.queue), room.queue[0].Path)
	}
}

// Un salto con la foto vieja ubica la pista en su puesto nuevo tras un recorte, o no hace nada si ya no está.
func TestFindAfterTrim(t *testing.T) {
	r := &jamRoom{queue: []music.Track{{Path: "a"}, {Path: "b"}, {Path: "c"}}}
	for _, c := range []struct {
		i    int
		path string
		want int
	}{{1, "b", 1}, {60, "c", 2}, {2, "b", 1}, {1, "x", -1}, {1, "", 1}, {9, "", -1}, {-1, "a", -1}} {
		if got := r.find(c.i, c.path); got != c.want {
			t.Errorf("find(%d, %q) = %d; quería %d", c.i, c.path, got, c.want)
		}
	}
}

// Tras «cut» el servidor dice en qué segundo del flujo empieza lo nuevo: lo ya mandado puede seguir en camino y
// el teléfono no lo distingue por lo que le llega.
func TestCutMark(t *testing.T) {
	h := &Hub{}
	room, _, got := streamHub(t, h, []music.Track{{Path: "a.mp3", Dur: 60}, {Path: "b.mp3", Dur: 60}}, 0, "")
	time.Sleep(300 * time.Millisecond) // que el flujo ya lleve algo de «a»
	h.mu.Lock()
	room.set(1, 0, true) // otro dispositivo saltó a «b»
	h.mu.Unlock()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/jam", strings.NewReader(`{"device":"anatelefono","op":"cut"}`))
	if err := h.Cmd(w, r, httpx.User{Name: "ana"}); err != nil {
		t.Fatal(err)
	}
	var v struct{ Cut *float64 }
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Cut == nil {
		t.Fatalf("«cut» no dijo dónde empieza lo nuevo: %s", w.Body)
	}
	var b []byte
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !bytes.Contains(b, []byte("b")); time.Sleep(20 * time.Millisecond) {
		b = got()
	}
	start := bytes.IndexByte(b, 'b')
	if start < 0 || bytes.Count(b[:start], []byte("a")) != start || math.Abs(float64(start)/1000-*v.Cut) > 0.001 {
		t.Fatalf("lo nuevo empieza en el byte %d (%.3f s) y «cut» dijo %.3f s", start, float64(start)/1000, *v.Cut)
	}
}

// cmdView manda un comando de ana desde su teléfono y devuelve la foto.
func cmdView(t *testing.T, h *Hub, body string) view {
	t.Helper()
	w := httptest.NewRecorder()
	if err := h.Cmd(w, httptest.NewRequest("POST", "/api/jam", strings.NewReader(body)), httpx.User{Name: "ana"}); err != nil {
		t.Fatal(err)
	}
	var v view
	json.Unmarshal(w.Body.Bytes(), &v)
	return v
}

// «state» solo devuelve la foto: el teléfono que pregunta en segundo plano no despierta a los demás dispositivos.
func TestStateDoesNotNotify(t *testing.T) {
	h := &Hub{Lookup: func(p []string) ([]music.Track, error) { return []music.Track{{Path: p[0], Dur: 60}}, nil }}
	other := &conn{id: "anaparlante", name: "parlante", wake: make(chan struct{}, 1)}
	h.mu.Lock()
	h.get("ana").conns[other.id] = other
	h.mu.Unlock()
	cmdView(t, h, `{"device":"anatelefono","op":"start","paths":["a.mp3"],"playing":true}`)
	<-other.wake
	if v := cmdView(t, h, `{"device":"anatelefono","op":"state"}`); v.Room == nil || !v.Room.Playing {
		t.Fatalf("«state» no devolvió la sala: %+v", v)
	}
	select {
	case <-other.wake:
		t.Fatal("«state» despertó a otro dispositivo")
	default:
	}
	cmdView(t, h, `{"device":"anatelefono","op":"pause"}`)
	select {
	case <-other.wake:
	default:
		t.Fatal("«pause» no avisó al otro dispositivo")
	}
}

// La foto dice el modo de repetir y cuáles pistas de la cola son sugerencias.
func TestViewRepeatAndAuto(t *testing.T) {
	h := &Hub{
		Lookup: func(p []string) ([]music.Track, error) { return []music.Track{{Path: p[0], Dur: 60}}, nil },
		More:   func(int, string) ([]music.Track, error) { return []music.Track{{Path: "sug.mp3", Dur: 60}}, nil },
	}
	if v := cmdView(t, h, `{"device":"anatelefono","op":"start","paths":["a.mp3"],"playing":true,"repeat":2}`); v.Room.Repeat != 2 || v.Room.Auto != nil {
		t.Fatalf("repeat %d, sugerencias %v; quería 2 y ninguna", v.Room.Repeat, v.Room.Auto)
	}
	cmdView(t, h, `{"device":"anatelefono","op":"repeat","repeat":0}`)
	v := cmdView(t, h, `{"device":"anatelefono","op":"next"}`)
	if v.Room.Repeat != 0 || v.Room.Queue == nil || len(*v.Room.Queue) != 2 || !slices.Equal(v.Room.Auto, []int{1}) {
		t.Fatalf("repeat %d, sugerencias %v; quería 0 y [1]", v.Room.Repeat, v.Room.Auto)
	}
}

// Con la sesión cerrada o la cuenta desactivada el flujo termina al cambiar de pista, sin abrir la siguiente.
func TestStreamEndsWhenSessionDies(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	h := &Hub{Alive: func(*http.Request) bool { mu.Lock(); defer mu.Unlock(); calls++; return calls == 1 }}
	room, opened, _ := streamHub(t, h, []music.Track{{Path: "a.mp3", Dur: 0.5}, {Path: "b.mp3", Dur: 5}}, 0, "")
	waitRoom(t, h, func() bool { return room.streams == 0 })
	if got := opened(); !slices.Equal(got, []string{"a.mp3"}) {
		t.Fatalf("abrió %v; quería solo a.mp3", got)
	}
}

// En un jam cualquiera elige dónde suena entre los dispositivos de todos: solo en uno, o sumando otro.
func TestJamPicksDevices(t *testing.T) {
	h := &Hub{Lookup: func(p []string) ([]music.Track, error) { return []music.Track{{Path: p[0], Dur: 60}}, nil }}
	cmd := func(user, body string) view {
		w := httptest.NewRecorder()
		if err := h.Cmd(w, httptest.NewRequest("POST", "/api/jam", strings.NewReader(body)), httpx.User{Name: user}); err != nil {
			t.Fatal(err)
		}
		var v view
		json.Unmarshal(w.Body.Bytes(), &v)
		return v
	}
	h.mu.Lock()
	for user, dev := range map[string]string{"ana": "anatelefono", "beto": "betoparlante"} {
		h.get(user).conns[dev] = &conn{id: dev, name: dev, wake: make(chan struct{}, 1)}
	}
	h.mu.Unlock()
	v := cmd("ana", `{"device":"anatelefono","op":"start","paths":["a.mp3"],"open":true}`)
	cmd("beto", `{"device":"betoparlante","op":"join","code":"`+v.Room.Code+`"}`)
	sounding := func(v view) (on []string) {
		for _, d := range v.Devices {
			if d.Active {
				on = append(on, d.User+"/"+d.ID)
			}
		}
		return
	}
	if got := sounding(cmd("ana", `{"device":"anatelefono","op":"here","target":"betoparlante","user":"beto"}`)); !slices.Equal(got, []string{"beto/betoparlante"}) {
		t.Fatalf("«solo en el parlante de beto» dejó sonando %v", got)
	}
	if got := sounding(cmd("ana", `{"device":"anatelefono","op":"here","target":"anatelefono","add":true}`)); !slices.Equal(got, []string{"/anatelefono", "beto/betoparlante"}) {
		t.Fatalf("sumar el teléfono de ana dejó sonando %v", got)
	}
}

// Un salto hecho desde otro dispositivo reinicia solo el flujo del teléfono: lo nuevo empieza en el punto que dice la
// foto (skip) y llega de golpe, sin que el teléfono mande «cut»; si lo manda después, recibe ese mismo punto.
func TestStreamRestartsOnJump(t *testing.T) {
	h := &Hub{}
	room, _, got := streamHub(t, h, []music.Track{{Path: "a.mp3", Dur: 60}, {Path: "b.mp3", Dur: 60}}, 0, "")
	time.Sleep(300 * time.Millisecond) // que el flujo ya lleve algo de «a»
	if v := cmdView(t, h, `{"device":"anatelefono","op":"state"}`); v.Skip != nil {
		t.Fatalf("sin saltos no hay reinicio que anunciar: %+v", v.Skip)
	}
	h.mu.Lock()
	room.set(1, 0, true) // otro dispositivo saltó a «b»
	h.mu.Unlock()
	var b []byte
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !bytes.Contains(b, []byte("b")); time.Sleep(20 * time.Millisecond) {
		b = got()
	}
	v := cmdView(t, h, `{"device":"anatelefono","op":"state"}`)
	start := bytes.IndexByte(b, 'b')
	if start < 0 || v.Skip == nil || math.Abs(float64(start)/1000-v.Skip.At) > 0.001 {
		t.Fatalf("lo nuevo empieza en el byte %d y la foto dice %+v", start, v.Skip)
	}
	if cut := cmdView(t, h, `{"device":"anatelefono","op":"cut"}`); cut.Cut == nil || *cut.Cut != v.Skip.At {
		t.Fatalf("«cut» tras el reinicio debía repetir %.3f s: %+v", v.Skip.At, cut.Cut)
	}
	time.Sleep(300 * time.Millisecond)
	if b = got(); bytes.Count(b[start:], []byte("b")) != len(b)-start {
		t.Fatal("«cut» volvió a reiniciar el flujo: después de «b» llegó otra cosa")
	}
}
