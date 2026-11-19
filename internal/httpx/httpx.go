// Package httpx reúne lo que comparten todos los handlers: el usuario autenticado,
// la firma de un handler, los errores HTTP y las respuestas JSON.
package httpx

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
)

// User es el usuario de la sesión, ya verificado por auth.
type User struct {
	ID        int64
	Name      string
	Admin     bool
	MusicOnly bool // cuenta de invitado: solo entra a Música, sin fotos ni archivos propios
}

// Handler atiende una petición autenticada; el error lo traduce Fail.
type Handler func(http.ResponseWriter, *http.Request, User) error

// Err es un error con código HTTP y un mensaje para mostrar al usuario.
type Err struct {
	Code int
	Msg  string
}

func (e Err) Error() string { return e.Msg }

// Error crea un Err.
func Error(code int, msg string) error { return Err{Code: code, Msg: msg} }

func WriteJSON(w http.ResponseWriter, code int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	return json.NewEncoder(w).Encode(v)
}

// Gzip comprime las respuestas JSON de h si el cliente lo acepta: /api/music pasa de MB a cientos de KB.
// Solo para rutas que responden JSON: el envoltorio no es http.Flusher, que el audio y los eventos necesitan.
func Gzip(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h(w, r)
			return
		}
		g := &gzipWriter{ResponseWriter: w}
		h(g, r)
		if g.zw != nil {
			g.zw.Close()
			zips.Put(g.zw)
		}
	}
}

// El compresor reserva cientos de KB al crearse: se reutiliza entre respuestas.
var zips = sync.Pool{New: func() any { z, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed); return z }}

type gzipWriter struct {
	http.ResponseWriter
	zw    *gzip.Writer // nil: la respuesta sale tal cual (errores en texto)
	wrote bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if !g.wrote && code != http.StatusNoContent && g.Header().Get("Content-Type") == "application/json" {
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Del("Content-Length")
		g.zw = zips.Get().(*gzip.Writer)
		g.zw.Reset(g.ResponseWriter)
	}
	g.wrote = true
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.wrote {
		g.WriteHeader(http.StatusOK)
	}
	if g.zw != nil {
		return g.zw.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func ReadJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v) != nil {
		return Error(400, "petición inválida")
	}
	return nil
}

func Fail(w http.ResponseWriter, err error) {
	var he Err
	switch {
	case errors.As(err, &he):
		http.Error(w, he.Msg, he.Code)
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, sql.ErrNoRows):
		http.Error(w, "no encontrado", 404)
	default:
		log.Print(err)
		http.Error(w, "error interno", 500)
	}
}

// Safe une rel bajo base sin dejar salir de base: limpiar "/"+rel elimina todos los "..".
func Safe(base, rel string) string { return filepath.Join(base, filepath.Clean("/"+rel)) }

// UserDir es la carpeta sub del usuario dentro de los datos: <data>/<usuario>/<sub>.
func UserDir(data string, u User, sub string) string { return filepath.Join(data, u.Name, sub) }
