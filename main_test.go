package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type client struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (c client) do(method, path string, body []byte) (int, map[string]any, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if method == "PATCH" {
		req.Header.Set("X-Chunk-CRC32", fmt.Sprintf("%08x", crc32.ChecksumIEEE(body)))
	}
	res, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return res.StatusCode, m, raw
}

func (c client) login(name, pass string) int {
	code, _, _ := c.do("POST", "/api/login", []byte(`{"name":"`+name+`","pass":"`+pass+`"}`))
	return code
}

// upload sube data en trozos de 4 bytes (uploads.Chunk = 4 en el setup), del último al primero, simula un corte antes del
// primero y devuelve la respuesta final.
func (c client) upload(kind, dir, name string, data []byte, mtime int64) map[string]any {
	c.t.Helper()
	meta, _ := json.Marshal(map[string]any{"kind": kind, "dir": dir, "name": name, "size": len(data), "mtime": mtime})
	code, m, raw := c.do("POST", "/api/uploads", meta)
	if code != 200 {
		c.t.Fatalf("inicio de subida: %d %s", code, raw)
	}
	if m["done"] == true {
		return m
	}
	id := m["id"].(string)
	n := len(m["missing"].([]any))
	for i := n - 1; i >= 1; i-- {
		if code, m, raw = c.do("PATCH", fmt.Sprintf("/api/uploads/%s?chunk=%d", id, i), data[i*4:min(len(data), i*4+4)]); code != 200 || m["done"] != false {
			c.t.Fatalf("trozo %d: %d %s", i, code, raw)
		}
	}
	// Al "reconectar", el servidor dice que solo falta el primero.
	if _, m, raw = c.do("POST", "/api/uploads", meta); fmt.Sprint(m["missing"]) != "[0]" {
		c.t.Fatalf("reanudar: %s", raw)
	}
	code, m, raw = c.do("PATCH", "/api/uploads/"+id+"?chunk=0", data[:min(len(data), 4)])
	if code != 200 || m["done"] != true {
		c.t.Fatalf("último trozo: %d %s", code, raw)
	}
	return m
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestFlujo(t *testing.T) {
	data := t.TempDir()
	a, err := open(data)
	if err != nil {
		t.Fatal(err)
	}
	a.uploads.Chunk = 4 // lo que asume client.upload
	for _, n := range []string{"ana", "luis"} {
		if err := a.auth.AddUser(n, "clave-segura"); err != nil {
			t.Fatal(err)
		}
	}
	if a.auth.AddUser("../x", "clave-segura") == nil || a.auth.AddUser("ana", "corta") == nil || a.auth.AddUser("media", "clave-segura") == nil {
		t.Fatal("debe rechazar usuario o contraseña inválidos")
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	newClient := func() client {
		jar, _ := cookiejar.New(nil)
		return client{t, srv.URL, &http.Client{Jar: jar}}
	}
	ana, luis := newClient(), newClient()

	if code, _, _ := ana.do("GET", "/api/photos", nil); code != 401 {
		t.Fatalf("sin sesión debe dar 401, dio %d", code)
	}
	if ana.login("ana", "incorrecta") != 401 || ana.login("ana", "clave-segura") != 200 || luis.login("luis", "clave-segura") != 200 {
		t.Fatal("login")
	}

	// Panel de administrador: solo lo ve el usuario configurado, y registra quién usa la app.
	a.auth.Admin = "ana"
	if code, _, _ := luis.do("GET", "/api/admin", nil); code != 403 {
		t.Fatalf("un usuario normal no debe ver el panel, dio %d", code)
	}
	if _, m, _ := luis.do("GET", "/api/me", nil); m["admin"] != false {
		t.Fatalf("luis no es administrador: %v", m)
	}
	code, m, raw := ana.do("GET", "/api/admin", nil)
	us, _ := m["users"].([]any)
	if code != 200 || len(us) != 2 || m["diskTotal"].(float64) <= 0 {
		t.Fatalf("panel: %d %s", code, raw)
	}
	for _, x := range us {
		if x := x.(map[string]any); x["seen"].(float64) <= 0 || x["sessions"] != float64(1) {
			t.Fatalf("uso sin registrar: %v", x)
		}
	}

	// Foto: se guarda byte a byte en fotos/AAAA/MM según su fecha.
	foto := []byte("contenido-original-de-la-foto")
	mtime := time.Date(2026, 3, 9, 12, 0, 0, 0, time.Local)
	if m := ana.upload("photo", "", "IMG_1.heic", foto, mtime.UnixMilli()); m["dup"] == true {
		t.Fatal("la primera subida no es duplicado")
	}
	got, err := os.ReadFile(filepath.Join(data, "ana", "fotos", "2026", "03", "IMG_1.heic"))
	if err != nil || !bytes.Equal(got, foto) {
		t.Fatalf("el original no quedó idéntico: %v", err)
	}

	// Duplicados: mismo nombre y tamaño (antes de subir) o mismo contenido (al terminar).
	if m := ana.upload("photo", "", "IMG_1.heic", foto, 1); m["dup"] != true {
		t.Fatal("mismo nombre y tamaño debe ser duplicado")
	}
	if m := ana.upload("photo", "", "otra.heic", foto, 1); m["dup"] != true {
		t.Fatal("mismo contenido debe ser duplicado")
	}
	_, _, raw = ana.do("GET", "/api/photos", nil)
	var list []map[string]any
	json.Unmarshal(raw, &list)
	if len(list) != 1 {
		t.Fatalf("debe haber 1 foto, hay %d", len(list))
	}
	id := strings.TrimSuffix(string(mustJSON(list[0]["id"])), "\n")
	if _, _, raw := ana.do("GET", "/api/photos/"+id+"/o", nil); !bytes.Equal(raw, foto) {
		t.Fatal("la descarga del original no coincide")
	}

	// Aislamiento entre usuarios.
	if code, _, _ := luis.do("GET", "/api/photos/"+id+"/o", nil); code != 404 {
		t.Fatalf("otro usuario no debe ver la foto: %d", code)
	}
	if code, _, _ := luis.do("DELETE", "/api/photos/"+id, nil); code != 404 {
		t.Fatalf("otro usuario no debe borrar la foto: %d", code)
	}

	if code, _, _ := ana.do("POST", "/api/uploads", []byte(`{"kind":"photo","name":"virus.exe","size":5}`)); code != 400 {
		t.Fatalf("extensión no soportada debe dar 400: %d", code)
	}

	// Archivos: las rutas con ".." no salen de la carpeta del usuario, y nunca se sobrescribe.
	ana.upload("file", "../../luis/archivos", "nota.txt", []byte("hola"), 1)
	ana.upload("file", "../../luis/archivos", "nota.txt", []byte("otro"), 2)
	base := filepath.Join(data, "ana", "archivos", "luis", "archivos")
	if b, _ := os.ReadFile(filepath.Join(base, "nota.txt")); string(b) != "hola" {
		t.Fatal("el archivo debe quedar dentro de la carpeta de ana")
	}
	if b, _ := os.ReadFile(filepath.Join(base, "nota (1).txt")); string(b) != "otro" {
		t.Fatal("un nombre repetido no debe sobrescribir")
	}
	if _, err := os.Stat(filepath.Join(data, "luis", "archivos", "nota.txt")); err == nil {
		t.Fatal("escribió en la carpeta de otro usuario")
	}
	if _, _, raw := ana.do("GET", "/api/files/raw?path=luis/archivos/nota.txt", nil); string(raw) != "hola" {
		t.Fatal("descarga de archivo")
	}
	if code, _, _ := ana.do("GET", "/api/files/raw?path=../../.palobox/db.sqlite", nil); code != 404 {
		t.Fatalf("no debe poder leer fuera de su carpeta: %d", code)
	}
	if code, _, _ := ana.do("DELETE", "/api/files?path=..", nil); code != 400 {
		t.Fatalf("no debe poder borrar la raíz: %d", code)
	}

	// Música y películas: biblioteca compartida, de solo lectura y solo audio/video.
	disco := filepath.Join(data, "media", "musica", "Artista")
	os.MkdirAll(disco, 0o755)
	os.WriteFile(filepath.Join(disco, "cancion.mp3"), []byte("0123456789"), 0o644)
	os.WriteFile(filepath.Join(disco, "notas.txt"), []byte("privado"), 0o644)
	for _, c := range []client{ana, luis} { // la ven todos los usuarios
		_, _, raw := c.do("GET", "/api/music/album?path=Artista", nil)
		if s := string(raw); !strings.Contains(s, "cancion.mp3") || strings.Contains(s, "notas.txt") {
			t.Fatalf("la lista de música debe traer solo audio: %s", s)
		}
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/media/raw?lib=musica&path=Artista/cancion.mp3", nil)
	req.Header.Set("Range", "bytes=2-5")
	res, err := luis.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || string(part) != "2345" {
		t.Fatalf("debe atender rangos para poder adelantar: %d %q", res.StatusCode, part)
	}
	for _, q := range []string{
		"lib=musica&path=Artista/notas.txt",                         // no es audio
		"lib=musica&path=Artista",                                   // carpeta
		"lib=musica&path=../../ana/archivos/luis/archivos/nota.txt", // fuera de la biblioteca
		"lib=otra&path=x.mp3",                                       // biblioteca inexistente
	} {
		if code, _, _ := ana.do("GET", "/api/media/raw?"+q, nil); code != 404 {
			t.Fatalf("%s debe dar 404, dio %d", q, code)
		}
	}

	// Portadas: sin carátula ni cover.jpg responde 404 (y la interfaz deja el ícono); nunca sale de la biblioteca.
	for _, q := range []string{"lib=musica&path=Artista/cancion.mp3", "lib=musica&path=Artista/notas.txt", "lib=musica&path=../../ana"} {
		if code, _, _ := ana.do("GET", "/api/media/thumb?"+q, nil); code != 404 {
			t.Fatalf("portada %s debe dar 404, dio %d", q, code)
		}
	}

	// Borrar la foto elimina el archivo.
	if code, _, _ := ana.do("DELETE", "/api/photos/"+id, nil); code != 200 {
		t.Fatalf("borrar foto: %d", code)
	}
	if _, err := os.Stat(filepath.Join(data, "ana", "fotos", "2026", "03", "IMG_1.heic")); err == nil {
		t.Fatal("el archivo debía borrarse")
	}
	// SQLite puede reutilizar el id de la foto borrada: la huella de la URL debe cambiar igual.
	oldV := list[0]["v"]
	ana.upload("photo", "", "IMG_2.heic", []byte("otra foto, otro contenido"), 1)
	_, _, raw = ana.do("GET", "/api/photos", nil)
	list = nil
	json.Unmarshal(raw, &list)
	if len(list) != 1 || list[0]["v"] == "" || list[0]["v"] == oldV {
		t.Fatalf("la foto nueva debe tener otra huella: antes %v, ahora %s", oldV, raw)
	}

	ana.do("POST", "/api/logout", nil)
	if code, _, _ := ana.do("GET", "/api/me", nil); code != 401 {
		t.Fatalf("tras salir debe dar 401: %d", code)
	}
}

// La interfaz incrustada no tiene fecha: sin ETag el celular la descargaría completa en cada apertura.
func TestInterfazRevalida(t *testing.T) {
	a, err := open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	tag := res.Header.Get("ETag")
	if res.StatusCode != 200 || tag == "" {
		t.Fatalf("primera carga: %d, ETag %q", res.StatusCode, tag)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/app.js", nil)
	req.Header.Set("If-None-Match", tag)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 304 {
		t.Fatalf("con la misma huella debe responder 304, no %d", res.StatusCode)
	}
}
