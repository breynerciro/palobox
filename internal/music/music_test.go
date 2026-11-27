package music

import (
	"bytes"
	"encoding/binary"
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
)

func TestEtiquetas(t *testing.T) {
	tr := fromPath("Artista/Álbum/07 - nombre de archivo.mp3")
	if tr.Title != "nombre de archivo" || tr.Num != 7 || tr.Album != "Álbum" || tr.Artist != "Artista" || tr.AlbumPath != "Artista/Álbum" {
		t.Fatalf("respaldo por ruta: %+v", tr)
	}
	applyTags(&tr, []byte(`{"streams":[{"tags":{"TITLE":"De la pista","ARTIST":"Otra"}}],
		"format":{"duration":"187.52","tags":{"title":" Título real ","album":"Disco","track":"3/12","disc":"2/2","date":"2019-05-01","GENRE":"Flamenco"}}}`))
	if tr.Title != "Título real" || tr.Artist != "Otra" || tr.Album != "Disco" || tr.Num != 3 || tr.disc != 2 || tr.Year != 2019 || tr.Dur != 187.52 || tr.genre != "Flamenco" {
		t.Fatalf("etiquetas: %+v", tr)
	}
	// ffprobe ausente o archivo corrupto: se conserva lo deducido de la ruta.
	tr = fromPath("suelta.mp3")
	applyTags(&tr, nil)
	if tr.Title != "suelta" || tr.Album != "Sin álbum" || tr.AlbumPath != "" || tr.ArtistPath != "" {
		t.Fatalf("pista suelta: %+v", tr)
	}
}

// Una pista de la biblioteca (M4A del tipo del flujo) sale tal cual desde cualquier segundo, sin ffmpeg ni cupo.
func TestFlujoDirecto(t *testing.T) {
	l := &Library{Data: t.TempDir()}
	os.MkdirAll(l.musicRoot(), 0o755)
	os.WriteFile(filepath.Join(l.musicRoot(), "a.m4a"), fakeM4A('a', 10, 377), 0o644)
	// 10 cuadros de 384 bytes (con cabecera) duran 10 × 1024 / 44100 s; el segundo 0,13 cae dentro del sexto cuadro.
	src, rate, err := l.OpenStream("a.m4a", 0.24, 0.13)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	b, _ := io.ReadAll(src)
	if want := 384 * StreamHz / 1024.0; rate != want || len(b) != 5*384 || b[0] != 0xFF || b[1] != 0xF1 || b[7] != 'a' {
		t.Fatalf("rate=%v (se esperaba %v) bytes=%d", rate, want, len(b))
	}
	if len(convSlots) != 0 {
		t.Fatal("el paso directo no debe tocar el cupo de conversiones")
	}
	// Más allá del final no hay nada que mandar, pero tampoco es un error.
	end, _, err := l.OpenStream("a.m4a", 0.24, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer end.Close()
	if b, _ := io.ReadAll(end); len(b) != 0 {
		t.Fatalf("después del final llegaron %d bytes", len(b))
	}
}

// fakeM4A arma un M4A mínimo: n cuadros de size bytes rellenos con fill y el índice que los describe como AAC-LC
// a 44,1 kHz en estéreo (el mismo de jam_test.go, en el paquete principal).
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

// Si ffprobe no alcanza a correr, la pista no entra al índice y se reintenta; si corre y falla (archivo corrupto),
// entra con lo de la ruta.
func TestSyncSinFfprobe(t *testing.T) {
	l := biblioteca(t, map[string]time.Time{"Artista/Disco/01 - Uno.mp3": time.Now()})
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	ffprobe := filepath.Join(bin, "ffprobe")
	os.WriteFile(ffprobe, []byte("no es un programa"), 0o755) // no arranca: exec format error
	l.Sync()
	if n := l.lista(t, `SELECT path FROM tracks`); n != "" {
		t.Fatalf("sin poder leerla no debe quedar indexada: %q", n)
	}
	os.WriteFile(ffprobe, []byte("#!/bin/sh\nexit 1\n"), 0o755)
	l.Reindex()
	if n := l.lista(t, `SELECT title FROM tracks`); n != "Uno" {
		t.Fatalf("un archivo corrupto entra con lo de la ruta: %q", n)
	}
}

// Si la importación de una playlist de Spotify falla a la mitad no queda nada, y el siguiente intento la crea completa.
func TestImportPlaylistAMedias(t *testing.T) {
	l := biblioteca(t, nil)
	l.exec(t, `INSERT INTO users(name, pass) VALUES('ana', x'00')`)
	l.exec(t, `CREATE TRIGGER falla BEFORE INSERT ON playlist_tracks WHEN NEW.path = 'b.mp3' BEGIN SELECT RAISE(ABORT, 'falla'); END`)
	l.ImportPlaylist("ana", "trabajo", "Lista", []string{"a.mp3", "b.mp3", "c.mp3"})
	if n := l.lista(t, `SELECT job FROM spotdl_done`) + l.lista(t, `SELECT name FROM playlists`) + l.lista(t, `SELECT path FROM playlist_tracks`); n != "" {
		t.Fatalf("una importación fallida no debe dejar rastro: %q", n)
	}
	l.exec(t, `DROP TRIGGER falla`)
	l.ImportPlaylist("ana", "trabajo", "Lista", []string{"a.mp3", "b.mp3", "c.mp3"})
	if n := l.lista(t, `SELECT path FROM playlist_tracks ORDER BY pos`); n != "a.mp3|b.mp3|c.mp3" {
		t.Fatalf("el reintento debe crear la playlist completa: %q", n)
	}
	if n := l.lista(t, `SELECT job FROM spotdl_done`); n != "trabajo" {
		t.Fatalf("el trabajo debe quedar marcado: %q", n)
	}
}

// Si la limpieza de duplicados ya borró la copia que anuncia spotDL, a la playlist entra la que quedó.
func TestImportPlaylistCopiaBorrada(t *testing.T) {
	const borrada, queda = "Feid/19/100%_Sígueme - Remix.mp3", "Feid/Sígueme (Remix)/100%_Sígueme - Remix.mp3"
	l := biblioteca(t, map[string]time.Time{borrada: antes, queda: antes, "Feid/Otro/100%XSígueme - Remix.mp3": antes, "Otro/19/100%_Sígueme - Remix.mp3": antes})
	l.Sync()
	l.exec(t, `INSERT INTO users(name, pass) VALUES('ana', x'00')`)
	l.exec(t, `DELETE FROM tracks WHERE path = ?`, borrada)
	// Aún en disco es una pista sin indexar, no una copia borrada: puede ser otra canción con el mismo nombre.
	if g := l.twin(l.DB, borrada); g != "" {
		t.Fatalf("el archivo sigue en disco y se cambió por %q", g)
	}
	os.Remove(filepath.Join(l.musicRoot(), borrada))
	l.ImportPlaylist("ana", "trabajo", "Lista", []string{borrada, "Feid/sin indexar.mp3"})
	// La que no tiene gemela entra tal cual: puede no estar indexada todavía.
	if n := l.lista(t, `SELECT path FROM playlist_tracks ORDER BY pos`); n != queda+"|Feid/sin indexar.mp3" {
		t.Fatalf("debía entrar la copia que quedó: %q", n)
	}
	// Lo ya dañado se repara en la limpieza, sin repetir la canción en la playlist que ya tiene la gemela.
	l.exec(t, `INSERT INTO playlist_tracks VALUES(8, ?, 0), (9, ?, 0), (9, ?, 1)`, borrada, borrada, queda)
	l.dedupe()
	l.dedupe()
	if n := l.lista(t, `SELECT playlist_id || ':' || path FROM playlist_tracks WHERE playlist_id > 7 ORDER BY playlist_id, pos`); n != "8:"+queda+"|9:"+borrada+"|9:"+queda {
		t.Fatalf("reparación: %q", n)
	}
}

// La misma canción sonando en dos dispositivos de la cuenta cuenta una sola escucha.
func TestEscuchaRepetida(t *testing.T) {
	l := biblioteca(t, map[string]time.Time{"a.mp3": antes})
	oye := func(id int64) {
		t.Helper()
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"path":"a.mp3"}`))
		if err := l.Played(httptest.NewRecorder(), r, httpx.User{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	oye(1)
	oye(1)
	oye(2)
	if n := l.lista(t, `SELECT user_id FROM plays ORDER BY user_id`); n != "1|2" {
		t.Fatalf("escuchas: %q", n)
	}
	l.exec(t, `UPDATE plays SET at = at - 31000`)
	oye(1)
	if n := l.lista(t, `SELECT COUNT(*) FROM plays WHERE user_id = 1`); n != "2" {
		t.Fatalf("pasados 30 s vuelve a contar: %q", n)
	}
}

// El orden de una playlist de 1.500 canciones no cabe en los 64 KB de ReadJSON.
func TestOrdenPlaylistGrande(t *testing.T) {
	l := biblioteca(t, nil)
	l.exec(t, `INSERT INTO playlists VALUES(1, 1, 'Grande', 1)`)
	paths := make([]string, 1500)
	tx, _ := l.DB.Begin()
	for i := range paths {
		paths[i] = fmt.Sprintf("Artista de nombre largo/Álbum de nombre largo/%04d - Canción.mp3", i)
		tx.Exec(`INSERT INTO playlist_tracks VALUES(1, ?, ?)`, paths[i], len(paths)-i)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"paths": paths})
	r := httptest.NewRequest("PUT", "/", bytes.NewReader(body))
	r.SetPathValue("id", "1")
	if err := l.PlaylistOrder(httptest.NewRecorder(), r, httpx.User{ID: 1}); err != nil {
		t.Fatalf("%d bytes: %v", len(body), err)
	}
	if n := l.lista(t, `SELECT path FROM playlist_tracks ORDER BY pos LIMIT 2`); n != paths[0]+"|"+paths[1] {
		t.Fatalf("orden: %q", n)
	}
}
