package jam

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"palobox/internal/httpx"
	"palobox/internal/music"
)

// streamHub arma una sala de ana con la cola q sonando desde pos y le abre un flujo continuo. El audio es de
// mentira, a 1000 bytes por segundo; las rutas de missing no están en disco y una pista sin duración en el
// índice dura 0,3 s. Devuelve la sala y las rutas que el flujo abrió, en orden.
func streamHub(t *testing.T, h *Hub, q []music.Track, pos int, missing string) (*jamRoom, func() []string) {
	var mu sync.Mutex
	var opened []string
	h.Lead = 100 * time.Millisecond
	h.Audio = func(path string, dur, at float64) (io.ReadCloser, float64, error) {
		if path == missing {
			return nil, 0, os.ErrNotExist
		}
		mu.Lock()
		opened = append(opened, path)
		mu.Unlock()
		if dur <= 0 {
			dur = 0.3
		}
		return io.NopCloser(bytes.NewReader(make([]byte, int(max(dur-at, 0)*1000)))), 1000, nil
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	go io.Copy(io.Discard, res.Body)
	return room, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(opened) }
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
	room, opened := streamHub(t, h, []music.Track{{Path: "borrada.mp3", Dur: 1}, {Path: "b.mp3", Dur: 5}}, 0, "borrada.mp3")
	waitRoom(t, h, func() bool { return room.pos == 1 && room.playing && room.streams == 1 })
	waitRoom(t, h, func() bool { return slices.Equal(opened(), []string{"b.mp3"}) })
}

// Una pista sin duración en el índice avanza cuando su archivo se acaba, y se abre una sola vez.
func TestStreamNoDuration(t *testing.T) {
	h := &Hub{}
	room, opened := streamHub(t, h, []music.Track{{Path: "a.mp3"}, {Path: "b.mp3", Dur: 5}}, 0, "")
	waitRoom(t, h, func() bool { return room.pos == 1 && room.playing && room.streams == 1 })
	waitRoom(t, h, func() bool { return slices.Equal(opened(), []string{"a.mp3", "b.mp3"}) })
}

// Con la cola llena (una lista de 500) se recortan las ya escuchadas para que sigan llegando sugerencias, y el
// flujo en curso no reabre la pista que estaba mandando.
func TestStreamTrimFullQueue(t *testing.T) {
	n := 0
	h := &Hub{More: func(k int, not string) ([]music.Track, error) {
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
	room, opened := streamHub(t, h, q, maxQueue-3, "")
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
