package music

// Biblioteca compartida por todos los usuarios y de solo lectura: la llena spotDL
// o se copia por Samba a media/musica.
// shortcut: reproducción directa, sin transcodificar: cada dispositivo reproduce solo los formatos que soporta.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"palobox/internal/httpx"
	"palobox/internal/media"
)

type Library struct {
	DB     *sql.DB
	Data   string
	Sem    chan struct{} // cupo de procesos pesados, compartido con las miniaturas
	Thumbs media.Thumbs
	idxMu  sync.Mutex // un recorrido del índice a la vez
	idxAt  time.Time  // último recorrido completo; lo protege idxMu
}

// Reindex fuerza un recorrido del disco sin esperar el intervalo entre recorridos.
func (l *Library) Reindex() {
	l.idxMu.Lock()
	l.idxAt = time.Time{}
	l.idxMu.Unlock()
	l.Sync()
}

var libs = map[string]func(string) bool{"musica": media.IsAudio}

// libPath resuelve la ruta pedida dentro de la biblioteca ?lib= y devuelve su filtro de archivos.
func (l *Library) libPath(r *http.Request) (string, func(string) bool, error) {
	lib := r.URL.Query().Get("lib")
	keep, ok := libs[lib]
	if !ok {
		return "", nil, fs.ErrNotExist
	}
	root := filepath.Join(l.Data, "media", lib)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, err
	}
	return httpx.Safe(root, r.URL.Query().Get("path")), keep, nil
}

// Raw sirve un archivo de la biblioteca.
func (l *Library) Raw(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	p, keep, err := l.libPath(r)
	if err != nil {
		return err
	}
	// Se sirve en línea, así que solo sale audio o video: nada más de la carpeta queda expuesto.
	if st, err := os.Stat(p); err != nil || st.IsDir() || !keep(p) {
		return fs.ErrNotExist
	}
	http.ServeFile(w, r, p) // atiende peticiones por rangos: permite adelantar sin descargar todo
	return nil
}

// Thumb devuelve la portada de una canción o carpeta; 404 si no hay.
func (l *Library) Thumb(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	p, keep, err := l.libPath(r)
	if err != nil {
		return err
	}
	kind := "audio"
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if st.IsDir() {
		if p, kind = media.CoverSource(p, keep, 1); p == "" {
			return fs.ErrNotExist
		}
		if st, err = os.Stat(p); err != nil {
			return err
		}
	} else if !keep(p) {
		return fs.ErrNotExist
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%d", p, st.Size(), st.ModTime().UnixNano()))
	dst, err := l.Thumbs.Get(hex.EncodeToString(sum[:]), p, kind, "t")
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, dst)
	return nil
}

// fold normaliza para comparar: minúsculas y sin tildes ("canción" encuentra "Cancion").
var foldAccents = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n")

func fold(s string) string { return foldAccents.Replace(strings.ToLower(s)) }
