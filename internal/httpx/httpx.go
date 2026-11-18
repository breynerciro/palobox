// Package httpx reúne lo que comparten todos los handlers: el usuario autenticado,
// la firma de un handler, los errores HTTP y las respuestas JSON.
package httpx

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
)

// User es el usuario de la sesión, ya verificado por auth.
type User struct {
	ID    int64
	Name  string
	Admin bool
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
