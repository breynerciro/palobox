package music

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
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
