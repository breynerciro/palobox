package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
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
	if _, m = cmd(luis, "luistelefono", map[string]any{"op": "next"}); room(m)["pos"].(float64) != 1 {
		t.Fatalf("next: %v", m)
	}
	// Dos dispositivos avisan que terminó la misma pista: solo se avanza una vez.
	cmd(ana, "anatelefono", map[string]any{"op": "ended", "pos": 1})
	if _, m = cmd(luis, "luistelefono", map[string]any{"op": "ended", "pos": 1}); room(m)["pos"].(float64) != 2 {
		t.Fatalf("ended repetido: %v", m)
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
}
