package module

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExigeToken(t *testing.T) {
	t.Setenv("PRUEBA_URL", "http://spotdl:8801")
	t.Setenv("PRUEBA_TOKEN_FILE", filepath.Join(t.TempDir(), "no-existe"))
	if m := Load("PRUEBA", "x"); m.URL != "" {
		t.Fatal("sin token el módulo debía quedar desactivado")
	}
	f := filepath.Join(t.TempDir(), "token")
	os.WriteFile(f, []byte("  abc\n"), 0o600)
	t.Setenv("PRUEBA_TOKEN_FILE", f)
	if m := Load("PRUEBA", "x"); m.URL != "http://spotdl:8801" || m.Token != "abc" {
		t.Fatalf("módulo con token: %+v", m)
	}
}
