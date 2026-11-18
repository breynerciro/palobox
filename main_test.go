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
	"sync"
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

// upload sube data en trozos de 4 bytes, del último al primero, simula un corte antes del
// primero y devuelve la respuesta final.
func (c client) upload(kind, dir, name string, data []byte, mtime int64) map[string]any {
	c.t.Helper()
	defer func(old int64) { upChunk = old }(upChunk)
	upChunk = 4
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
	for _, n := range []string{"ana", "luis"} {
		if err := a.addUser(n, "clave-segura"); err != nil {
			t.Fatal(err)
		}
	}
	if a.addUser("../x", "clave-segura") == nil || a.addUser("ana", "corta") == nil || a.addUser("media", "clave-segura") == nil {
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
	a.admin = "ana"
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

func TestNatLess(t *testing.T) {
	for _, c := range [][2]string{{"season 2", "season 10"}, {"s01e09", "s01e10"}, {"s01e99", "s01e100"}, {"a", "b"}, {"a", "a1"}, {"2", "a"}, {"e02", "e3"}} {
		if !natLess(c[0], c[1]) || natLess(c[1], c[0]) {
			t.Fatalf("%q debe ir antes que %q", c[0], c[1])
		}
	}
	if natLess("x", "x") {
		t.Fatal("un nombre no va antes que sí mismo")
	}
}

func TestSafe(t *testing.T) {
	for rel, want := range map[string]string{"": "/b", "a/c": "/b/a/c", "../../etc": "/b/etc", "/etc/passwd": "/b/etc/passwd", "a/../..": "/b"} {
		if got := safe("/b", rel); got != want {
			t.Errorf("safe(%q) = %q, quiero %q", rel, got, want)
		}
	}
}

func TestSubidas(t *testing.T) {
	data := t.TempDir()
	a, err := open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.addUser("ana", "clave-segura"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	ana := client{t, srv.URL, &http.Client{Jar: jar}}
	if ana.login("ana", "clave-segura") != 200 {
		t.Fatal("login")
	}

	// Tope de tamaño: lo declara el cliente, así que se rechaza antes de recibir nada.
	meta := mustJSON(map[string]any{"kind": "file", "name": "enorme.bin", "size": int64(maxUpload) + 1})
	if code, _, raw := ana.do("POST", "/api/uploads", meta); code != 413 {
		t.Fatalf("por encima del tope debe dar 413: %d %s", code, raw)
	}
	if code, _, _ := ana.do("POST", "/api/uploads", []byte(`{"kind":"file","name":"x.bin","size":-1}`)); code != 400 {
		t.Fatalf("tamaño negativo debe dar 400: %d", code)
	}

	// Trozos en paralelo y en desorden, uno de ellos dos veces a la vez: el archivo queda idéntico.
	defer func(old int64) { upChunk = old }(upChunk)
	upChunk = 1 << 20
	body := append(bytes.Repeat([]byte("0123456789abcdef"), 1<<18), "fin"...) // 4 MiB + 3 bytes: 5 trozos
	_, m, _ := ana.do("POST", "/api/uploads", mustJSON(map[string]any{"kind": "file", "name": "doble.bin", "size": len(body)}))
	id := m["id"].(string)
	if len(m["missing"].([]any)) != 5 {
		t.Fatalf("deben faltar 5 trozos: %v", m)
	}
	if code, _, _ := ana.do("PATCH", "/api/uploads/"+id+"?chunk=5", []byte("x")); code != 400 {
		t.Fatalf("un trozo fuera de rango debe dar 400: %d", code)
	}
	// Un trozo con huella que no coincide o sin huella se rechaza y no cuenta como recibido.
	for _, crc := range []string{"00000000", ""} {
		req, _ := http.NewRequest("PATCH", srv.URL+"/api/uploads/"+id+"?chunk=4", bytes.NewReader([]byte("fin")))
		req.Header.Set("X-Chunk-CRC32", crc)
		if res, err := ana.c.Do(req); err != nil || res.StatusCode != 400 {
			t.Fatalf("huella %q debe dar 400: %v %v", crc, res, err)
		}
	}
	if _, m, _ := ana.do("POST", "/api/uploads", mustJSON(map[string]any{"kind": "file", "name": "doble.bin", "size": len(body)})); len(m["missing"].([]any)) != 5 {
		t.Fatalf("el trozo dañado no debe contar: %v", m)
	}
	var wg sync.WaitGroup
	var dones sync.Map
	for _, i := range []int{4, 2, 0, 3, 1, 2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, m, raw := ana.do("PATCH", fmt.Sprintf("/api/uploads/%s?chunk=%d", id, i), body[i<<20:min(len(body), (i+1)<<20)])
			if code != 200 {
				t.Errorf("trozo %d: %d %s", i, code, raw)
			}
			if m["done"] == true && m["dup"] == false {
				dones.Store(i, true)
			}
		}()
	}
	wg.Wait()
	n := 0
	dones.Range(func(_, _ any) bool { n++; return true })
	if n != 1 {
		t.Fatalf("exactamente un trozo debe cerrar la subida: %d", n)
	}
	if got, err := os.ReadFile(filepath.Join(data, "ana", "archivos", "doble.bin")); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("el archivo final no quedó idéntico: %v", err)
	}
	n = 0
	a.upMu.Range(func(_, _ any) bool { n++; return true })
	if n != 0 {
		t.Fatalf("quedaron %d candados de subida sin borrar", n)
	}

	// Un tramo que en disco ya no coincide con su huella (bug o disco con fallas) se detecta al
	// releer antes de cerrar: no se cierra, se vuelve a pedir y el archivo final queda idéntico.
	meta = mustJSON(map[string]any{"kind": "file", "name": "releido.bin", "size": len(body)})
	_, m, _ = ana.do("POST", "/api/uploads", meta)
	id = m["id"].(string)
	for _, i := range []int{0, 1, 2, 3} {
		if code, m, raw := ana.do("PATCH", fmt.Sprintf("/api/uploads/%s?chunk=%d", id, i), body[i<<20:(i+1)<<20]); code != 200 || m["done"] != false {
			t.Fatalf("trozo %d: %d %s", i, code, raw)
		}
	}
	// Un reenvío de un trozo ya aceptado, aunque traiga otros bytes con su huella, no lo pisa.
	if code, m, raw := ana.do("PATCH", fmt.Sprintf("/api/uploads/%s?chunk=0", id), bytes.Repeat([]byte("Z"), 1<<20)); code != 200 || m["done"] != false {
		t.Fatalf("reenvío de trozo aceptado: %d %s", code, raw)
	}
	f, _ := os.OpenFile(a.tmp(id+".part"), os.O_WRONLY, 0)
	f.WriteAt([]byte("X"), 3<<20+7)
	f.Close()
	if code, m, raw := ana.do("PATCH", fmt.Sprintf("/api/uploads/%s?chunk=4", id), body[4<<20:]); code != 200 || m["done"] != false {
		t.Fatalf("con un tramo dañado no debe cerrar: %d %s", code, raw)
	}
	if _, _, raw := ana.do("POST", "/api/uploads", meta); !strings.Contains(string(raw), `"missing":[3]`) {
		t.Fatalf("solo debe faltar el tramo dañado: %s", raw)
	}
	if code, m, raw := ana.do("PATCH", fmt.Sprintf("/api/uploads/%s?chunk=3", id), body[3<<20:4<<20]); code != 200 || m["done"] != true {
		t.Fatalf("al reenviarlo debe cerrar: %d %s", code, raw)
	}
	if got, err := os.ReadFile(filepath.Join(data, "ana", "archivos", "releido.bin")); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("el archivo final no quedó idéntico: %v", err)
	}

	// La misma foto subida dos veces a la vez: una queda, la otra es duplicado, sin archivo huérfano.
	foto := []byte("la-misma-foto-desde-dos-dispositivos")
	dups := make([]any, 2)
	for i := range dups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, m, _ := ana.do("POST", "/api/uploads", mustJSON(map[string]any{"kind": "photo", "name": "IMG_9.jpg", "size": len(foto), "mtime": 1741536000000 + i}))
			if id, ok := m["id"].(string); ok {
				_, m, _ = ana.do("PATCH", "/api/uploads/"+id+"?chunk=0", foto)
			}
			dups[i] = m["dup"]
		}()
	}
	wg.Wait()
	if !(dups[0] == false && dups[1] == true) && !(dups[0] == true && dups[1] == false) {
		t.Fatalf("una debe guardarse y la otra ser duplicado: %v", dups)
	}
	var files []string
	filepath.WalkDir(filepath.Join(data, "ana", "fotos"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 1 {
		t.Fatalf("debe quedar un solo archivo: %v", files)
	}

	// Fotos distintas en paralelo (como sube la interfaz): todas quedan, ninguna es duplicado.
	const nf = 6
	for i := range nf {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf("foto-distinta-%d", i))
			_, m, _ := ana.do("POST", "/api/uploads", mustJSON(map[string]any{"kind": "photo", "name": fmt.Sprintf("IMG_2%d.jpg", i), "size": len(body), "mtime": 1741536000000}))
			if code, m, raw := ana.do("PATCH", "/api/uploads/"+m["id"].(string)+"?chunk=0", body); code != 200 || m["done"] != true || m["dup"] != false {
				t.Errorf("foto %d en paralelo: %d %s", i, code, raw)
			}
		}()
	}
	wg.Wait()
	var nmedia int
	a.db.QueryRow(`SELECT COUNT(*) FROM media WHERE name LIKE 'IMG_2%'`).Scan(&nmedia)
	if nmedia != nf {
		t.Fatalf("deben quedar %d fotos en el índice: hay %d", nf, nmedia)
	}

	// Archivos distintos con el mismo nombre, a la vez y a la misma carpeta: ninguno pisa a otro.
	const n8 = 8
	for i := range n8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := bytes.Repeat([]byte{'a' + byte(i)}, 1000)
			_, m, _ := ana.do("POST", "/api/uploads", mustJSON(map[string]any{"kind": "file", "dir": "juntos", "name": "igual.txt", "size": len(body), "mtime": i}))
			if code, m, raw := ana.do("PATCH", "/api/uploads/"+m["id"].(string)+"?chunk=0", body); code != 200 || m["done"] != true {
				t.Errorf("subida %d: %d %s", i, code, raw)
			}
		}()
	}
	wg.Wait()
	es, _ := os.ReadDir(filepath.Join(data, "ana", "archivos", "juntos"))
	seen := map[byte]bool{}
	for _, e := range es {
		b, _ := os.ReadFile(filepath.Join(data, "ana", "archivos", "juntos", e.Name()))
		if len(b) != 1000 || !bytes.Equal(b, bytes.Repeat(b[:1], 1000)) {
			t.Fatalf("%s quedó dañado (%d bytes)", e.Name(), len(b))
		}
		seen[b[0]] = true
	}
	if len(es) != n8 || len(seen) != n8 {
		t.Fatalf("deben quedar %d archivos distintos: hay %d con %d contenidos", n8, len(es), len(seen))
	}

	// Un POST que llega mientras el último PATCH cierra la subida espera y no le borra el .part.
	// finMu detiene al PATCH dentro de finalize, con el .part ya completo.
	foto = []byte("foto-que-se-esta-cerrando")
	meta = mustJSON(map[string]any{"kind": "photo", "name": "IMG_10.jpg", "size": len(foto), "mtime": 1741536000000})
	_, m, _ = ana.do("POST", "/api/uploads", meta)
	id = m["id"].(string)
	a.finMu.Lock()
	var patch, post int
	var postM map[string]any
	wg.Add(2)
	go func() {
		defer wg.Done()
		patch, _, _ = ana.do("PATCH", "/api/uploads/"+id+"?chunk=0", foto)
	}()
	for i := 0; i < 200; i++ { // hasta que el PATCH haya escrito todo; luego un margen para que entre a finalize
		if st, err := os.Stat(a.tmp(id + ".part")); err == nil && st.Size() == int64(len(foto)) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	go func() {
		defer wg.Done()
		post, postM, _ = ana.do("POST", "/api/uploads", meta)
	}()
	time.Sleep(100 * time.Millisecond)
	a.finMu.Unlock()
	wg.Wait()
	if patch != 200 || post != 200 || postM["dup"] != true {
		t.Fatalf("el POST debe esperar y ver la subida ya terminada: PATCH %d, POST %d %v", patch, post, postM)
	}
	if _, err := os.Stat(filepath.Join(data, "ana", "fotos", "2025", "03", "IMG_10.jpg")); err != nil {
		t.Fatalf("la foto debía quedar guardada: %v", err)
	}
	if _, err := os.Stat(a.tmp(id + ".json")); err == nil {
		t.Fatal("el POST recreó una subida ya terminada")
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
