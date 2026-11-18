package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reindex fuerza un recorrido del disco sin esperar el intervalo entre recorridos.
func (a *app) reindex() {
	a.idxMu.Lock()
	a.idxAt = time.Time{}
	a.idxMu.Unlock()
	a.syncTracks()
}

// musicSetup crea una biblioteca sin etiquetas (en las pruebas no hay ffprobe) y dos usuarios con sesión.
func musicSetup(t *testing.T) (a *app, data string, ana, luis client) {
	data = t.TempDir()
	a, err := open(data)
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"Niña Pastori/Cañaílla/01 - Tú me camelas.mp3": "a",
		"Niña Pastori/Cañaílla/02 - Cai.mp3":           "b",
		"Niña Pastori/Cañaílla/notas.txt":              "privado",
		"Niña Pastori/Joyas/1. Amor de San Juan.flac":  "c",
		"Juanes/Un día normal/A Dios le pido.mp3":      "d",
		"suelta.mp3":    "e",
		".oculta/x.mp3": "f",
	} {
		p := filepath.Join(data, "media", "musica", filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	os.MkdirAll(filepath.Join(data, "ana", "archivos"), 0o755)
	os.WriteFile(filepath.Join(data, "ana", "archivos", "x.mp3"), []byte("privado"), 0o644)
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	for _, n := range []string{"ana", "luis"} {
		if err := a.addUser(n, "clave-segura"); err != nil {
			t.Fatal(err)
		}
	}
	newClient := func(name string) client {
		jar, _ := cookiejar.New(nil)
		c := client{t, srv.URL, &http.Client{Jar: jar}}
		if c.login(name, "clave-segura") != 200 {
			t.Fatal("login")
		}
		return c
	}
	return a, data, newClient("ana"), newClient("luis")
}

// titles devuelve los títulos de una lista de pistas en JSON, separados por "|".
func titles(v any) string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(map[string]any)["title"].(string))
	}
	return strings.Join(out, "|")
}

func TestIndiceYBusqueda(t *testing.T) {
	a, data, ana, _ := musicSetup(t)
	if code, _, _ := (client{t, ana.base, &http.Client{}}).do("GET", "/api/music", nil); code != 401 {
		t.Fatalf("sin sesión debe dar 401: %d", code)
	}
	code, m, raw := ana.do("GET", "/api/music", nil)
	if code != 200 || len(m["albums"].([]any)) != 4 || len(m["artists"].([]any)) != 2 {
		t.Fatalf("biblioteca: %d %s", code, raw)
	}
	if s := string(raw); strings.Contains(s, "oculta") || strings.Contains(s, "notas") {
		t.Fatalf("el índice solo lleva audio visible: %s", s)
	}

	// Sin etiquetas, título, número, álbum y artista salen de la ruta.
	_, m, raw = ana.do("GET", "/api/music/album?path="+"Ni%C3%B1a%20Pastori/Ca%C3%B1a%C3%ADlla", nil)
	if m["name"] != "Cañaílla" || m["artist"] != "Niña Pastori" || titles(m["tracks"]) != "Tú me camelas|Cai" {
		t.Fatalf("álbum: %s", raw)
	}
	if tr := m["tracks"].([]any)[1].(map[string]any); tr["num"] != float64(2) || tr["artistPath"] != "Niña Pastori" {
		t.Fatalf("pista: %v", tr)
	}
	if code, _, _ := ana.do("GET", "/api/music/album?path=No%20existe", nil); code != 404 {
		t.Fatalf("álbum inexistente: %d", code)
	}
	_, m, raw = ana.do("GET", "/api/music/artist?path=Ni%C3%B1a%20Pastori", nil)
	if len(m["albums"].([]any)) != 2 || len(m["tracks"].([]any)) != 3 || len(m["popular"].([]any)) != 3 {
		t.Fatalf("artista: %s", raw)
	}

	// Búsqueda: sin tildes ni mayúsculas, por título, álbum o artista, y con varias palabras.
	for q, want := range map[string]string{"tu%20me": "Tú me camelas", "NINA%20cai": "Cai", "dia%20normal": "A Dios le pido", "100%25": "", "_": ""} {
		_, m, raw = ana.do("GET", "/api/music/search?q="+q, nil)
		if got := titles(m["tracks"]); got != want {
			t.Fatalf("buscar %q: %q, quiero %q (%s)", q, got, want, raw)
		}
	}
	_, m, raw = ana.do("GET", "/api/music/search?q=pastori", nil)
	if len(m["artists"].([]any)) != 1 || len(m["albums"].([]any)) != 2 || len(m["tracks"].([]any)) != 3 {
		t.Fatalf("buscar artista: %s", raw)
	}

	// El índice sigue al disco: lo borrado desaparece y lo cambiado se vuelve a leer.
	os.Remove(filepath.Join(data, "media", "musica", "Juanes", "Un día normal", "A Dios le pido.mp3"))
	os.WriteFile(filepath.Join(data, "media", "musica", "Juanes", "Un día normal", "La paga.mp3"), []byte("nueva"), 0o644)
	a.reindex()
	_, m, raw = ana.do("GET", "/api/music/search?q=juanes", nil)
	if titles(m["tracks"]) != "La paga" {
		t.Fatalf("índice desactualizado: %s", raw)
	}
}

func TestEtiquetas(t *testing.T) {
	tr := fromPath("Artista/Álbum/07 - nombre de archivo.mp3")
	if tr.Title != "nombre de archivo" || tr.Num != 7 || tr.Album != "Álbum" || tr.Artist != "Artista" || tr.AlbumPath != "Artista/Álbum" {
		t.Fatalf("respaldo por ruta: %+v", tr)
	}
	applyTags(&tr, []byte(`{"streams":[{"tags":{"TITLE":"De la pista","ARTIST":"Otra"}}],
		"format":{"duration":"187.52","tags":{"title":" Título real ","album":"Disco","track":"3/12","disc":"2/2","date":"2019-05-01"}}}`))
	if tr.Title != "Título real" || tr.Artist != "Otra" || tr.Album != "Disco" || tr.Num != 3 || tr.disc != 2 || tr.Year != 2019 || tr.Dur != 187.52 {
		t.Fatalf("etiquetas: %+v", tr)
	}
	// ffprobe ausente o archivo corrupto: se conserva lo deducido de la ruta.
	tr = fromPath("suelta.mp3")
	applyTags(&tr, nil)
	if tr.Title != "suelta" || tr.Album != "Sin álbum" || tr.AlbumPath != "" || tr.ArtistPath != "" {
		t.Fatalf("pista suelta: %+v", tr)
	}
}

func TestMeGustaEHistorial(t *testing.T) {
	_, _, ana, luis := musicSetup(t)
	cai := "Ni%C3%B1a%20Pastori/Ca%C3%B1a%C3%ADlla/02%20-%20Cai.mp3"
	for _, p := range []string{cai, "Juanes/Un%20d%C3%ADa%20normal/A%20Dios%20le%20pido.mp3", cai} { // repetir no duplica
		if code, _, raw := ana.do("PUT", "/api/music/likes?path="+p, nil); code != 200 {
			t.Fatalf("me gusta: %d %s", code, raw)
		}
	}
	_, _, raw := ana.do("GET", "/api/music/likes", nil)
	var liked []any
	json.Unmarshal(raw, &liked)
	if titles(liked) != "A Dios le pido|Cai" {
		t.Fatalf("me gusta, lo más reciente primero: %s", raw)
	}
	// Solo se guardan pistas reales de la biblioteca, en su ruta exacta.
	for _, p := range []string{
		"../../ana/archivos/x.mp3", // fuera de la biblioteca
		"Juanes/../Juanes/Un%20d%C3%ADa%20normal/A%20Dios%20le%20pido.mp3", // con ".."
		"/suelta.mp3", // no canónica
		"Ni%C3%B1a%20Pastori/Ca%C3%B1a%C3%ADlla/notas.txt", // no es audio
		"Juanes", // carpeta
		"no-existe.mp3",
		"",
	} {
		if code, _, _ := ana.do("PUT", "/api/music/likes?path="+p, nil); code != 404 {
			t.Fatalf("me gusta a %q debe dar 404, dio %d", p, code)
		}
		if code, _, _ := ana.do("POST", "/api/music/plays", mustJSON(map[string]string{"path": strings.ReplaceAll(p, "%20", " ")})); code != 404 {
			t.Fatalf("escucha de %q debe dar 404, dio %d", p, code)
		}
	}
	// Cada usuario tiene los suyos.
	_, m, _ := luis.do("GET", "/api/music", nil)
	if len(m["likes"].([]any)) != 0 {
		t.Fatalf("luis no ha marcado nada: %v", m["likes"])
	}
	luis.do("DELETE", "/api/music/likes?path="+cai, nil)
	if _, m, _ = ana.do("GET", "/api/music", nil); len(m["likes"].([]any)) != 2 {
		t.Fatalf("luis no puede quitar los de ana: %v", m["likes"])
	}
	ana.do("DELETE", "/api/music/likes?path="+cai, nil)
	if _, m, _ = ana.do("GET", "/api/music", nil); len(m["likes"].([]any)) != 1 {
		t.Fatalf("quitar me gusta: %v", m["likes"])
	}

	// Historial: alimenta lo más escuchado, lo reciente y los populares del artista, por usuario.
	for _, p := range []string{"Niña Pastori/Joyas/1. Amor de San Juan.flac", "suelta.mp3", "Niña Pastori/Joyas/1. Amor de San Juan.flac"} {
		if code, _, raw := ana.do("POST", "/api/music/plays", mustJSON(map[string]string{"path": p})); code != 200 {
			t.Fatalf("escucha: %d %s", code, raw)
		}
	}
	_, m, raw = ana.do("GET", "/api/music", nil)
	if titles(m["top"]) != "Amor de San Juan|suelta" {
		t.Fatalf("lo más escuchado: %s", raw)
	}
	for _, x := range m["albums"].([]any) {
		if al := x.(map[string]any); (al["name"] == "Joyas") != (al["plays"] == float64(2)) || (al["played"].(float64) > 0) != (al["plays"].(float64) > 0) {
			t.Fatalf("escuchas por álbum: %v", al)
		}
	}
	_, m, raw = ana.do("GET", "/api/music/artist?path=Ni%C3%B1a%20Pastori", nil)
	if !strings.HasPrefix(titles(m["popular"]), "Amor de San Juan|") {
		t.Fatalf("populares: %s", raw)
	}
	if _, m, _ = luis.do("GET", "/api/music", nil); len(m["top"].([]any)) != 0 {
		t.Fatalf("el historial de ana no es de luis: %v", m["top"])
	}
}

func TestPlaylists(t *testing.T) {
	ap, data, ana, luis := musicSetup(t)
	for _, body := range []string{`{"name":"  "}`, `{"name":"` + strings.Repeat("x", 101) + `"}`, `no es json`} {
		if code, _, _ := ana.do("POST", "/api/music/playlists", []byte(body)); code != 400 {
			t.Fatalf("nombre inválido %q debe dar 400, dio %d", body, code)
		}
	}
	code, m, raw := ana.do("POST", "/api/music/playlists", []byte(`{"name":" Para el carro "}`))
	if code != 200 || m["name"] != "Para el carro" {
		t.Fatalf("crear: %d %s", code, raw)
	}
	pl := "/api/music/playlists/" + strings.TrimSpace(string(mustJSON(m["id"])))
	a, b, c := "Niña Pastori/Cañaílla/01 - Tú me camelas.mp3", "Niña Pastori/Cañaílla/02 - Cai.mp3", "suelta.mp3"
	for _, p := range []string{a, b, c} {
		if code, m, raw := ana.do("POST", pl+"/tracks", mustJSON(map[string]string{"path": p})); code != 200 || m["added"] != true {
			t.Fatalf("agregar: %d %s", code, raw)
		}
	}
	if _, m, _ := ana.do("POST", pl+"/tracks", mustJSON(map[string]string{"path": a})); m["added"] != false {
		t.Fatal("una pista no entra dos veces")
	}
	for _, p := range []string{"../../ana/archivos/x.mp3", "Juanes/../suelta.mp3", "Niña Pastori/Cañaílla/notas.txt", "Juanes", ""} {
		if code, _, _ := ana.do("POST", pl+"/tracks", mustJSON(map[string]string{"path": p})); code != 404 {
			t.Fatalf("agregar %q debe dar 404, dio %d", p, code)
		}
	}
	order := func(want string) {
		t.Helper()
		_, m, raw := ana.do("GET", pl, nil)
		if titles(m["tracks"]) != want {
			t.Fatalf("orden: %s, quiero %s", raw, want)
		}
	}
	order("Tú me camelas|Cai|suelta")
	if code, _, raw := ana.do("PUT", pl+"/order", mustJSON(map[string]any{"paths": []string{c, a, b}})); code != 200 {
		t.Fatalf("reordenar: %d %s", code, raw)
	}
	order("suelta|Tú me camelas|Cai")
	// Un orden con pistas ajenas a la playlist no cambia nada.
	if code, _, _ := ana.do("PUT", pl+"/order", mustJSON(map[string]any{"paths": []string{b, "Juanes/Un día normal/A Dios le pido.mp3"}})); code != 400 {
		t.Fatalf("orden inválido debe dar 400, dio %d", code)
	}
	order("suelta|Tú me camelas|Cai")
	ana.do("DELETE", pl+"/tracks?path=Ni%C3%B1a%20Pastori/Ca%C3%B1a%C3%ADlla/01%20-%20T%C3%BA%20me%20camelas.mp3", nil)
	ana.do("POST", pl+"/tracks", mustJSON(map[string]string{"path": "Juanes/Un día normal/A Dios le pido.mp3"}))
	order("suelta|Cai|A Dios le pido") // lo nuevo va al final aunque se haya reordenado antes
	if code, _, _ := ana.do("PATCH", pl, []byte(`{"name":"Carretera"}`)); code != 200 {
		t.Fatalf("renombrar: %d", code)
	}
	_, m, raw = ana.do("GET", "/api/music", nil)
	if p := m["playlists"].([]any)[0].(map[string]any); p["name"] != "Carretera" || p["tracks"] != float64(3) || p["cover"] != "" {
		t.Fatalf("lista de playlists: %s", raw)
	}
	if _, m, raw = ana.do("GET", "/api/music/search?q=carre", nil); len(m["playlists"].([]any)) != 1 {
		t.Fatalf("buscar playlist: %s", raw)
	}

	// Una pista que desaparece del disco sale de la lista sin romper la playlist.
	os.Remove(filepath.Join(data, "media", "musica", "suelta.mp3"))
	ap.reindex()
	order("Cai|A Dios le pido")
	if code, _, raw := ana.do("PUT", pl+"/order", mustJSON(map[string]any{"paths": []string{"Juanes/Un día normal/A Dios le pido.mp3", b}})); code != 200 {
		t.Fatalf("reordenar lo visible: %d %s", code, raw)
	}
	order("A Dios le pido|Cai")

	// Aislamiento: para otro usuario la playlist no existe.
	if _, m, _ := luis.do("GET", "/api/music", nil); len(m["playlists"].([]any)) != 0 {
		t.Fatalf("luis no debe ver playlists de ana: %v", m["playlists"])
	}
	if _, m, _ := luis.do("GET", "/api/music/search?q=carre", nil); len(m["playlists"].([]any)) != 0 {
		t.Fatal("luis no debe encontrar playlists de ana")
	}
	for _, req := range [][3]string{
		{"GET", pl, ""}, {"PATCH", pl, `{"name":"robada"}`}, {"DELETE", pl, ""},
		{"POST", pl + "/tracks", `{"path":"` + b + `"}`}, {"DELETE", pl + "/tracks?path=suelta.mp3", ""},
		{"PUT", pl + "/order", `{"paths":[]}`}, {"GET", "/api/music/playlists/abc", ""}, {"GET", "/api/music/playlists/999", ""},
	} {
		if code, _, _ := luis.do(req[0], req[1], []byte(req[2])); code != 404 {
			t.Fatalf("%s %s de otro usuario debe dar 404, dio %d", req[0], req[1], code)
		}
	}
	if _, m, _ := ana.do("GET", pl, nil); m["name"] != "Carretera" || len(m["tracks"].([]any)) != 2 {
		t.Fatalf("la playlist de ana cambió: %v", m)
	}

	if code, _, _ := ana.do("DELETE", pl, nil); code != 200 {
		t.Fatalf("borrar: %d", code)
	}
	if code, _, _ := ana.do("GET", pl, nil); code != 404 {
		t.Fatalf("la playlist borrada debe dar 404, dio %d", code)
	}
}
