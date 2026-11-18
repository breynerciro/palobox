package media

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCoverSource(t *testing.T) {
	root := t.TempDir()
	touch := func(p string) {
		t.Helper()
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	touch("Artista/Album1/01.mp3")
	touch("Artista/Album2/01.flac")
	touch("Artista/Album2/Cover.JPG")
	touch("Artista/.oculto/cover.jpg")

	cases := []struct {
		dir       string
		depth     int
		src, kind string
	}{
		{"Artista/Album2", 0, "Artista/Album2/Cover.JPG", "image"}, // la imagen gana al audio
		{"Artista/Album1", 0, "Artista/Album1/01.mp3", "audio"},
		{"Artista", 0, "", "audio"},                      // sin bajar no hay nada
		{"Artista", 1, "Artista/Album1/01.mp3", "audio"}, // primer álbum; ignora .oculto
	}
	for _, c := range cases {
		src, kind := CoverSource(filepath.Join(root, c.dir), IsAudio, c.depth)
		want := ""
		if c.src != "" {
			want = filepath.Join(root, c.src)
		}
		if src != want || kind != c.kind {
			t.Errorf("CoverSource(%s, %d) = %q %q; quería %q %q", c.dir, c.depth, src, kind, want, c.kind)
		}
	}
}

func TestIsAudio(t *testing.T) {
	for name, want := range map[string]bool{"a.MP3": true, "b.opus": true, "c.jpg": false, "flac": false} {
		if IsAudio(name) != want {
			t.Errorf("IsAudio(%q) != %v", name, want)
		}
	}
}
