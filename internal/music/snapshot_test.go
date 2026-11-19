package music

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"palobox/internal/httpx"
	"palobox/internal/store"
)

// sintetica llena el índice con n pistas inventadas, como la biblioteca real: de cada 3 canciones,
// 2 son un álbum de una sola canción (playlists de Spotify) y la otra va en un álbum de 10.
// Ana (id 1) tiene escuchas, guardados, me gusta y una playlist. fresh() no recorre el disco.
func sintetica(tb testing.TB, n int) (*Library, httpx.User) {
	tb.Helper()
	data := tb.TempDir()
	db, err := store.Open(data)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { db.Close() })
	l := &Library{DB: db, Data: data, Sem: make(chan struct{}, 1), idxAt: time.Now().Add(time.Hour)}
	words := strings.Fields("amor noche ciudad Alcolirykoz fuego corazón calle luna sueño camino vida canción mar tiempo barrio flor sol lluvia")
	tx, _ := db.Begin()
	for i := range n {
		ar := fmt.Sprintf("Artista %d %s", i%2000, words[i%len(words)])
		title := fmt.Sprintf("%s %s %d", words[i*7%len(words)], words[i*13%len(words)], i)
		alb := title
		if i%3 == 2 {
			alb = fmt.Sprintf("Disco %s %d", words[i%5], i/30)
		}
		p := ar + "/" + alb + "/" + title + ".mp3"
		if _, err := tx.Exec(`INSERT INTO tracks VALUES(?, 1, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, '', '')`,
			p, int64(i), title, ar, alb, ar+"/"+alb, ar, 1990+i%30, i%10+1, 180+float64(i%120)); err != nil {
			tb.Fatal(err)
		}
		if i%50 == 0 {
			tx.Exec(`INSERT INTO plays VALUES(1, ?, ?)`, p, int64(i))
			tx.Exec(`INSERT INTO likes VALUES(1, ?, ?)`, p, int64(i))
			tx.Exec(`INSERT INTO saved VALUES(1, 'album', ?, ?)`, ar+"/"+alb, int64(i))
			tx.Exec(`INSERT INTO saved VALUES(1, 'artist', ?, ?)`, ar, int64(i))
			tx.Exec(`INSERT INTO playlist_tracks VALUES(1, ?, ?)`, p, i)
		}
	}
	tx.Exec(`INSERT INTO playlists VALUES(1, 1, 'Carretera', 1)`)
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	return l, httpx.User{ID: 1, Name: "ana"}
}

// pedir llama al handler como lo haría el enrutador y devuelve el cuerpo.
func pedir(tb testing.TB, h httpx.Handler, url string, u httpx.User) string {
	w := httptest.NewRecorder()
	if err := h(w, httptest.NewRequest("GET", url, nil), u); err != nil {
		tb.Fatal(err)
	}
	return w.Body.String()
}

// completa arma álbumes y artistas como web/music.js: el catálogo de /api/music/catalog más lo de "mine".
func completa(tb testing.TB, l *Library, u httpx.User) string {
	tb.Helper()
	var lib struct {
		Catalog string
		Mine    struct{ Albums, Artists map[string]mine }
	}
	json.Unmarshal([]byte(pedir(tb, l.Lib, "/api/music?catalog=1", u)), &lib)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/music/catalog?v="+lib.Catalog, nil)
	r.Header.Set("Accept-Encoding", "gzip")
	if err := l.Catalog(w, r, u); err != nil || w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" {
		tb.Fatalf("catálogo: %v %d %v", err, w.Code, w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		tb.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	var cat struct {
		Albums  []album
		Artists []artist
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		tb.Fatal(err)
	}
	for i, al := range cat.Albums {
		m := lib.Mine.Albums[al.Path]
		cat.Albums[i].Played, cat.Albums[i].Plays, cat.Albums[i].Saved = m.Played, m.Plays, m.Saved
	}
	for i, ar := range cat.Artists {
		m := lib.Mine.Artists[ar.Path]
		cat.Artists[i].Played, cat.Artists[i].Plays, cat.Artists[i].Saved = m.Played, m.Plays, m.Saved
	}
	b, _ := json.Marshal(cat)
	return string(b)
}

// Catálogo más lo del usuario da exactamente los álbumes y artistas que /api/music mandaba completos.
func TestCatalogoMasUsuario(t *testing.T) {
	l, u := sintetica(t, 3000)
	_, albums, artists, err := l.library(u)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(map[string]any{"Albums": albums, "Artists": artists})
	if got := completa(t, l, u); got != string(want) {
		t.Fatal("la combinación no coincide con la respuesta completa")
	}
	if !bytes.Contains(want, []byte(`"saved":`)) || !strings.Contains(pedir(t, l.Lib, "/api/music", u), `"plays":1`) {
		t.Fatal("la prueba no tiene datos del usuario")
	}
	// Una interfaz anterior, sin ?catalog, sigue recibiendo la biblioteca completa en /api/music.
	var vieja struct{ Albums, Artists json.RawMessage }
	json.Unmarshal([]byte(pedir(t, l.Lib, "/api/music", u)), &vieja)
	if got, _ := json.Marshal(map[string]any{"Albums": vieja.Albums, "Artists": vieja.Artists}); string(got) != string(want) {
		t.Fatal("la respuesta completa para interfaces anteriores cambió")
	}
	// Sin gzip sale el mismo JSON, y una versión vieja redirige a la vigente sin quedar en caché.
	s, _ := l.snap()
	w := httptest.NewRecorder()
	l.Catalog(w, httptest.NewRequest("GET", "/api/music/catalog?v="+s.gen, nil), u)
	if w.Header().Get("Content-Encoding") != "" || !strings.HasPrefix(w.Body.String(), `{"albums":[`) || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("sin gzip: %v %.40s", w.Header(), w.Body)
	}
	w = httptest.NewRecorder()
	l.Catalog(w, httptest.NewRequest("GET", "/api/music/catalog?v=vieja", nil), u)
	if w.Code != 302 || w.Header().Get("Location") != "/api/music/catalog?v="+s.gen || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("versión vieja: %d %v", w.Code, w.Header())
	}
}

func BenchmarkCatalog(b *testing.B) {
	l, u := sintetica(b, 15000)
	s, _ := l.snap()
	r := httptest.NewRequest("GET", "/api/music/catalog?v="+s.gen, nil)
	r.Header.Set("Accept-Encoding", "gzip")
	b.ReportAllocs()
	for b.Loop() {
		w := httptest.NewRecorder()
		if err := l.Catalog(w, r, u); err != nil {
			b.Fatal(err)
		}
		b.SetBytes(int64(w.Body.Len()))
	}
}

func BenchmarkLib(b *testing.B) {
	l, u := sintetica(b, 15000)
	pedir(b, l.Lib, "/api/music?catalog=1", u)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		pedir(b, l.Lib, "/api/music?catalog=1", u)
	}
}

func BenchmarkSearch(b *testing.B) {
	l, u := sintetica(b, 15000)
	for _, q := range []string{"alkolirykos", "amor", "corazon calle"} {
		b.Run(q, func(b *testing.B) {
			url := "/api/music/search?q=" + strings.ReplaceAll(q, " ", "%20")
			pedir(b, l.Search, url, u)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				pedir(b, l.Search, url, u)
			}
		})
	}
}

// La instantánea sigue al índice: lo que agrega o borra un recorrido aparece o desaparece en Inicio y en Buscar.
func TestInstantaneaSigueAlIndice(t *testing.T) {
	l := biblioteca(t, map[string]time.Time{"Alcolirykoz/Desde Abajo/Changó.mp3": antes})
	u := httpx.User{ID: 1}
	l.Sync()
	buscar := func() string { return pedir(t, l.Search, "/api/music/search?q=alkolirykos", u) }
	lib, uno := completa(t, l, u), buscar()
	if !strings.Contains(uno, "Changó") || !strings.Contains(lib, "Desde Abajo") {
		t.Fatalf("sin la pista inicial: %s %s", uno, lib)
	}
	nueva := filepath.Join(l.musicRoot(), "Alcolirykoz", "Servicios", "Otra.mp3")
	os.MkdirAll(filepath.Dir(nueva), 0o755)
	os.WriteFile(nueva, []byte("otra"), 0o644)
	l.Reindex()
	if s := buscar(); !strings.Contains(s, "Otra") {
		t.Fatalf("la pista nueva no sale en la búsqueda: %s", s)
	}
	if s := completa(t, l, u); !strings.Contains(s, "Alcolirykoz/Servicios") {
		t.Fatalf("el álbum nuevo no sale en Inicio: %s", s)
	}
	os.Remove(nueva)
	l.Reindex()
	if s := buscar(); s != uno {
		t.Fatalf("la pista borrada sigue en la búsqueda: %s", s)
	}
	if s := completa(t, l, u); s != lib {
		t.Fatalf("Inicio no volvió a como estaba:\n%s\n%s", s, lib)
	}
}
