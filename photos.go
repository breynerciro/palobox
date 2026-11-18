package main

// Fotos y videos.

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
)

func (a *app) photos(w http.ResponseWriter, r *http.Request, u user) error {
	// shortcut: devuelve todo el índice de una vez; paginar si la galería pasa de ~20.000 elementos.
	rows, err := a.db.Query(`SELECT id, name, size, kind, taken, sha FROM media WHERE user_id = ? ORDER BY taken DESC, id DESC`, u.id)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		Kind  string `json:"kind"`
		Taken int64  `json:"taken"`
		// Huella del contenido para las URLs de miniaturas (caché immutable): SQLite reutiliza el id
		// de la última fila borrada, y sin esto el celular mostraría la miniatura de la foto anterior.
		V string `json:"v"`
	}
	out := []item{}
	for rows.Next() {
		var i item
		var sha string
		if err := rows.Scan(&i.ID, &i.Name, &i.Size, &i.Kind, &i.Taken, &sha); err != nil {
			return err
		}
		i.V = sha[:min(12, len(sha))]
		out = append(out, i)
	}
	return writeJSON(w, 200, out)
}

func (a *app) photo(w http.ResponseWriter, r *http.Request, u user) error {
	var sha, kind, path, name string
	if err := a.db.QueryRow(`SELECT sha, kind, path, name FROM media WHERE id = ? AND user_id = ?`,
		r.PathValue("id"), u.id).Scan(&sha, &kind, &path, &name); err != nil {
		return err
	}
	src := filepath.Join(a.data, path)
	v := r.PathValue("v")
	switch {
	case v == "o":
		if r.URL.Query().Has("dl") {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		}
		http.ServeFile(w, r, src)
	case v == "t", v == "p" && kind == "image":
		dst, err := a.thumb(sha, src, kind, v)
		if err != nil {
			return err
		}
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		http.ServeFile(w, r, dst)
	default:
		return fs.ErrNotExist
	}
	return nil
}

func (a *app) photoDelete(w http.ResponseWriter, r *http.Request, u user) error {
	var sha, path string
	if err := a.db.QueryRow(`SELECT sha, path FROM media WHERE id = ? AND user_id = ?`, r.PathValue("id"), u.id).Scan(&sha, &path); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(a.data, path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, v := range []string{"t", "p"} {
		os.Remove(filepath.Join(a.data, ".palobox", "thumbs", sha+"_"+v+".jpg"))
	}
	_, err := a.db.Exec(`DELETE FROM media WHERE id = ? AND user_id = ?`, r.PathValue("id"), u.id)
	return err
}
