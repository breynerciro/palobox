package main

// HTTP.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
)

type httpErr struct {
	code int
	msg  string
}

func (e httpErr) Error() string { return e.msg }

func writeJSON(w http.ResponseWriter, code int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	return json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v) != nil {
		return httpErr{400, "petición inválida"}
	}
	return nil
}

func fail(w http.ResponseWriter, err error) {
	var he httpErr
	switch {
	case errors.As(err, &he):
		http.Error(w, he.msg, he.code)
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, sql.ErrNoRows):
		http.Error(w, "no encontrado", 404)
	default:
		log.Print(err)
		http.Error(w, "error interno", 500)
	}
}
