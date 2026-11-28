// Package push despierta la app de Android con un aviso de Firebase Cloud Messaging (FCM HTTP v1). Con la pantalla
// bloqueada y en silencio Android congela la app y le corta la red; un aviso de prioridad alta la despierta y la deja
// arrancar el servicio de reproducción, y así suena cuando le pasan la música desde otro dispositivo.
package push

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ErrGone: el token ya no sirve (la app se desinstaló o renovó el token). Hay que olvidarlo.
var ErrGone = errors.New("push: token vencido")

// FCM envía avisos con una cuenta de servicio del proyecto de Firebase.
type FCM struct {
	project, email, tokenURL string
	key                      *rsa.PrivateKey
	SendURL                  string // vacío = la de FCM; las pruebas ponen la de un servidor falso
	Client                   *http.Client

	mu     sync.Mutex
	access string
	until  time.Time
}

// Load lee el JSON de la cuenta de servicio de PUSH_KEY_FILE; nil si no hay (la app no se despierta sola).
func Load() (*FCM, error) {
	b, err := os.ReadFile(os.Getenv("PUSH_KEY_FILE"))
	if err != nil {
		return nil, nil
	}
	return Parse(b)
}

// Parse arma el cliente con el JSON de la cuenta de servicio.
func Parse(b []byte) (*FCM, error) {
	var sa struct {
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(b, &sa); err != nil {
		return nil, fmt.Errorf("push: cuenta de servicio ilegible: %w", err)
	}
	blk, _ := pem.Decode([]byte(sa.PrivateKey))
	if blk == nil || sa.ProjectID == "" || sa.ClientEmail == "" || sa.TokenURI == "" {
		return nil, errors.New("push: a la cuenta de servicio le faltan datos")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("push: llave: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("push: la llave no es RSA")
	}
	return &FCM{project: sa.ProjectID, email: sa.ClientEmail, tokenURL: sa.TokenURI, key: rk, Client: &http.Client{Timeout: 10 * time.Second}}, nil
}

// token devuelve un token de acceso de Google, pidiendo otro con un JWT firmado cuando el anterior está por vencer.
func (f *FCM) token() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.access != "" && time.Now().Before(f.until) {
		return f.access, nil
	}
	now := time.Now().Unix()
	enc := base64.RawURLEncoding.EncodeToString
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": f.email, "scope": "https://www.googleapis.com/auth/firebase.messaging",
		"aud": f.tokenURL, "iat": now, "exp": now + 3600})
	msg := enc(head) + "." + enc(claims)
	sum := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	r, err := f.Client.PostForm(f.tokenURL, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {msg + "." + enc(sig)}})
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&t) != nil || t.AccessToken == "" {
		return "", fmt.Errorf("push: Google no dio acceso (%d)", r.StatusCode)
	}
	f.access, f.until = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn-60)*time.Second)
	return f.access, nil
}

// Send manda un aviso solo de datos, de prioridad alta, que vence a los 30 s: un «suena aquí» viejo no sirve.
func (f *FCM) Send(device string, data map[string]string) error {
	access, err := f.token()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"message": map[string]any{"token": device, "data": data,
		"android": map[string]any{"priority": "HIGH", "ttl": "30s"}}})
	u := f.SendURL
	if u == "" {
		u = "https://fcm.googleapis.com/v1/projects/" + f.project + "/messages:send"
	}
	req, _ := http.NewRequest("POST", u, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	r, err := f.Client.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	switch {
	case r.StatusCode == 200:
		return nil
	case r.StatusCode == 404 || strings.Contains(string(b), "UNREGISTERED"):
		return ErrGone
	default:
		return fmt.Errorf("push: FCM respondió %d: %s", r.StatusCode, bytes.TrimSpace(b))
	}
}
