package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Página de descarga: pública, muestra primero lo del dispositivo, y /apk entrega la APK publicada (o 404).
func TestLanding(t *testing.T) {
	a, _, _, _ := musicSetup(t)
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	get := func(path, ua string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("User-Agent", ua)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b := new(strings.Builder)
		buf := make([]byte, 1<<16)
		for n, _ := res.Body.Read(buf); n > 0; n, _ = res.Body.Read(buf) {
			b.Write(buf[:n])
		}
		return res, b.String()
	}
	first := func(body string) string { // la primera sección es la del dispositivo
		i := strings.Index(body, "<section")
		return body[i : i+strings.Index(body[i:], "</section>")]
	}
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Linux; Android 12; TECNO CH7n) Chrome/140 Mobile":             "Descargar PaloBox para Android",
		"Mozilla/5.0 (Linux; Android 14; onn. Full HD Streaming Device; Android TV)": "Downloader",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1":        "Agregar a inicio",
		"Mozilla/5.0 (X11; Linux x86_64) Chrome/140":                                 "Abrir PaloBox",
	} {
		res, body := get("/app", ua) // sin sesión
		if res.StatusCode != 200 || !strings.Contains(first(body), want) {
			t.Errorf("%s: %d, primera sección sin %q", ua, res.StatusCode, want)
		}
	}
	apkFile = filepath.Join(t.TempDir(), "palobox.apk")
	if res, _ := get("/apk", ""); res.StatusCode != 404 {
		t.Fatalf("sin APK publicada: %d", res.StatusCode)
	}
	os.WriteFile(apkFile, []byte("PK apk falsa"), 0o644)
	os.WriteFile(apkFile+".version", []byte("7\n"), 0o644)
	res, body := get("/apk", "")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/vnd.android.package-archive" || body != "PK apk falsa" {
		t.Fatalf("/apk: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if _, v := get("/app/version", ""); v != "7" {
		t.Fatalf("versión: %q", v)
	}
}
