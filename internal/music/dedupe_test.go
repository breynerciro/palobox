package music

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"palobox/internal/store"
)

// biblioteca crea los archivos con la fecha dada; cada prueba llama a Sync (sin ffprobe la duración queda en 0).
func biblioteca(t *testing.T, files map[string]time.Time) *Library {
	t.Helper()
	data := t.TempDir()
	db, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l := &Library{DB: db, Data: data, Sem: make(chan struct{}, 1)}
	for rel, at := range files {
		p := filepath.Join(l.musicRoot(), rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, at, at)
	}
	return l
}

func (l *Library) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := l.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// lista devuelve una columna de texto, unida por "|".
func (l *Library) lista(t *testing.T, q string, args ...any) string {
	t.Helper()
	rows, err := l.DB.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return strings.Join(out, "|")
}

func existe(l *Library, rel string) bool {
	_, err := os.Stat(filepath.Join(l.musicRoot(), rel))
	return err == nil
}

var antes, despues = time.Now().Add(-48 * time.Hour), time.Now().Add(-time.Hour)

func TestSencilloPasaAlAlbum(t *testing.T) {
	const sencillo, delAlbum = "Penyair/Nada/Nada.mp3", "Penyair/La Ley del Todo/Nada.mp3"
	l := biblioteca(t, map[string]time.Time{sencillo: antes, delAlbum: despues, "Penyair/La Ley del Todo/Otra.mp3": despues})
	l.Sync()
	l.exec(t, `UPDATE tracks SET dur = 200 WHERE path = ?`, delAlbum)
	l.exec(t, `UPDATE tracks SET dur = 202.5 WHERE path = ?`, sencillo)
	l.exec(t, `INSERT INTO likes VALUES(1, ?, 1), (2, ?, 1), (2, ?, 2)`, sencillo, sencillo, delAlbum)
	l.exec(t, `INSERT INTO plays VALUES(1, ?, 1)`, sencillo)
	l.exec(t, `INSERT INTO playlist_tracks VALUES(7, ?, 0)`, sencillo)
	l.exec(t, `INSERT INTO saved VALUES(1, 'album', 'Penyair/Nada', 1), (2, 'album', 'Penyair/Nada', 1), (2, 'album', 'Penyair/La Ley del Todo', 1)`)
	l.dedupe()

	if existe(l, sencillo) || existe(l, "Penyair/Nada") || !existe(l, delAlbum) {
		t.Fatal("debía quedar solo la versión del álbum y desaparecer la carpeta del sencillo")
	}
	if got := l.lista(t, `SELECT user_id || ' ' || path FROM likes ORDER BY user_id`); got != "1 "+delAlbum+"|2 "+delAlbum {
		t.Fatalf("me gusta: %s", got)
	}
	if got := l.lista(t, `SELECT path FROM plays UNION ALL SELECT path FROM playlist_tracks`); got != delAlbum+"|"+delAlbum {
		t.Fatalf("escuchas y playlist: %s", got)
	}
	if got := l.lista(t, `SELECT user_id || ' ' || path FROM saved ORDER BY user_id`); got != "1 Penyair/La Ley del Todo|2 Penyair/La Ley del Todo" {
		t.Fatalf("álbumes guardados: %s", got)
	}
	if got := l.lista(t, `SELECT path FROM tracks ORDER BY path`); got != delAlbum+"|Penyair/La Ley del Todo/Otra.mp3" {
		t.Fatalf("índice: %s", got)
	}
}

func TestAlbumPartidoSeUne(t *testing.T) {
	l := biblioteca(t, map[string]time.Time{
		"Penyair/La Ley del Todo/A.mp3": despues,
		"Penyair/La Ley del Todo/B.mp3": despues,
		"Penyair/La Ley del Todo/D.mp3": despues, // 3 pistas contra 2: gana aunque sea más nueva
		"Penyair/La Ley Del Todo/A.mp3": antes,   // mismo nombre en el destino: es la misma canción
		"Penyair/La Ley Del Todo/C.mp3": antes,
		// Empate en pistas: gana la carpeta de archivos más antiguos.
		"Penyair/Antes del alba/X.mp3": despues,
		"Penyair/Antes del Alba/Y.mp3": antes,
	})
	l.exec(t, `INSERT INTO likes VALUES(1, 'Penyair/La Ley Del Todo/C.mp3', 1), (1, 'Penyair/La Ley Del Todo/A.mp3', 1)`)
	l.exec(t, `INSERT INTO saved VALUES(1, 'album', 'Penyair/La Ley Del Todo', 1)`)
	l.Sync() // la unión corre dentro del recorrido

	want := []string{"Penyair/Antes del Alba/X.mp3", "Penyair/Antes del Alba/Y.mp3",
		"Penyair/La Ley del Todo/A.mp3", "Penyair/La Ley del Todo/B.mp3", "Penyair/La Ley del Todo/C.mp3", "Penyair/La Ley del Todo/D.mp3"}
	if got := l.lista(t, `SELECT path FROM tracks ORDER BY path`); got != strings.Join(want, "|") {
		t.Fatalf("índice: %s", got)
	}
	var disco []string
	filepath.WalkDir(l.musicRoot(), func(p string, d os.DirEntry, err error) error {
		if !d.IsDir() {
			rel, _ := filepath.Rel(l.musicRoot(), p)
			disco = append(disco, filepath.ToSlash(rel))
		}
		return nil
	})
	if !reflect.DeepEqual(disco, want) || existe(l, "Penyair/La Ley Del Todo") || existe(l, "Penyair/Antes del alba") {
		t.Fatalf("disco: %v", disco)
	}
	if got := l.lista(t, `SELECT DISTINCT album_dir || ' ' || album FROM tracks WHERE path LIKE '%/C.mp3' OR path LIKE '%/X.mp3' ORDER BY 1`); got != "Penyair/Antes del Alba Antes del Alba|Penyair/La Ley del Todo La Ley del Todo" {
		t.Fatalf("álbum de las pistas movidas: %s", got)
	}
	if got := l.lista(t, `SELECT path FROM likes ORDER BY path`); got != "Penyair/La Ley del Todo/A.mp3|Penyair/La Ley del Todo/C.mp3" {
		t.Fatalf("me gusta: %s", got)
	}
	if got := l.lista(t, `SELECT path FROM saved`); got != "Penyair/La Ley del Todo" {
		t.Fatalf("álbum guardado: %s", got)
	}
}

func TestNoTocaSinCertezas(t *testing.T) {
	files := map[string]time.Time{
		"Alcolirykoz/Uno/Intro.mp3":  antes,
		"Alcolirykoz/Dos/Intro.mp3":  antes,
		"Penyair/Nada/Nada.mp3":      antes,
		"Penyair/La Ley/Nada.mp3":    antes,
		"Penyair/La Ley/Otra.mp3":    antes,
		"Otro artista/Nada/Nada.mp3": antes, // mismo título, otro artista
	}
	l := biblioteca(t, files)
	l.Sync()
	l.exec(t, `UPDATE tracks SET dur = 60 WHERE path = 'Alcolirykoz/Uno/Intro.mp3'`)
	l.exec(t, `UPDATE tracks SET dur = 63.5 WHERE path = 'Alcolirykoz/Dos/Intro.mp3'`)
	l.exec(t, `UPDATE tracks SET dur = 200 WHERE path IN ('Penyair/La Ley/Nada.mp3', 'Otro artista/Nada/Nada.mp3')`) // el sencillo sigue en 0
	l.dedupe()
	for rel := range files {
		if !existe(l, rel) {
			t.Fatalf("no debía borrarse %s", rel)
		}
	}
	if got := l.lista(t, `SELECT COUNT(*) FROM tracks`); got != "6" {
		t.Fatalf("índice: %s pistas", got)
	}
}
