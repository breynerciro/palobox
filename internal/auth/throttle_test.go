package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"palobox/internal/store"
)

func newService(t *testing.T) *Service {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Service{DB: db}
	if err := s.AddUser("ana", "clave-segura"); err != nil {
		t.Fatal(err)
	}
	return s
}

// login manda un intento desde remote (la conexión) y devuelve el código.
func login(s *Service, remote, pass string) int {
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"name":"ana","pass":"`+pass+`"}`))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	s.Login(w, r)
	return w.Code
}

func TestLoginThrottlePerIP(t *testing.T) {
	s := newService(t)
	bad := make(chan int)
	go func() { bad <- login(s, "203.0.113.1:1000", "mala-clave") }()
	time.Sleep(300 * time.Millisecond) // el fallo de A ya está pagando su espera

	// (b) la misma IP no puede probar otra clave mientras tanto.
	if c := login(s, "203.0.113.1:1001", "otra-mala"); c != http.StatusTooManyRequests {
		t.Fatalf("misma IP durante la espera: %d, quería 429", c)
	}
	// (a) otra IP entra sin esperar a A.
	start := time.Now()
	if c := login(s, "203.0.113.2:1000", "clave-segura"); c != 200 {
		t.Fatalf("otra IP: %d", c)
	}
	if d := time.Since(start); d > 600*time.Millisecond {
		t.Fatalf("la otra IP esperó %v", d)
	}
	if c := <-bad; c != 401 {
		t.Fatalf("clave mala: %d", c)
	}
	// Pasada la espera, A vuelve a poder entrar y el acierto olvida sus fallos.
	if c := login(s, "203.0.113.1:1000", "clave-segura"); c != 200 || s.tries["203.0.113.1"] != nil {
		t.Fatalf("A tras la espera: %d", c)
	}
}

func TestClientIPTrustsProxyOnlyFromLoopback(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	r.RemoteAddr = "203.0.113.9:4000" // (c) conexión directa: la cabecera la inventó el cliente
	if ip := clientIP(r); ip != "203.0.113.9" {
		t.Fatalf("no loopback: %s", ip)
	}
	r.RemoteAddr = "127.0.0.1:4000" // desde el proxy: vale el último, el que él agregó
	if ip := clientIP(r); ip != "5.6.7.8" {
		t.Fatalf("loopback: %s", ip)
	}
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")
	if ip := clientIP(r); ip != "9.9.9.9" {
		t.Fatalf("cloudflared: %s", ip)
	}
}
