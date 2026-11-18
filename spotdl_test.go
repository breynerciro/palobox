package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"palobox/internal/module"
	"palobox/internal/spotdl"
)

const testToken = "token-de-prueba"

// fakeSpotdl imita al servicio: cola en memoria y registro de lo que recibe.
type fakeSpotdl struct {
	sync.Mutex
	jobs    []spotdl.Job
	deleted []string
	posted  []string
	retried []string
	lastGet string
	users   []string // X-PaloBox-User recibido en cada petición
}

func (f *fakeSpotdl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Lock()
	defer f.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, `{"error":"no autorizado"}`, 401)
		return
	}
	f.users = append(f.users, r.Header.Get("X-PaloBox-User")+"/"+r.Header.Get("X-PaloBox-Admin"))
	switch {
	case r.Method == "GET" && r.URL.Path == "/jobs":
		f.lastGet = r.URL.Query().Encode()
		json.NewEncoder(w).Encode(f.jobs)
	case r.Method == "POST" && r.URL.Path == "/jobs":
		var in struct{ URL string }
		json.NewDecoder(r.Body).Decode(&in)
		f.posted = append(f.posted, in.URL)
		job := spotdl.Job{ID: "0123456789abcdef", URL: in.URL, Status: "queued", Created: 1, Owner: r.Header.Get("X-PaloBox-User")}
		f.jobs = append(f.jobs, job)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(job)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/retry"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/jobs/"), "/retry")
		f.retried = append(f.retried, id)
		for i, j := range f.jobs {
			if j.ID != id {
				continue
			}
			if j.Owner != r.Header.Get("X-PaloBox-User") && r.Header.Get("X-PaloBox-Admin") != "1" {
				w.WriteHeader(403)
				json.NewEncoder(w).Encode(map[string]string{"error": "Solo quien pidió esta descarga puede reintentarla."})
				return
			}
			f.jobs[i].Status, f.jobs[i].Attempts = "queued", j.Attempts+1
			json.NewEncoder(w).Encode(f.jobs[i])
			return
		}
		http.Error(w, `{"error":"no encontrado"}`, 404)
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/jobs/"):
		id := strings.TrimPrefix(r.URL.Path, "/jobs/")
		f.deleted = append(f.deleted, id)
		var keep []spotdl.Job
		for _, j := range f.jobs {
			if j.ID != id {
				keep = append(keep, j)
			}
		}
		f.jobs = keep
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	default:
		http.Error(w, "no existe", 404)
	}
}

func (f *fakeSpotdl) estado() (posted, deleted []string) {
	f.Lock()
	defer f.Unlock()
	return append([]string(nil), f.posted...), append([]string(nil), f.deleted...)
}

func newSpotdlTest(t *testing.T) (*fakeSpotdl, client, *app) {
	t.Helper()
	data := t.TempDir()
	a, err := open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.auth.AddUser("ana", "clave-segura"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSpotdl{}
	side := httptest.NewServer(fake)
	t.Cleanup(side.Close)
	a.spotdl.Module = module.Module{Label: "El servicio de descargas de Spotify", URL: side.URL, Token: testToken}

	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	c := client{t, srv.URL, &http.Client{Jar: jar}}
	if c.login("ana", "clave-segura") != 200 {
		t.Fatal("login")
	}
	return fake, c, a
}

func TestSpotdlProxy(t *testing.T) {
	fake, ana, a := newSpotdlTest(t)

	// Lista vacía pero habilitada.
	code, m, raw := ana.do("GET", "/api/spotdl", nil)
	if code != 200 || m["enabled"] != true || len(m["items"].([]any)) != 0 {
		t.Fatalf("lista inicial: %d %s", code, raw)
	}

	// Un enlace inválido no llega al servicio.
	if code, _, _ := ana.do("POST", "/api/spotdl", []byte(`{"url":"https://evil.example/track/4uLU6hMCjMI75M1A2tKUQC"}`)); code != 400 {
		t.Fatalf("enlace inválido: %d", code)
	}
	if code, _, _ := ana.do("POST", "/api/spotdl", []byte(`{}`)); code != 400 {
		t.Fatalf("sin url: %d", code)
	}
	if posted, _ := fake.estado(); len(posted) != 0 {
		t.Fatalf("el servicio no debía recibir nada: %v", posted)
	}

	// Alta: la app manda el enlace ya canónico.
	if code, m, raw := ana.do("POST", "/api/spotdl", []byte(`{"url":"https://open.spotify.com/intl-es/playlist/37i9dQZF1DXcBWIGoYBM5M?si=x"}`)); code != 200 || m["status"] != "queued" {
		t.Fatalf("alta: %d %s", code, raw)
	}
	posted, _ := fake.estado()
	if len(posted) != 1 || posted[0] != "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M" {
		t.Fatalf("el servicio recibió %v", posted)
	}

	// La lista se reenvía con los campos del contrato.
	code, m, raw = ana.do("GET", "/api/spotdl", nil)
	items := m["items"].([]any)
	job := items[0].(map[string]any)
	for _, k := range []string{"id", "url", "title", "status", "current", "total", "done", "progress", "error", "created"} {
		if _, ok := job[k]; !ok {
			t.Fatalf("falta el campo %q en %s", k, raw)
		}
	}
	if code != 200 || job["progress"] != nil {
		t.Fatalf("progreso desconocido debe ser null: %d %s", code, raw)
	}

	// Borrar: id válido al servicio, id inválido rechazado sin llamarlo.
	if code, _, _ := ana.do("DELETE", "/api/spotdl/no-es-id", nil); code != 400 {
		t.Fatalf("id inválido: %d", code)
	}
	if code, _, _ := ana.do("DELETE", "/api/spotdl/0123456789abcdef", nil); code != 200 {
		t.Fatalf("borrar: %d", code)
	}
	if _, deleted := fake.estado(); len(deleted) != 1 || deleted[0] != "0123456789abcdef" {
		t.Fatalf("el servicio debía borrar el trabajo: %v", deleted)
	}
	fake.Lock()
	for _, u := range fake.users {
		if u != "ana/" {
			t.Errorf("el servicio debía recibir a ana sin admin, recibió %q", u)
		}
	}
	fake.Unlock()

	// Servicio caído: error 503 con mensaje entendible, no un 500 genérico.
	a.spotdl.Module.URL = "http://127.0.0.1:1"
	if code, _, raw := ana.do("GET", "/api/spotdl", nil); code != 503 || !strings.Contains(string(raw), "no responde") {
		t.Fatalf("servicio caído: %d %s", code, raw)
	}
}

func TestSpotdlErroresDelServicio(t *testing.T) {
	// El servicio responde 429 con su propio mensaje y la app lo conserva.
	side := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		json.NewEncoder(w).Encode(map[string]string{"error": "Hay demasiadas descargas en cola."})
	}))
	defer side.Close()
	data := t.TempDir()
	a, err := open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.auth.AddUser("ana", "clave-segura"); err != nil {
		t.Fatal(err)
	}
	a.spotdl.Module = module.Module{Label: "El servicio de descargas de Spotify", URL: side.URL, Token: testToken}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := client{t, srv.URL, &http.Client{Jar: jar}}
	c.login("ana", "clave-segura")
	code, _, raw := c.do("POST", "/api/spotdl", []byte(`{"url":"https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"}`))
	if code != 429 || !strings.Contains(string(raw), "demasiadas") {
		t.Fatalf("429 del servicio: %d %s", code, raw)
	}
}

func TestSpotdlDeshabilitado(t *testing.T) {
	data := t.TempDir()
	a, err := open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.auth.AddUser("ana", "clave-segura"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	ana := client{t, srv.URL, &http.Client{Jar: jar}}
	ana.login("ana", "clave-segura")

	code, m, raw := ana.do("GET", "/api/spotdl", nil)
	if code != 200 || m["enabled"] != false || len(m["items"].([]any)) != 0 {
		t.Fatalf("sin servicio debe estar deshabilitado: %d %s", code, raw)
	}
	if code, _, _ := ana.do("POST", "/api/spotdl", []byte(`{"url":"https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"}`)); code != 503 {
		t.Fatalf("alta sin servicio: %d", code)
	}
	if code, _, _ := ana.do("DELETE", "/api/spotdl/0123456789abcdef", nil); code != 503 {
		t.Fatalf("borrar sin servicio: %d", code)
	}
}

// La app marca como propios solo los trabajos del usuario (o todos, para el admin).
func TestSpotdlDueno(t *testing.T) {
	fake, ana, a := newSpotdlTest(t)
	fake.jobs = []spotdl.Job{{ID: "0123456789abcdef", Owner: "ana", Status: "queued"}, {ID: "fedcba9876543210", Owner: "pepe", Status: "done"}}
	_, m, raw := ana.do("GET", "/api/spotdl", nil)
	items := m["items"].([]any)
	if items[0].(map[string]any)["mine"] != true || items[1].(map[string]any)["mine"] != false {
		t.Fatalf("mine mal calculado para ana: %s", raw)
	}
	a.auth.Admin = "ana"
	_, m, raw = ana.do("GET", "/api/spotdl", nil)
	if m["items"].([]any)[1].(map[string]any)["mine"] != true {
		t.Fatalf("el admin puede quitar cualquiera: %s", raw)
	}
	fake.Lock()
	last := fake.users[len(fake.users)-1]
	fake.Unlock()
	if last != "ana/1" {
		t.Fatalf("el servicio debía saber que es admin: %q", last)
	}
}

// Reintentar pasa por la app con la identidad del usuario; el servicio decide si es suyo.
func TestSpotdlRetry(t *testing.T) {
	fake, ana, a := newSpotdlTest(t)
	fake.jobs = []spotdl.Job{
		{ID: "0123456789abcdef", Owner: "ana", Status: "error", Attempts: 2, Error: "22 canciones no se pudieron descargar"},
		{ID: "fedcba9876543210", Owner: "pepe", Status: "error", Attempts: 1},
	}
	if code, _, _ := ana.do("POST", "/api/spotdl/no-es-id/retry", nil); code != 400 {
		t.Fatalf("id inválido: %d", code)
	}
	code, m, raw := ana.do("POST", "/api/spotdl/0123456789abcdef/retry", nil)
	if code != 200 || m["status"] != "queued" || m["attempts"] != float64(3) || m["mine"] != true {
		t.Fatalf("reintento: %d %s", code, raw)
	}
	if code, _, raw := ana.do("POST", "/api/spotdl/fedcba9876543210/retry", nil); code != 403 || !strings.Contains(string(raw), "Solo quien") {
		t.Fatalf("reintento ajeno: %d %s", code, raw)
	}
	a.auth.Admin = "ana"
	if code, _, raw := ana.do("POST", "/api/spotdl/fedcba9876543210/retry", nil); code != 200 {
		t.Fatalf("el admin puede reintentar cualquiera: %d %s", code, raw)
	}
	fake.Lock()
	defer fake.Unlock()
	if strings.Join(fake.retried, ",") != "0123456789abcdef,fedcba9876543210,fedcba9876543210" {
		t.Fatalf("el servicio recibió %v", fake.retried)
	}
}

// Un token equivocado no se confunde con una sesión cerrada (401 haría salir al usuario).
func TestSpotdlTokenRechazado(t *testing.T) {
	_, ana, a := newSpotdlTest(t)
	a.spotdl.Module.Token = "otro"
	if code, _, raw := ana.do("GET", "/api/spotdl", nil); code != 503 || !strings.Contains(string(raw), "no acepta") {
		t.Fatalf("token rechazado: %d %s", code, raw)
	}
}

// CSRF: una petición que cambia datos desde otro sitio se rechaza; desde la propia app pasa.
func TestCSRF(t *testing.T) {
	_, ana, _ := newSpotdlTest(t)
	body := `{"url":"https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"}`
	enviar := func(method string, h map[string]string) int {
		req, _ := http.NewRequest(method, ana.base+"/api/spotdl", strings.NewReader(body))
		for k, v := range h {
			req.Header.Set(k, v)
		}
		res, err := ana.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := enviar("POST", map[string]string{"Sec-Fetch-Site": "cross-site"}); code != 403 {
		t.Fatalf("POST de otro sitio: %d", code)
	}
	if code := enviar("POST", map[string]string{"Origin": "https://malo.example"}); code != 403 {
		t.Fatalf("POST con Origin ajeno: %d", code)
	}
	if code := enviar("POST", map[string]string{"Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Fatalf("POST de la propia app: %d", code)
	}
	if code := enviar("GET", map[string]string{"Sec-Fetch-Site": "cross-site"}); code != 200 {
		t.Fatalf("GET no cambia datos y debe pasar: %d", code)
	}
}
