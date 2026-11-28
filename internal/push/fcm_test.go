package push

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Un Google y un FCM falsos: el token de acceso solo sale con un JWT bien firmado, y el envío dice UNREGISTERED
// para el token "viejo".
func TestSend(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			asked++
			parts := strings.Split(r.FormValue("assertion"), ".")
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
				http.Error(w, "firma", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "acc", "expires_in": 3600})
		case "/send":
			var m struct{ Message struct{ Token string } }
			json.NewDecoder(r.Body).Decode(&m)
			if r.Header.Get("Authorization") != "Bearer acc" {
				http.Error(w, "auth", 401)
			} else if m.Message.Token == "viejo" {
				http.Error(w, `{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`, 404)
			}
		}
	}))
	defer srv.Close()
	sa, _ := json.Marshal(map[string]string{"project_id": "p", "client_email": "x@p.iam", "token_uri": srv.URL + "/token",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	f, err := Parse(sa)
	if err != nil {
		t.Fatal(err)
	}
	f.SendURL = srv.URL + "/send"
	if err := f.Send("bueno", map[string]string{"op": "play"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Send("viejo", nil); err != ErrGone {
		t.Fatalf("token viejo: %v", err)
	}
	if asked != 1 {
		t.Fatalf("pidió %d tokens de acceso; el primero debía servir para los dos envíos", asked)
	}
}
