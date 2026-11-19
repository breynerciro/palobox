package httpx

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGzip(t *testing.T) {
	h := Gzip(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("falla") {
			Fail(w, Error(400, "mal"))
			return
		}
		WriteJSON(w, 200, map[string]string{"hola": "mundo"})
	})
	pedir := func(url, enc string) *http.Response {
		r := httptest.NewRequest("GET", url, nil)
		if enc != "" {
			r.Header.Set("Accept-Encoding", enc)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w.Result()
	}
	res := pedir("/", "gzip, deflate")
	zr, err := gzip.NewReader(res.Body)
	if err != nil || res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("sin comprimir: %v %v", err, res.Header)
	}
	if b, _ := io.ReadAll(zr); string(b) != `{"hola":"mundo"}`+"\n" {
		t.Fatalf("cuerpo: %q", b)
	}
	// Los errores salen en texto plano, y sin Accept-Encoding nada se comprime.
	for _, c := range [][2]string{{"/?falla", "gzip"}, {"/", ""}} {
		res := pedir(c[0], c[1])
		if b, _ := io.ReadAll(res.Body); res.Header.Get("Content-Encoding") != "" || len(b) == 0 || b[0] == 0x1f {
			t.Fatalf("%s %q no debía comprimirse: %v %q", c[0], c[1], res.Header, b)
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	for h, want := range map[string]bool{
		"":                       false,
		"gzip":                   true,
		"gzip, deflate, br":      true,
		"br;q=1.0, gzip;q=0.8":   true,
		"gzip;q=0, identity":     false,
		"gzip; q=0.000":          false,
		"identity, gzip;q=0.001": true,
		"x-gzip":                 false,
	} {
		r, _ := http.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Encoding", h)
		if got := AcceptsGzip(r); got != want {
			t.Errorf("%q: %v, quería %v", h, got, want)
		}
	}
}
