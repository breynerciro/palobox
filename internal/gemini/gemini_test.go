package gemini

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// falso responde por modelo: un código de error, o el JSON dado como texto de la respuesta.
func falso(t *testing.T, by map[string]any) (*Client, *[]string) {
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ":generateContent")
		calls = append(calls, model)
		if r.Header.Get("x-goog-api-key") != "llave" {
			t.Error("falta la llave")
		}
		switch v := by[model].(type) {
		case int:
			w.WriteHeader(v)
		case string:
			io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":`+v+`}]}}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Key: "llave", URL: srv.URL, Models: []string{"a", "b"}, Hard: []string{"capaz"}, Timeout: time.Second}, &calls
}

func TestAskPasaAlOtroModelo(t *testing.T) {
	c, calls := falso(t, map[string]any{"capaz": 400, "a": 503, "b": `"{\"n\":7}"`})
	var out struct{ N int }
	// El capaz falla con cualquier error y los livianos se turnan: el saturado cede al que responde.
	if err := c.Ask(context.Background(), true, "sistema", "caso", nil, &out); err != nil || out.N != 7 {
		t.Fatalf("Ask: %v %+v", err, out)
	}
	if (*calls)[0] != "capaz" || len(*calls) < 2 || (*calls)[len(*calls)-1] != "b" {
		t.Fatalf("modelos llamados: %v", *calls)
	}
}

func TestAskNoDisponible(t *testing.T) {
	c, calls := falso(t, map[string]any{"a": 429, "b": 429})
	if err := c.Ask(context.Background(), false, "s", "p", nil, &struct{}{}); !errors.Is(err, ErrUnavailable) || len(*calls) != 2 {
		t.Fatalf("sin cuota: %v, llamadas %v", err, *calls)
	}
	// Una llave mala falla igual en todos: no se gasta la segunda solicitud.
	c, calls = falso(t, map[string]any{"a": 400, "b": 400})
	if err := c.Ask(context.Background(), false, "s", "p", nil, &struct{}{}); !errors.Is(err, ErrUnavailable) || len(*calls) != 1 {
		t.Fatalf("error fijo: %v, llamadas %v", err, *calls)
	}
}

func TestAskTopeDiario(t *testing.T) {
	c, calls := falso(t, map[string]any{"a": `"{}"`, "b": `"{}"`})
	c.PerDay = 2
	for i := range 3 {
		err := c.Ask(context.Background(), false, "s", "p", nil, &struct{}{})
		if (i < 2) != (err == nil) {
			t.Fatalf("solicitud %d: %v", i, err)
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("llamadas con tope 2: %v", *calls)
	}
}
