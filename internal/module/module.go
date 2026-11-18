// Package module habla con servicios internos en contenedores aparte (hoy solo spotDL)
// que la app usa como proxy. La app es su única puerta: autentica al usuario y reenvía la
// petición con el token secreto del módulo y la identidad del usuario ya verificada.
//
// Agregar un módulo: NOMBRE_URL y NOMBRE_TOKEN_FILE en la app, un secreto propio en
// compose.yml montado en ambos contenedores, y en el módulo exigir
// "Authorization: Bearer <token>" (comparación en tiempo constante) en toda petición.
package module

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"palobox/internal/httpx"
)

var client = &http.Client{Timeout: 20 * time.Second}

type Module struct {
	Label string // para los mensajes: "El servicio de descargas de Spotify"
	URL   string // vacío = módulo desactivado
	Token string
}

// Load lee PREFIJO_URL y el token de PREFIJO_TOKEN_FILE. Sin token el módulo
// queda desactivado: nunca se habla con él sin autenticación.
func Load(prefix, label string) Module {
	m := Module{Label: label, URL: os.Getenv(prefix + "_URL")}
	if m.URL == "" {
		return m
	}
	b, err := os.ReadFile(os.Getenv(prefix + "_TOKEN_FILE"))
	if m.Token = strings.TrimSpace(string(b)); err != nil || m.Token == "" {
		log.Printf("%s desactivado: falta su token (%s_TOKEN_FILE)", label, prefix)
		m.URL = ""
	}
	return m
}

// Do habla con el módulo en nombre de u y traduce sus errores a mensajes de la app.
func (m Module) Do(u httpx.User, method, path string, in, out any) error {
	if m.URL == "" {
		return httpx.Error(503, m.Label+" no está configurado en el servidor.")
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(m.URL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("X-PaloBox-User", u.Name)
	if u.Admin {
		req.Header.Set("X-PaloBox-Admin", "1")
	}
	res, err := client.Do(req)
	if err != nil {
		return httpx.Error(503, m.Label+" no responde.")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode == 401 {
		// Un 401 tal cual haría creer a la interfaz que se cerró la sesión del usuario.
		log.Printf("%s rechazó el token de la app", m.Label)
		return httpx.Error(503, m.Label+" no acepta la conexión de la app.")
	}
	if res.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = m.Label + " rechazó la petición."
		}
		return httpx.Error(res.StatusCode, e.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
