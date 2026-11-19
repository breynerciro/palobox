package music

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// Con el cupo de conversiones lleno, una pista a mitad de camino sale tal cual desde un cuadro entero, sin ffmpeg.
func TestFlujoSinCupo(t *testing.T) {
	frame := make([]byte, 384) // MPEG-1 capa III, 128 kbps, 48 kHz, estéreo
	copy(frame, []byte{0xFF, 0xFB, 0x94, 0x00})
	l := &Library{Data: t.TempDir()}
	os.MkdirAll(l.musicRoot(), 0o755)
	os.WriteFile(filepath.Join(l.musicRoot(), "a.mp3"), bytes.Repeat(frame, 10), 0o644)
	for len(convSlots) < cap(convSlots) {
		convSlots <- struct{}{}
	}
	defer func() {
		for len(convSlots) > 0 {
			<-convSlots
		}
	}()
	// 10 cuadros en 0,24 s son 16000 bytes por segundo; el segundo 0,13 cae dentro del sexto cuadro.
	src, rate, err := l.OpenStream("a.mp3", 0.24, 0.13)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	b, _ := io.ReadAll(src)
	if rate != 16000 || len(b) != 4*384 || b[0] != 0xFF {
		t.Fatalf("rate=%v bytes=%d", rate, len(b))
	}
	if len(convSlots) != cap(convSlots) {
		t.Fatal("el paso directo no debe tocar el cupo")
	}
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
