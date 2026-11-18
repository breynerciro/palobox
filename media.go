package main

// Música.
// Biblioteca compartida por todos los usuarios y de solo lectura: la llena spotDL
// o se copia por Samba a media/musica.
// shortcut: reproducción directa, sin transcodificar: cada dispositivo reproduce solo los formatos que soporta.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"palobox/internal/media"
)

var mediaLibs = map[string]func(string) bool{"musica": media.IsAudio}

// mediaPath resuelve la ruta pedida dentro de la biblioteca ?lib= y devuelve su filtro de archivos.
func (a *app) mediaPath(r *http.Request) (string, func(string) bool, error) {
	lib := r.URL.Query().Get("lib")
	keep, ok := mediaLibs[lib]
	if !ok {
		return "", nil, fs.ErrNotExist
	}
	root := filepath.Join(a.data, "media", lib)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, err
	}
	return safe(root, r.URL.Query().Get("path")), keep, nil
}

func (a *app) mediaRaw(w http.ResponseWriter, r *http.Request, u user) error {
	p, keep, err := a.mediaPath(r)
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

// mediaThumb devuelve la portada de una canción o carpeta; 404 si no hay.
func (a *app) mediaThumb(w http.ResponseWriter, r *http.Request, u user) error {
	p, keep, err := a.mediaPath(r)
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
	dst, err := a.thumbs.Get(hex.EncodeToString(sum[:]), p, kind, "t")
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
