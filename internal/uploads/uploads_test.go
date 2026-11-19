package uploads

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"palobox/internal/httpx"
	"palobox/internal/media"
	"palobox/internal/store"
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

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestSubidas(t *testing.T) {
	data := t.TempDir()
	db, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, data, media.Thumbs{Dir: filepath.Join(data, ".palobox", "thumbs"), Sem: make(chan struct{}, 2)})
	// Usuario fijo: la autenticación se prueba en la raíz, aquí solo interesa la subida.
	wrap := func(fn httpx.Handler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := fn(w, r, httpx.User{ID: 1, Name: "ana"}); err != nil {
				httpx.Fail(w, err)
			}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/uploads", wrap(h.Start))
	mux.HandleFunc("PATCH /api/uploads/{id}", wrap(h.Part))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ana := client{t, srv.URL, srv.Client()}

	// Tope de tamaño: lo declara el cliente, así que se rechaza antes de recibir nada.
	meta := mustJSON(map[string]any{"kind": "file", "name": "enorme.bin", "size": int64(maxUpload) + 1})
	if code, _, raw := ana.do("POST", "/api/uploads", meta); code != 413 {
		t.Fatalf("por encima del tope debe dar 413: %d %s", code, raw)
	}
	if code, _, _ := ana.do("POST", "/api/uploads", []byte(`{"kind":"file","name":"x.bin","size":-1}`)); code != 400 {
		t.Fatalf("tamaño negativo debe dar 400: %d", code)
	}

	// Trozos en paralelo y en desorden, uno de ellos dos veces a la vez: el archivo queda idéntico.
	h.Chunk = 1 << 20
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
	h.upMu.Range(func(_, _ any) bool { n++; return true })
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
	f, _ := os.OpenFile(h.tmp(id+".part"), os.O_WRONLY, 0)
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
	h.DB.QueryRow(`SELECT COUNT(*) FROM media WHERE name LIKE 'IMG_2%'`).Scan(&nmedia)
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
	h.finMu.Lock()
	var patch, post int
	var postM map[string]any
	wg.Add(2)
	go func() {
		defer wg.Done()
		patch, _, _ = ana.do("PATCH", "/api/uploads/"+id+"?chunk=0", foto)
	}()
	for i := 0; i < 200; i++ { // hasta que el PATCH haya escrito todo; luego un margen para que entre a finalize
		if st, err := os.Stat(h.tmp(id + ".part")); err == nil && st.Size() == int64(len(foto)) {
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
	h.finMu.Unlock()
	wg.Wait()
	if patch != 200 || post != 200 || postM["done"] != true || postM["dup"] != false {
		t.Fatalf("el POST debe esperar y ver la subida ya terminada: PATCH %d, POST %d %v", patch, post, postM)
	}
	if _, err := os.Stat(filepath.Join(data, "ana", "fotos", "2025", "03", "IMG_10.jpg")); err != nil {
		t.Fatalf("la foto debía quedar guardada: %v", err)
	}
	if _, err := os.Stat(h.tmp(id + ".json")); err == nil {
		t.Fatal("el POST recreó una subida ya terminada")
	}

	// Se pierde la respuesta del PATCH que cerró un archivo y el cliente repite el POST: no se
	// vuelven a pedir trozos ni aparece "nombre (1)".
	meta = mustJSON(map[string]any{"kind": "file", "name": "perdida.bin", "size": len(body), "mtime": 7})
	_, m, _ = ana.do("POST", "/api/uploads", meta)
	id = m["id"].(string)
	for i := range 5 {
		ana.do("PATCH", fmt.Sprintf("/api/uploads/%s?chunk=%d", id, i), body[i<<20:min(len(body), (i+1)<<20)])
	}
	if _, m, raw := ana.do("POST", "/api/uploads", meta); m["done"] != true || m["missing"] != nil {
		t.Fatalf("el reintento debe ver la subida terminada: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(data, "ana", "archivos", "perdida (1).bin")); err == nil {
		t.Fatal("el reintento guardó una copia")
	}

	// Dos imágenes distintas con el mismo nombre y tamaño se guardan las dos; un video con el
	// mismo nombre y tamaño que otro ya guardado se toma por duplicado sin recibir datos.
	for i, b := range [][]byte{[]byte("escaneo-A"), []byte("escaneo-B")} {
		_, m, _ := ana.do("POST", "/api/uploads", mustJSON(map[string]any{"kind": "photo", "name": "scan.tif", "size": len(b), "mtime": 1741536000000 + i}))
		id, ok := m["id"].(string)
		if !ok {
			t.Fatalf("imagen %d tomada por duplicado antes de subirla: %v", i, m)
		}
		if _, m, raw := ana.do("PATCH", "/api/uploads/"+id+"?chunk=0", b); m["done"] != true || m["dup"] != false {
			t.Fatalf("imagen %d: %s", i, raw)
		}
	}
	h.DB.QueryRow(`SELECT COUNT(*) FROM media WHERE name = 'scan.tif'`).Scan(&nmedia)
	if nmedia != 2 {
		t.Fatalf("deben quedar las dos imágenes: hay %d", nmedia)
	}
	video := []byte("video-de-ios")
	_, m, _ = ana.do("POST", "/api/uploads", mustJSON(map[string]any{"kind": "photo", "name": "IMG_5.MOV", "size": len(video), "mtime": 1741536000000}))
	ana.do("PATCH", "/api/uploads/"+m["id"].(string)+"?chunk=0", video)
	if _, m, raw := ana.do("POST", "/api/uploads", mustJSON(map[string]any{"kind": "photo", "name": "IMG_5.MOV", "size": len(video), "mtime": 1741536999000})); m["dup"] != true {
		t.Fatalf("el video con mismo nombre y tamaño debe ser duplicado: %s", raw)
	}
}
