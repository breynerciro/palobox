// Package gemini hace preguntas a Gemini con respuesta JSON de esquema cerrado. Pensado para el plan
// gratuito: reparte las solicitudes entre modelos con cuota aparte, pasa al siguiente si uno está sin
// cuota, saturado o colgado, y lleva un tope diario propio para no agotar la cuota de Google.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable: ningún modelo respondió (cuota, saturación, red) o se llegó al tope diario. Quien llama
// debe seguir sin Gemini.
var ErrUnavailable = errors.New("Gemini no está disponible")

const apiURL = "https://generativelanguage.googleapis.com/v1beta/models"

type Client struct {
	Key string
	URL string // vacío = la API de Google; las pruebas ponen un servidor falso
	// Livianos, cada uno con cuota gratuita aparte: las solicitudes se alternan entre ellos.
	Models []string
	// Más capaz y escaso (20 solicitudes al día en el plan gratuito): solo para Ask(hard). Si falla siguen los livianos.
	Hard    []string
	PerDay  int           // tope de solicitudes al día entre todas las cuentas; 0 = sin tope
	Timeout time.Duration // por modelo

	mu   sync.Mutex
	day  string
	used int
	turn int
}

// Load arma el cliente con la llave de GEMINI_KEY_FILE; nil si no hay llave (las funciones con IA quedan apagadas).
// shortcut: alias «latest» de Flash-Lite y un segundo Flash-Lite, los únicos estables en el plan gratuito
// (nov-2026: 3.8 se cuelga, 3.6 responde 503); GEMINI_MODELS y GEMINI_HARD_MODELS los cambian sin recompilar.
func Load() *Client {
	b, _ := os.ReadFile(os.Getenv("GEMINI_KEY_FILE"))
	key := strings.TrimSpace(string(b))
	if key == "" {
		return nil
	}
	c := &Client{Key: key, PerDay: 300, Timeout: 25 * time.Second,
		Models: list("GEMINI_MODELS", "gemini-flash-lite-latest,gemini-3.1-flash-lite"),
		Hard:   list("GEMINI_HARD_MODELS", "gemini-3-flash-preview")}
	if v := os.Getenv("GEMINI_PER_DAY"); v != "" {
		if _, err := fmt.Sscan(v, &c.PerDay); err != nil {
			log.Fatal("GEMINI_PER_DAY no es un número: ", v)
		}
	}
	return c
}

func list(env, def string) []string {
	v, ok := os.LookupEnv(env)
	if !ok {
		v = def
	}
	out := []string{}
	for _, m := range strings.Split(v, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// take aparta una solicitud del tope diario; false = agotado.
func (c *Client) take() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	// shortcut: el contador vive en memoria y un reinicio lo pone en cero; si el tope debe ser exacto, a la base.
	if today := time.Now().Format(time.DateOnly); today != c.day {
		c.day, c.used = today, 0
	}
	if c.PerDay > 0 && c.used >= c.PerDay {
		return false
	}
	c.used++
	return true
}

// order devuelve los modelos de una pregunta: los capaces si hard, y los livianos empezando por el que sigue
// en el turno, para repartir el límite por minuto entre sus cuotas.
func (c *Client) order(hard bool) (models []string, nHard int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hard {
		models = append(models, c.Hard...)
	}
	nHard = len(models)
	for i := range c.Models {
		models = append(models, c.Models[(c.turn+i)%len(c.Models)])
	}
	c.turn++
	return models, nHard
}

// Ask pregunta y decodifica en out la respuesta, que cumple schema (formato de responseSchema de Gemini).
// system son las instrucciones fijas; prompt, el caso. hard prueba primero los modelos más capaces.
func (c *Client) Ask(ctx context.Context, hard bool, system, prompt string, schema map[string]any, out any) error {
	models, nHard := c.order(hard)
	var last error
	for i, m := range models {
		if !c.take() {
			return fmt.Errorf("%w: tope diario de %d solicitudes", ErrUnavailable, c.PerDay)
		}
		cfg := map[string]any{"temperature": 0, "maxOutputTokens": 2000, "responseMimeType": "application/json", "responseSchema": schema}
		if i < nHard {
			// Los que razonan gastan parte del tope de salida antes de escribir el JSON.
			cfg["maxOutputTokens"], cfg["thinkingConfig"] = 4000, map[string]any{"thinkingLevel": "low"}
		}
		text, status, err := c.call(ctx, m, map[string]any{
			"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": system}}},
			"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}},
			"generationConfig":  cfg,
		})
		if err == nil {
			if err = json.Unmarshal([]byte(text), out); err == nil {
				return nil
			}
			err = fmt.Errorf("respuesta que no es el JSON pedido: %.200s", text)
		}
		last = fmt.Errorf("gemini %s: %w", m, err)
		log.Print(last)
		// Sin cuota (429), saturado (503), colgado o sin red: sigue el otro modelo. Un modelo escaso pasa con
		// cualquier fallo; en los livianos, otro error (llave mala, petición inválida) fallaría igual en todos.
		if i >= nHard && status != 0 && status != 429 && status != 503 && status != 200 {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, last)
}

// call hace una solicitud a un modelo y devuelve el texto de la respuesta. status 0 = no hubo respuesta HTTP.
func (c *Client) call(ctx context.Context, model string, body any) (string, int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", 0, err
	}
	url := c.URL
	if url == "" {
		url = apiURL
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url+"/"+model+":generateContent", bytes.NewReader(b))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.Key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", 0, err
	}
	if res.StatusCode != 200 {
		return "", res.StatusCode, fmt.Errorf("%d: %.300s", res.StatusCode, strings.Join(strings.Fields(string(raw)), " "))
	}
	var d struct {
		Candidates []struct {
			Content struct {
				Parts []struct{ Text string }
			}
		}
		UsageMetadata struct{ PromptTokenCount, CandidatesTokenCount, ThoughtsTokenCount int }
	}
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Candidates) == 0 {
		return "", 200, fmt.Errorf("sin respuesta: %.300s", raw)
	}
	text := ""
	for _, p := range d.Candidates[0].Content.Parts {
		text += p.Text
	}
	u := d.UsageMetadata
	log.Printf("gemini %s: %d tokens de entrada, %d de salida, %d de razonamiento", model, u.PromptTokenCount, u.CandidatesTokenCount, u.ThoughtsTokenCount)
	return text, 200, nil
}
