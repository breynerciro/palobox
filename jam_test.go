package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"palobox/internal/push"
)

// Salas de reproducción: elegir dispositivo y jam entre usuarios.
func TestJam(t *testing.T) {
	_, _, ana, luis := musicSetup(t)
	ana.do("GET", "/api/music", nil) // deja el índice de música al día
	const cai, dios, suelta = "Niña Pastori/Cañaílla/02 - Cai.mp3", "Juanes/Un día normal/A Dios le pido.mp3", "suelta.mp3"
	cmd := func(c client, device string, v map[string]any) (int, map[string]any) {
		t.Helper()
		v["device"] = device
		code, m, _ := c.do("POST", "/api/jam", mustJSON(v))
		return code, m
	}
	room := func(m map[string]any) map[string]any { r, _ := m["room"].(map[string]any); return r }

	if code, _ := cmd(ana, "anatelefono", map[string]any{"op": "next"}); code != 409 {
		t.Fatalf("sin sala debe dar 409, dio %d", code)
	}
	if code, _ := cmd(ana, "x", map[string]any{"op": "start"}); code != 400 {
		t.Fatalf("dispositivo inválido: %d", code)
	}
	// Ana pasa su cola a una sala; una ruta que no existe se descarta.
	_, m := cmd(ana, "anatelefono", map[string]any{"op": "start", "paths": []string{cai, "no/existe.mp3", dios}, "pos": 0, "at": 12.5, "playing": true})
	r := room(m)
	if r == nil || len(r["queue"].([]any)) != 2 || r["playing"] != true || r["at"].(float64) < 12.5 || r["open"] != false {
		t.Fatalf("start: %v", m)
	}
	code := r["code"].(string)

	// El flujo de eventos entrega la foto al conectarse, con la cola completa.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ana.base+"/api/jam/events?device=anatablet00&name=Tablet", nil)
	res, err := ana.c.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("eventos: %v", err)
	}
	defer res.Body.Close()
	events := bufio.NewReader(res.Body)
	next := func() map[string]any {
		t.Helper()
		for {
			line, err := events.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var v map[string]any
				json.Unmarshal([]byte(data), &v)
				return v
			}
		}
	}
	first := next()
	if first["me"] != "anatablet00" || len(room(first)["queue"].([]any)) != 2 || len(first["devices"].([]any)) != 1 {
		t.Fatalf("primer evento: %v", first)
	}
	// Elegir dispositivo: solo uno conectado de la misma cuenta.
	if code, _ := cmd(ana, "anatelefono", map[string]any{"op": "here", "target": "noconectado"}); code != 409 {
		t.Fatalf("pasar a un dispositivo sin conexión: %d", code)
	}
	if _, m := cmd(ana, "anatelefono", map[string]any{"op": "here", "target": "anatablet00"}); m["devices"].([]any)[0].(map[string]any)["active"] != true {
		t.Fatalf("here: %v", m)
	}
	if ev := next(); ev["devices"].([]any)[0].(map[string]any)["active"] != true || room(ev)["queue"] != nil {
		t.Fatalf("el cambio debe llegar por el flujo, sin repetir la cola: %v", ev)
	}

	// Varios a la vez: «add» suma un dispositivo a los que suenan, y repetirlo lo quita.
	devs := func(m map[string]any) (on int) {
		for _, d := range m["devices"].([]any) {
			if d.(map[string]any)["active"] == true {
				on++
			}
		}
		return on
	}
	req2, _ := http.NewRequestWithContext(ctx, "GET", ana.base+"/api/jam/events?device=anaparlante&name=Parlante", nil)
	res2, err := ana.c.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	bufio.NewReader(res2.Body).ReadString('\n') // conectado
	if _, m := cmd(ana, "anatelefono", map[string]any{"op": "here", "target": "anaparlante", "add": true}); devs(m) != 2 {
		t.Fatalf("sonar en dos a la vez: %v", m)
	}
	if _, m := cmd(ana, "anatelefono", map[string]any{"op": "here", "target": "anaparlante", "add": true}); devs(m) != 1 {
		t.Fatalf("quitar uno: %v", m)
	}
	next() // los avisos de estos cambios
	next()
	next()

	// Jam: no se entra a una sala personal ni con un código inventado; sí cuando se abre.
	if code, _ := cmd(luis, "luistelefono", map[string]any{"op": "join", "code": code}); code != 404 {
		t.Fatalf("sala sin abrir: %d", code)
	}
	cmd(ana, "anatelefono", map[string]any{"op": "open"})
	if code, _ := cmd(luis, "luistelefono", map[string]any{"op": "join", "code": "ZZZZZZ"}); code != 404 {
		t.Fatalf("código inventado: %d", code)
	}
	_, m = cmd(luis, "luistelefono", map[string]any{"op": "join", "code": strings.ToLower(code)})
	if r := room(m); r == nil || len(r["members"].([]any)) != 2 || len(r["queue"].([]any)) != 2 {
		t.Fatalf("join: %v", m)
	}
	// El invitado agrega a continuación y cambia de canción.
	_, m = cmd(luis, "luistelefono", map[string]any{"op": "add", "paths": []string{suelta}, "next": true, "qrev": 1})
	q := room(m)["queue"].([]any)
	if len(q) != 3 || q[1].(map[string]any)["path"] != suelta {
		t.Fatalf("add: %v", m)
	}
	// «Agregar a la cola» va detrás de lo ya agregado a mano, no de primero; y la cola se puede reordenar (tail).
	last := q[2].(map[string]any)["path"].(string)
	_, m = cmd(luis, "luistelefono", map[string]any{"op": "add", "paths": []string{last}})
	if q = room(m)["queue"].([]any); len(q) != 4 || q[1].(map[string]any)["path"] != suelta || q[2].(map[string]any)["path"] != last || room(m)["manual"].(float64) != 2 {
		t.Fatalf("add al final de lo agregado: %v", m)
	}
	_, m = cmd(luis, "luistelefono", map[string]any{"op": "tail", "paths": []string{suelta, last}, "manual": 1})
	if q = room(m)["queue"].([]any); len(q) != 3 || q[1].(map[string]any)["path"] != suelta || room(m)["manual"].(float64) != 1 {
		t.Fatalf("tail con manual: %v", m)
	}
	if _, m = cmd(luis, "luistelefono", map[string]any{"op": "next"}); room(m)["pos"].(float64) != 1 {
		t.Fatalf("next: %v", m)
	}
	if _, m = cmd(ana, "anatelefono", map[string]any{"op": "next"}); room(m)["pos"].(float64) != 2 {
		t.Fatalf("next del otro: %v", m)
	}
	if _, m = cmd(ana, "anatelefono", map[string]any{"op": "pause"}); room(m)["playing"] != false {
		t.Fatalf("pause: %v", m)
	}
	if _, m = cmd(ana, "anatelefono", map[string]any{"op": "seek", "at": 30}); room(m)["at"].(float64) != 30 || room(m)["playing"] != false {
		t.Fatalf("seek: %v", m)
	}
	// Quitar de la cola exige que la fila siga siendo la misma (la cola ya trae sugerencias al final).
	cmd(ana, "anatelefono", map[string]any{"op": "jump", "index": 0})
	_, m = cmd(ana, "anatelefono", map[string]any{"op": "remove", "index": 1, "path": dios, "qrev": 0})
	before := len(room(m)["queue"].([]any))
	if _, m = cmd(ana, "anatelefono", map[string]any{"op": "remove", "index": 1, "path": suelta, "qrev": 0}); len(room(m)["queue"].([]any)) != before-1 {
		t.Fatalf("remove: %v", m)
	}
	// Al salir, el otro sigue en la sala; quien salió ya no puede mandar.
	if _, m = cmd(luis, "luistelefono", map[string]any{"op": "leave"}); room(m) != nil {
		t.Fatalf("leave: %v", m)
	}
	if code, _ := cmd(luis, "luistelefono", map[string]any{"op": "play"}); code != 409 {
		t.Fatalf("fuera de la sala: %d", code)
	}
	if _, m = cmd(ana, "anatelefono", map[string]any{"op": "play"}); len(room(m)["members"].([]any)) != 1 {
		t.Fatalf("la sala sigue con ana: %v", m)
	}
	// Reordenar una cola vacía no tumba el servidor.
	cmd(luis, "luistelefono", map[string]any{"op": "start"})
	if code, _ := cmd(luis, "luistelefono", map[string]any{"op": "tail", "paths": []string{suelta}}); code != 200 {
		t.Fatalf("tail con la cola vacía: %d", code)
	}
}

// fakeM4A arma un M4A mínimo: n cuadros de size bytes rellenos con fill (mdat) y el índice que los describe (moov)
// como AAC-LC a 44,1 kHz en estéreo. Es lo que lee internal/music/m4a.go.
func fakeM4A(fill byte, n, size int) []byte {
	u32 := func(v int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }
	box := func(kind string, parts ...[]byte) []byte {
		body := bytes.Join(parts, nil)
		return append(append(u32(8+len(body)), kind...), body...)
	}
	zero := func(k int) []byte { return make([]byte, k) }
	ftyp := box("ftyp", []byte("M4A "), u32(0), []byte("M4A mp42"))
	mdat := box("mdat", bytes.Repeat([]byte{fill}, n*size))
	esds := box("esds", u32(0), []byte{3, 25, 0, 1, 0, 4, 17, 0x40, 0x15}, zero(11), []byte{5, 2, 0x12, 0x10, 6, 1, 2})
	mp4a := box("mp4a", zero(6), []byte{0, 1}, zero(8), []byte{0, 2, 0, 16}, zero(4), u32(44100<<16), esds)
	sizes := []byte{}
	for i := 0; i < n; i++ {
		sizes = append(sizes, u32(size)...)
	}
	stbl := box("stbl",
		box("stsd", u32(0), u32(1), mp4a),
		box("stsz", u32(0), u32(0), u32(n), sizes),
		box("stsc", u32(0), u32(1), u32(1), u32(n), u32(1)),
		box("stco", u32(0), u32(1), u32(len(ftyp)+8)))
	hdlr := box("hdlr", u32(0), u32(0), []byte("soun"), zero(12), []byte{0})
	moov := box("moov", box("trak", box("mdia", hdlr, box("minf", stbl))))
	return bytes.Join([][]byte{ftyp, mdat, moov}, nil)
}

// Flujo continuo: el audio de la sala por una sola respuesta, sin etiquetas, y el servidor avanza la cola.
func TestJamStream(t *testing.T) {
	a, data, ana, _ := musicSetup(t)
	// M4A de mentira con la forma de uno real (fakeM4A): 40 cuadros AAC que, ya con su cabecera ADTS, miden 384 bytes.
	for i, name := range []string{"uno", "dos"} {
		os.WriteFile(filepath.Join(data, "media", "musica", name+".m4a"), fakeM4A('a'+byte(i), 40, 377), 0o644)
	}
	ana.do("GET", "/api/music", nil)
	a.music.DB.Exec(`UPDATE tracks SET dur = 1 WHERE path IN ('uno.m4a', 'dos.m4a')`) // un segundo cada una
	a.jam.Lead = 300 * time.Millisecond                                               // el colchón real (6 s) es más largo que estas pistas

	if code, _, _ := ana.do("GET", "/api/jam/stream?device=anatelefono", nil); code != 409 {
		t.Fatalf("sin sala no hay flujo: %d", code)
	}
	cmd := func(v map[string]any) map[string]any {
		v["device"] = "anatelefono"
		_, m, _ := ana.do("POST", "/api/jam", mustJSON(v))
		return m
	}
	cmd(map[string]any{"op": "start", "paths": []string{"uno.m4a", "dos.m4a"}, "pos": 0, "playing": true})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ana.base+"/api/jam/stream?device=anatelefono", nil)
	res, err := ana.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Type") != "audio/aac" || res.ContentLength != -1 {
		t.Fatalf("debe ser una emisión sin tamaño: %q %d", res.Header.Get("Content-Type"), res.ContentLength)
	}
	// Llegan los dos cuerpos seguidos por la misma conexión: primero «uno» y después «dos». Son 80 cuadros; el
	// primero puede faltar, porque el flujo arranca en el cuadro siguiente a la posición de la sala.
	got := make([]byte, 79*384)
	if n, err := io.ReadFull(res.Body, got); err != nil {
		t.Fatalf("el flujo se cortó antes de mandar las dos pistas: %v (llegaron %d bytes)", err, n)
	}
	if bytes.Contains(got, []byte("moov")) || bytes.Contains(got, []byte("ftyp")) || got[0] != 0xFF || got[1] != 0xF1 {
		t.Fatal("por el flujo solo van cuadros ADTS, no las cajas del archivo")
	}
	na, nb, cambio := bytes.Count(got, []byte{'a'}), bytes.Count(got, []byte{'b'}), bytes.IndexByte(got, 'b')
	if na < 39*370 || nb < 39*370 || bytes.IndexByte(got[cambio:], 'a') >= 0 {
		t.Fatalf("deben llegar las dos pistas completas y en orden: %d de la primera, %d de la segunda", na, nb)
	}
	// El servidor avanzó la sala por su cuenta: nadie mandó «ended».
	if m := cmd(map[string]any{"op": "pause"}); m["room"].(map[string]any)["pos"].(float64) < 1 {
		t.Fatalf("la sala debe ir en la segunda pista o más: %v", m["room"])
	}

	// Descarga para escuchar sin conexión: el mismo audio limpio, completo y solo con sesión.
	if code, _, raw := ana.do("GET", "/api/music/offline?path=uno.m4a", nil); code != 200 || len(raw) != 40*384 || bytes.Contains(raw, []byte("moov")) || raw[0] != 0xFF {
		t.Fatalf("offline: %d, %d bytes", code, len(raw))
	}
	if code, _, _ := (client{t, ana.base, &http.Client{}}).do("GET", "/api/music/offline?path=uno.m4a", nil); code != 401 {
		t.Fatalf("offline sin sesión: %d", code)
	}
	if code, _, _ := ana.do("GET", "/api/music/offline?path=../x.mp3", nil); code != 404 {
		t.Fatalf("offline fuera de la biblioteca: %d", code)
	}

	// «cut»: el teléfono se salta su colchón y el envío vuelve a empezar en lo que la sala suena ahora, por la
	// misma conexión; «state» solo devuelve la foto.
	if m := cmd(map[string]any{"op": "state"}); m["room"] == nil {
		t.Fatalf("state: %v", m)
	}
	cmd(map[string]any{"op": "jump", "index": 0})
	cmd(map[string]any{"op": "cut"})
	more := make([]byte, 10*384)
	if _, err := io.ReadFull(res.Body, more); err != nil || bytes.Count(more[5*384:], []byte{'a'}) < 4*370 {
		t.Fatalf("tras «cut» debe llegar de nuevo la primera pista: %v %q", err, more[len(more)-8:])
	}

	// Saltar al abrir el flujo: la acción viaja en la petición y repetirla (Safari lo hace) no salta dos veces.
	cmd(map[string]any{"op": "jump", "index": 0})
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", ana.base+"/api/jam/stream?device=anatelefono&op=next&from=0&n=abc", nil)
		if r2, err := ana.c.Do(req); err == nil {
			r2.Body.Close() // con la respuesta iniciada la acción ya se aplicó
		}
	}
	if m := cmd(map[string]any{"op": "pause"}); m["room"].(map[string]any)["pos"].(float64) != 1 {
		t.Fatalf("«siguiente» en la petición del flujo debe aplicarse una sola vez: %v", m["room"])
	}

	// Repetir esta canción: el flujo vuelve a mandar la misma pista en vez de pasar a la siguiente.
	res.Body.Close()
	cmd(map[string]any{"op": "repeat", "repeat": 2})
	cmd(map[string]any{"op": "jump", "index": 0})
	req, _ = http.NewRequestWithContext(ctx, "GET", ana.base+"/api/jam/stream?device=anatelefono", nil)
	if res, err = ana.c.Do(req); err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := io.ReadFull(res.Body, got); err != nil || bytes.IndexByte(got, 'b') >= 0 || bytes.Count(got, []byte{'a'}) < 78*370 {
		t.Fatalf("con repetir esta canción debe sonar dos veces la primera: %v", err)
	}
	room := func(m map[string]any) map[string]any { return m["room"].(map[string]any) }
	if r := room(cmd(map[string]any{"op": "state"})); r["pos"].(float64) != 0 || r["playing"] != true {
		t.Fatalf("repetir esta canción: al terminar sigue la misma: %v", r)
	}
	if r := room(cmd(map[string]any{"op": "next"})); r["pos"].(float64) != 1 {
		t.Fatalf("«siguiente» a mano sí pasa de canción: %v", r)
	}
	// Repetir toda la cola: después de la última vuelve la primera, sin sugerencias.
	res.Body.Close()
	cmd(map[string]any{"op": "queue", "paths": []string{"uno.m4a", "dos.m4a"}, "pos": 1}) // sin las sugerencias de antes
	cmd(map[string]any{"op": "repeat", "repeat": 1})
	req, _ = http.NewRequestWithContext(ctx, "GET", ana.base+"/api/jam/stream?device=anatelefono", nil)
	if res, err = ana.c.Do(req); err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := io.ReadFull(res.Body, got); err != nil || bytes.Count(got, []byte{'a'}) < 39*370 || bytes.IndexByte(got, 'a') < bytes.IndexByte(got, 'b') {
		t.Fatalf("repetir la cola: tras la última debe llegar la primera: %v", err)
	}
	if r := room(cmd(map[string]any{"op": "state"})); len(r["queue"].([]any)) != 2 || r["playing"] != true {
		t.Fatalf("repetir la cola: %v", r)
	}
	// En un jam no se repite.
	res.Body.Close()
	cmd(map[string]any{"op": "open"})
	cmd(map[string]any{"op": "jump", "index": 1})
	if r := room(cmd(map[string]any{"op": "next"})); r["pos"].(float64) == 0 {
		t.Fatalf("en un jam la cola no vuelve a empezar: %v", r)
	}
}

// El modo sin conexión vive en el navegador: sus pruebas (webtest/) corren con node, si está instalado.
func TestOfflineJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("sin node: las pruebas de web/offline.js no corren")
	}
	files, _ := filepath.Glob("webtest/*.test.mjs") // por archivo: no todas las versiones de node aceptan una carpeta
	if out, err := exec.Command(node, append([]string{"--test"}, files...)...).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Una sesión se renueva con el uso: quien usa la app no vuelve a pedir la clave, y una sesión que el servidor
// no reconoce significa cuenta eliminada o cerrada (el celular borra entonces lo descargado).
func TestSessionRenews(t *testing.T) {
	a, err := open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.auth.AddUser("ana", "clave-segura"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := client{t, srv.URL, &http.Client{Jar: jar}}
	if c.login("ana", "clave-segura") != 200 {
		t.Fatal("no entró")
	}
	soon := time.Now().Add(time.Hour).Unix() // le queda una hora
	a.auth.DB.Exec(`UPDATE sessions SET expires = ?`, soon)
	if code, _, _ := c.do("GET", "/api/me", nil); code != 200 {
		t.Fatalf("me: %d", code)
	}
	var exp int64
	a.auth.DB.QueryRow(`SELECT expires FROM sessions`).Scan(&exp)
	if exp-soon < 29*24*3600 {
		t.Errorf("la sesión no se renovó: vence en %d s", exp-time.Now().Unix())
	}
	a.auth.DB.Exec(`UPDATE sessions SET expires = 1`) // vencida de verdad: no revive
	if code, _, _ := c.do("GET", "/api/me", nil); code != 401 {
		t.Errorf("sesión vencida: %d, quería 401", code)
	}
}

// El service worker guarda una copia de la interfaz para abrir sin servidor: no puede faltarle ningún archivo.
func TestServiceWorkerFiles(t *testing.T) {
	sw, err := webFS.ReadFile("web/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := webFS.ReadDir("web")
	for _, e := range entries {
		name := e.Name()
		if name == "sw.js" || name == "index.html" { // index.html es «/»
			continue
		}
		if !bytes.Contains(sw, []byte("'/"+name+"'")) {
			t.Errorf("web/sw.js no incluye /%s: sin conexión la app no abriría", name)
		}
	}
}

// Descarga compacta para el modo sin conexión: la pista convertida a Opus (WebM), una sola vez.
func TestOfflineOpus(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("sin ffmpeg")
	}
	_, data, ana, _ := musicSetup(t)
	src := filepath.Join(data, "media", "musica", "real.mp3")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-b:a", "128k", src).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	var first []byte
	for i := 0; i < 2; i++ {
		code, _, raw := ana.do("GET", "/api/music/offline?path=real.mp3&fmt=opus", nil)
		if code != 200 || len(raw) < 1000 || !bytes.HasPrefix(raw, []byte{0x1A, 0x45, 0xDF, 0xA3}) || !bytes.Contains(raw, []byte("OpusHead")) {
			t.Fatalf("opus: %d, %d bytes", code, len(raw))
		}
		if i == 0 {
			first = raw
		} else if !bytes.Equal(first, raw) {
			t.Fatal("la segunda descarga debe salir de la copia en disco, igual a la primera")
		}
	}
	if files, _ := filepath.Glob(filepath.Join(data, ".palobox", "offline-opus", "*")); len(files) != 1 || !strings.HasSuffix(files[0], ".webm") {
		t.Fatalf("debe quedar una sola copia convertida y ningún archivo a medias: %v", files)
	}
	if code, _, _ := ana.do("GET", "/api/music/offline?path=no-existe.mp3&fmt=opus", nil); code != 404 {
		t.Fatalf("una pista que no existe: %d", code)
	}
	if code, _, _ := ana.do("GET", "/api/music/offline?path=../x.mp3&fmt=opus", nil); code != 404 {
		t.Fatalf("fuera de la biblioteca: %d", code)
	}
}

// Dispositivo dormido: la app de Android deja su token, se desconecta y sigue en la lista; al pasarle la música le
// llega un push. Un token que Firebase da por vencido se olvida.
func TestJamSleep(t *testing.T) {
	a, _, ana, _ := musicSetup(t)
	ana.do("GET", "/api/music", nil)
	pushed := make(chan string, 4)
	gone := false
	a.jam.Push = func(token string) error {
		pushed <- token
		if gone {
			return push.ErrGone
		}
		return nil
	}
	cmd := func(v map[string]any) (int, map[string]any) {
		t.Helper()
		code, m, _ := ana.do("POST", "/api/jam", mustJSON(v))
		return code, m
	}
	// El Android se conecta, deja su token y se duerme (se cae su flujo de eventos).
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ana.base+"/api/jam/events?device=anaandroid0&name=PaloBox%20en%20Android", nil)
	res, err := ana.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bufio.NewReader(res.Body).ReadString('\n')
	cmd(map[string]any{"device": "anaandroid0", "op": "push", "token": "tok-1"})
	cancel()
	res.Body.Close()
	time.Sleep(100 * time.Millisecond) // que el servidor note la desconexión

	// Desde el iPhone: el Android aparece dormido y se le puede pasar la música.
	_, m := cmd(map[string]any{"device": "anaiphone00", "op": "start", "paths": []string{"suelta.mp3"}, "playing": true})
	var found map[string]any
	for _, d := range m["devices"].([]any) {
		if d := d.(map[string]any); d["id"] == "anaandroid0" {
			found = d
		}
	}
	if found == nil || found["asleep"] != true || found["name"] != "PaloBox en Android" {
		t.Fatalf("el Android dormido no aparece: %v", m["devices"])
	}
	if code, m := cmd(map[string]any{"device": "anaiphone00", "op": "here", "target": "anaandroid0"}); code != 200 {
		t.Fatalf("pasar al dormido: %d %v", code, m)
	}
	select {
	case tok := <-pushed:
		if tok != "tok-1" {
			t.Fatalf("push a %q", tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no se mandó el push")
	}
	// Token vencido: se olvida y el dispositivo sale de la lista.
	gone = true
	cmd(map[string]any{"device": "anaiphone00", "op": "here", "target": "anaandroid0"})
	<-pushed
	time.Sleep(100 * time.Millisecond)
	if code, _ := cmd(map[string]any{"device": "anaiphone00", "op": "here", "target": "anaandroid0"}); code != 409 {
		t.Fatalf("un token vencido debía olvidarse: %d", code)
	}
}
