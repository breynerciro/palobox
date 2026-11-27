package music

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// El audio que sale del lector de M4A debe ser, byte por byte, el que saca ffmpeg al pasar el mismo archivo a ADTS
// sin recodificar: así se sabe que el índice se leyó bien y las cabeceras están bien armadas.
func TestM4AComoFfmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("sin ffmpeg")
	}
	dir := t.TempDir()
	for _, c := range []struct {
		name  string
		args  []string
		plain bool
	}{
		{"indice-al-final.m4a", []string{"-ar", "44100", "-ac", "2"}, true},
		{"indice-al-principio.m4a", []string{"-ar", "44100", "-ac", "2", "-movflags", "+faststart"}, true},
		{"otra-frecuencia.m4a", []string{"-ar", "48000", "-ac", "2"}, false},
		{"mono.m4a", []string{"-ar", "44100", "-ac", "1"}, false},
	} {
		src := filepath.Join(dir, c.name)
		args := append([]string{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:a", "aac", "-b:a", "128k"}, c.args...)
		if out, err := exec.Command("ffmpeg", append(args, src)...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", c.name, err, out)
		}
		f, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		m, err := readM4A(f)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if m.plain() != c.plain {
			t.Fatalf("%s: plain = %v, se esperaba %v (%+v)", c.name, m.plain(), c.plain, [3]int{m.objType, m.freqIdx, m.channels})
		}
		got, err := io.ReadAll(&adts{f: f, m: m})
		if err != nil {
			t.Fatal(err)
		}
		want, err := exec.Command("ffmpeg", "-v", "error", "-i", src, "-c:a", "copy", "-f", "adts", "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: %d bytes, ffmpeg da %d; difieren desde el byte %d", c.name, len(got), len(want), firstDiff(got, want))
		}
		// Desde la mitad: los mismos cuadros finales, empezando en una cabecera.
		half := len(m.sizes) / 2
		tail, _ := io.ReadAll(&adts{f: f, m: m, next: half})
		if !bytes.HasSuffix(want, tail) || len(tail) == 0 || tail[0] != 0xFF {
			t.Fatalf("%s: desde el cuadro %d no salen los cuadros finales", c.name, half)
		}
		f.Close()
	}
}

func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// Con M4A_DIR=carpeta revisa archivos reales: cada uno debe ser del tipo del flujo y salir igual que con ffmpeg.
// M4A_MAX limita cuántos (por defecto 20). Sirve para verificar la biblioteca descargada antes de cambiarla.
func TestM4AReales(t *testing.T) {
	dir := os.Getenv("M4A_DIR")
	if dir == "" {
		t.Skip("sin M4A_DIR")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.m4a"))
	limit := 20
	if v := os.Getenv("M4A_MAX"); v != "" {
		fmt.Sscan(v, &limit)
	}
	bad := 0
	for i, src := range files {
		if i >= limit {
			break
		}
		f, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		m, err := readM4A(f)
		if err != nil || !m.plain() {
			t.Errorf("%s: no es del tipo del flujo (%v)", filepath.Base(src), err)
			bad++
			f.Close()
			continue
		}
		got, _ := io.ReadAll(&adts{f: f, m: m})
		f.Close()
		if os.Getenv("M4A_FFMPEG") == "" && i >= 5 { // contra ffmpeg solo los primeros: es lo lento
			continue
		}
		want, err := exec.Command("ffmpeg", "-v", "error", "-i", src, "-c:a", "copy", "-f", "adts", "-").Output()
		// ffmpeg a veces deja por fuera el último cuadro (relleno del final): eso no cuenta como diferencia.
		if err != nil || !bytes.HasPrefix(got, want) || len(got)-len(want) > 800 {
			t.Errorf("%s: %d bytes, ffmpeg da %d (%v)", filepath.Base(src), len(got), len(want), err)
			bad++
		}
	}
	t.Logf("%d archivos revisados, %d con problemas", min(len(files), limit), bad)
}
