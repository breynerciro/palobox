package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"palobox/internal/httpx"
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
	// Pasada la espera, A vuelve a poder entrar y el acierto olvida los fallos de esa cuenta.
	if c := login(s, "203.0.113.1:1000", "clave-segura"); c != 200 || len(s.tries["203.0.113.1"].fails) != 0 {
		t.Fatalf("A tras la espera: %d", c)
	}
	// Un acierto en la cuenta propia no borra los fallos contra otra: alternarlas no desarma el freno.
	if !s.begin("203.0.113.9") {
		t.Fatal("turno")
	}
	s.tries["203.0.113.9"].fails = map[string]int{"admin": 5}
	s.end("203.0.113.9", "ana", true)
	if f := s.tries["203.0.113.9"].fails; f["admin"] != 5 || s.tries["203.0.113.9"].busy {
		t.Fatalf("fallos contra admin tras entrar como ana: %v", f)
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
	r.Header.Set("CF-Connecting-IP", "9.9.9.9") // sin cloudflared en el despliegue, la inventa el cliente
	if ip := clientIP(r); ip != "5.6.7.8" {
		t.Fatalf("CF-Connecting-IP: %s", ip)
	}
}

func TestPenaltyCapped(t *testing.T) {
	for n, want := range map[int]time.Duration{3: time.Second, 5: 4 * time.Second, 8: 30 * time.Second, 37: 30 * time.Second, 100: 30 * time.Second} {
		if got := penalty(n); got != want {
			t.Errorf("penalty(%d) = %v, quería %v", n, got, want)
		}
	}
}

// Un fallo de la base no es "sesión inválida": con 401 el celular borraría su música.
func TestWrapDBErrorIsNot401(t *testing.T) {
	s := newService(t)
	h := s.Wrap(func(w http.ResponseWriter, r *http.Request, u httpx.User) error { return nil })
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "s", Value: "x"})
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != 401 {
		t.Fatalf("sesión desconocida: %d, quería 401", w.Code)
	}
	s.DB.Close()
	w = httptest.NewRecorder()
	h(w, r)
	if w.Code != 500 {
		t.Fatalf("base cerrada: %d, quería 500", w.Code)
	}
}

// Alive deja de valer al desactivar la cuenta o cerrar la sesión: con eso los flujos de jam ya abiertos terminan.
func TestAlive(t *testing.T) {
	s := newService(t)
	w := httptest.NewRecorder()
	s.Login(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"name":"ana","pass":"clave-segura"}`)))
	r := httptest.NewRequest("GET", "/api/jam/stream", nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	if !s.Alive(r) {
		t.Fatal("sesión recién abierta: no vigente")
	}
	if s.Alive(httptest.NewRequest("GET", "/", nil)) {
		t.Fatal("sin cookie: vigente")
	}
	s.DB.Exec(`UPDATE users SET disabled = 1`)
	if s.Alive(r) {
		t.Fatal("cuenta desactivada: sigue vigente")
	}
	s.DB.Exec(`UPDATE users SET disabled = 0`)
	s.DB.Exec(`DELETE FROM sessions`)
	if s.Alive(r) {
		t.Fatal("sesión cerrada: sigue vigente")
	}
}

// Si poner la clave falla, la invitación no queda gastada.
func TestAcceptAtomic(t *testing.T) {
	s := newService(t)
	tok, _, err := s.Invite(1)
	if err != nil {
		t.Fatal(err)
	}
	accept := func(pass string) int {
		r := httptest.NewRequest("POST", "/api/invite", strings.NewReader(`{"token":"`+tok+`","pass":"`+pass+`"}`))
		w := httptest.NewRecorder()
		s.Accept(w, r)
		return w.Code
	}
	if c := accept("corta"); c != 400 {
		t.Fatalf("clave corta: %d", c)
	}
	s.DB.Exec(`CREATE TRIGGER falla BEFORE UPDATE OF pass ON users BEGIN SELECT RAISE(ABORT, 'falla'); END`)
	if c := accept("clave-nueva"); c != 500 {
		t.Fatalf("con UPDATE fallando: %d", c)
	}
	s.DB.Exec(`DROP TRIGGER falla`)
	if c := accept("clave-nueva"); c != 200 {
		t.Fatalf("el enlace quedó gastado: %d", c)
	}
}
