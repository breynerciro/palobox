package music

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"palobox/internal/gemini"
	"palobox/internal/httpx"
)

// respuesta arma un Ask falso: answer recibe la pregunta y devuelve lo que Gemini diría (o un error).
func respuesta(calls *int, answer func(hard bool, system, prompt string) (any, error)) func(context.Context, bool, string, string, map[string]any, any) error {
	return func(_ context.Context, hard bool, system, prompt string, _ map[string]any, out any) error {
		*calls++
		v, err := answer(hard, system, prompt)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
}

// Un artista nuevo se pregunta una sola vez; lo que el modelo liviano no reconoce pasa al capaz, y lo que
// ninguno reconoce queda guardado como desconocido para no volver a preguntar.
func TestClassify(t *testing.T) {
	l, _ := sintetica(t, 3) // tres artistas, ninguno en genres.json
	calls := 0
	l.Ask = respuesta(&calls, func(hard bool, system, prompt string) (any, error) {
		if !hard && (!strings.Contains(system, "tropical/merengue") || !strings.Contains(prompt, "Artista 0 amor | álbum: ")) {
			t.Errorf("pregunta mal armada:\n%s", prompt)
		}
		if hard { // segunda opinión: solo los dos que el liviano no reconoció
			if strings.Count(prompt, "\n") != 2 {
				t.Errorf("segunda opinión: %q", prompt)
			}
			return []map[string]any{{"i": 0, "g": "rap/hip-hop"}, {"i": 1, "g": "desconocido"}}, nil
		}
		return []map[string]any{{"i": 0, "g": "tropical/merengue"}, {"i": 1, "g": "desconocido"}, {"i": 2, "g": "género inventado"}}, nil
	})
	l.classify()
	if got := l.lista(t, `SELECT artist || '=' || genre || '/' || source FROM artist_genre ORDER BY artist`); got != "artista 0 amor=tropical/merengue/gemini|artista 1 noche=rap/hip-hop/gemini|artista 2 ciudad=/gemini" {
		t.Fatal(got)
	}
	s, _ := l.snap()
	if g := s.genreOf("Artista 0 amor/x/y.mp3"); g != "tropical/merengue" {
		t.Fatalf("la instantánea no ve el género nuevo: %q", g)
	}
	l.classify()
	if calls != 2 {
		t.Fatalf("solicitudes a Gemini: %d", calls)
	}
}

// Sin Gemini la clasificación se reintenta después, sin dejar a nadie marcado como desconocido.
func TestClassifySinGemini(t *testing.T) {
	l, _ := sintetica(t, 3)
	calls := 0
	l.Ask = respuesta(&calls, func(bool, string, string) (any, error) { return nil, gemini.ErrUnavailable })
	l.classify()
	l.classify() // dentro de la espera: no vuelve a preguntar
	if got := l.lista(t, `SELECT artist FROM artist_genre`); got != "" || calls != 1 {
		t.Fatalf("guardó %q con %d solicitudes", got, calls)
	}
}

// Un artista sin género hereda el de la playlist con la que llegó, si la mayoría es clara.
func TestInherit(t *testing.T) {
	l, _ := sintetica(t, 10)
	s, _ := l.snap()
	var paths []string
	for i, tr := range s.tracks {
		paths = append(paths, tr.Path)
		if i < 8 {
			s.genre[fold(tr.ArtistPath)] = "tropical/salsa"
		}
	}
	nuevo := fold(s.tracks[9].ArtistPath)
	l.exec(t, `INSERT INTO artist_genre VALUES(?, '', 'gemini')`, nuevo)                                  // Gemini no lo reconoció
	l.exec(t, `INSERT INTO artist_genre VALUES(?, 'rock/metal', 'gemini')`, fold(s.tracks[8].ArtistPath)) // a este sí
	l.inherit(paths)
	if got := l.lista(t, `SELECT genre || '/' || source FROM artist_genre ORDER BY source`); got != "rock/metal/gemini|tropical/salsa/playlist" {
		t.Fatal(got)
	}
}

func TestLocalIntent(t *testing.T) {
	l, _ := sintetica(t, 30)
	s, _ := l.snap()
	for _, c := range []struct {
		prompt, genres string
		from, to, n    int
		artists        int
	}{
		{"Merengues de los 90", "tropical/merengue", 1990, 1999, 30, 0},
		{"salsa y vallenato de los 80 y 90, 20 canciones", "tropical/salsa tropical/vallenato", 1980, 1999, 20, 0},
		{"rock en español ochentero", "rock/metal rock/rock rock/rock-espanol", 1980, 1989, 30, 0},
		{"música de 2010", "", 2010, 2019, 30, 0},
		{"lo mejor de artista 3 alcolirykoz", "", 0, 0, 30, 1},
		{"algo tranquilo para trabajar", "", 0, 0, 30, 0},
		{"reggaetón para la mañana", "urbano/regueton", 0, 0, 30, 0},
	} {
		in := s.localIntent(c.prompt)
		var gs []string
		for g := range in.genres {
			gs = append(gs, g)
		}
		slices.Sort(gs)
		if strings.Join(gs, " ") != c.genres || in.from != c.from || in.to != c.to || in.n != c.n || len(in.artists) != c.artists {
			t.Errorf("%q: géneros %v, %d-%d, n %d, artistas %v", c.prompt, gs, in.from, in.to, in.n, in.artists)
		}
	}
}

func TestPlaylistAI(t *testing.T) {
	l, u := sintetica(t, 600) // un artista por pista, años 1990-2019
	s, _ := l.snap()
	for i, tr := range s.tracks {
		s.genre[fold(tr.ArtistPath)] = []string{"tropical/merengue", "rap/hip-hop"}[i%2]
	}
	pedir := func(prompt string) (int, map[string]any) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/music/playlists/ai", strings.NewReader(`{"prompt":`+string(must(json.Marshal(prompt)))+`}`))
		if err := l.PlaylistAI(w, r, u); err != nil {
			e, _ := err.(httpx.Err)
			return e.Code, map[string]any{"error": err.Error()}
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	check := func(out map[string]any, n int) {
		t.Helper()
		ts, _ := out["tracks"].([]any)
		if len(ts) != n {
			t.Fatalf("%d canciones: %v", len(ts), out)
		}
		for _, x := range ts {
			tr := x.(map[string]any)
			if y := tr["year"].(float64); s.genreOf(tr["path"].(string)) != "tropical/merengue" || y < 1990 || y > 1999 {
				t.Fatalf("fuera del filtro: %v", tr)
			}
		}
	}
	calls := 0
	// El género y la década se entienden sin IA: una sola solicitud, la de elegir entre candidatas.
	l.Ask = respuesta(&calls, func(_ bool, system, prompt string) (any, error) {
		if strings.Count(prompt, "\n") < 50 || !strings.Contains(prompt, "«merengue de los 90»") {
			t.Errorf("candidatas: %.200s", prompt)
		}
		return map[string]any{"nombre": "Merengue noventero", "canciones": []int{3, 3, 1, 999, 7, 2, 9}}, nil
	})
	code, out := pedir("merengue de los 90")
	if check(out, 5); code != 200 || calls != 1 || out["name"] != "Merengue noventero" || out["left"].(float64) != aiPerDay-1 {
		t.Fatalf("%d, %d solicitudes: %v", code, calls, out)
	}
	// Sin Gemini sigue respondiendo con lo que entiende por palabras, y no gasta el cupo del usuario.
	l.Ask = respuesta(&calls, func(bool, string, string) (any, error) { return nil, gemini.ErrUnavailable })
	code, out = pedir("merengue de los 90")
	if check(out, aiTracks); code != 200 || out["name"] != "Merengue de los 90" || out["left"].(float64) != aiPerDay-1 {
		t.Fatalf("sin Gemini: %d %v", code, out)
	}
	if code, out = pedir("algo tranquilo para trabajar"); code != 503 {
		t.Fatalf("pedido abierto sin Gemini: %d %v", code, out)
	}
	// Pedido abierto: Gemini da el filtro y luego elige.
	calls = 0
	l.Ask = respuesta(&calls, func(_ bool, system, prompt string) (any, error) {
		if strings.Contains(system, "Traduces") {
			return map[string]any{"generos": []string{"tropical/merengue"}, "artistas": []string{}, "desde": 1990, "hasta": 1999}, nil
		}
		return map[string]any{"nombre": "x", "canciones": []int{0, 1, 2, 3, 4, 5}}, nil
	})
	if code, out = pedir("algo para bailar en una fiesta de los abuelos"); code != 200 || calls != 2 {
		t.Fatalf("pedido abierto: %d, %d solicitudes: %v", code, calls, out)
	}
	check(out, 6)
	// Tope diario por cuenta.
	for range aiPerDay - 2 {
		pedir("merengue de los 90")
	}
	if code, out = pedir("merengue de los 90"); code != 429 {
		t.Fatalf("tope diario: %d %v", code, out)
	}
	if code, _ = pedir("x"); code != 400 {
		t.Fatalf("pedido vacío: %d", code)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Examen de la clasificación contra genres.json, con Gemini de verdad. Solo corre a pedido:
//
//	GEMINI_KEY_FILE=llave.txt GENRES_EVAL=artistas.tsv go test ./internal/music -run TestGeneroConGemini -v
//
// artistas.tsv: artista, pistas, álbum y canción de muestra, separados por tabulador.
func TestGeneroConGemini(t *testing.T) {
	f, err := os.Open(os.Getenv("GENRES_EVAL"))
	g := gemini.Load()
	if err != nil || g == nil {
		t.Skip("sin GENRES_EVAL o GEMINI_KEY_FILE")
	}
	defer f.Close()
	l := &Library{Ask: g.Ask}
	var known []sample
	example := map[string]bool{}
	for _, ex := range genreExamples {
		for _, a := range ex {
			example[fold(a)] = true
		}
	}
	for sc := bufio.NewScanner(f); sc.Scan(); {
		// Los ejemplos de las instrucciones no cuentan: el modelo ya vio la respuesta.
		if c := strings.Split(sc.Text(), "\t"); len(c) == 4 && baseGenre[fold(c[0])] != "" && !example[fold(c[0])] {
			known = append(known, sample{c[0], c[2], c[3]})
		}
	}
	same, fam, unknown := 0, 0, 0
	for i := 0; i < len(known); i += 40 {
		batch := known[i:min(i+40, len(known))]
		got, err := l.askGenres(context.Background(), false, batch)
		if err != nil {
			t.Fatal(err)
		}
		for j, s := range batch {
			want := baseGenre[fold(s.Artist)]
			switch {
			case got[j] == want:
				same++
				fam++
			case got[j] == "":
				unknown++
			case strings.Split(got[j], "/")[0] == strings.Split(want, "/")[0]:
				fam++
			default:
				t.Logf("otra familia: %s [%s / %s]: %s → %s", s.Artist, s.Album, s.Title, want, got[j])
			}
		}
	}
	t.Logf("%d artistas: mismo género %d (%d%%), misma familia %d (%d%%), desconocido %d", len(known), same, 100*same/len(known), fam, 100*fam/len(known), unknown)
}
