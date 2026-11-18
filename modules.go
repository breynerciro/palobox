package main

// Módulos: servicios internos en contenedores aparte (hoy solo spotDL) que la app usa
// como proxy. La app es su única puerta: autentica al usuario y reenvía la petición con
// el token secreto del módulo y la identidad del usuario ya verificada.
//
// Agregar un módulo: NOMBRE_URL y NOMBRE_TOKEN_FILE en la app, un secreto propio en
// compose.yml montado en ambos contenedores, y en el módulo exigir
// "Authorization: Bearer <token>" (comparación en tiempo constante) en toda petición.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

var moduleClient = &http.Client{Timeout: 20 * time.Second}

type module struct {
	label string // para los mensajes: "El servicio de descargas de Spotify"
	url   string // vacío = módulo desactivado
	token string
}

// loadModule lee PREFIJO_URL y el token de PREFIJO_TOKEN_FILE. Sin token el módulo
// queda desactivado: nunca se habla con él sin autenticación.
func loadModule(prefix, label string) module {
	m := module{label: label, url: os.Getenv(prefix + "_URL")}
	if m.url == "" {
		return m
	}
	b, err := os.ReadFile(os.Getenv(prefix + "_TOKEN_FILE"))
	if m.token = strings.TrimSpace(string(b)); err != nil || m.token == "" {
		log.Printf("%s desactivado: falta su token (%s_TOKEN_FILE)", label, prefix)
		m.url = ""
	}
	return m
}

// do habla con el módulo en nombre de u y traduce sus errores a mensajes de la app.
func (m module) do(u user, admin bool, method, path string, in, out any) error {
	if m.url == "" {
		return httpErr{503, m.label + " no está configurado en el servidor."}
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(m.url, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("X-PaloBox-User", u.name)
	if admin {
		req.Header.Set("X-PaloBox-Admin", "1")
	}
	res, err := moduleClient.Do(req)
	if err != nil {
		return httpErr{503, m.label + " no responde."}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode == 401 {
		// Un 401 tal cual haría creer a la interfaz que se cerró la sesión del usuario.
		log.Printf("%s rechazó el token de la app", m.label)
		return httpErr{503, m.label + " no acepta la conexión de la app."}
	}
	if res.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = m.label + " rechazó la petición."
		}
		return httpErr{res.StatusCode, e.Error}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
