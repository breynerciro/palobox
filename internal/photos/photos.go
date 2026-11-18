// Package photos es la galería de fotos y videos de cada usuario (tabla media).
package photos

import (
	"database/sql"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"palobox/internal/httpx"
	"palobox/internal/media"
)

type Handler struct {
	DB     *sql.DB
	Data   string
	Thumbs media.Thumbs
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	// shortcut: devuelve todo el índice de una vez; paginar si la galería pasa de ~20.000 elementos.
	rows, err := h.DB.Query(`SELECT id, name, size, kind, taken, sha FROM media WHERE user_id = ? ORDER BY taken DESC, id DESC`, u.ID)
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
	return httpx.WriteJSON(w, 200, out)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	var sha, kind, path, name string
	if err := h.DB.QueryRow(`SELECT sha, kind, path, name FROM media WHERE id = ? AND user_id = ?`,
		r.PathValue("id"), u.ID).Scan(&sha, &kind, &path, &name); err != nil {
		return err
	}
	src := filepath.Join(h.Data, path)
	v := r.PathValue("v")
	switch {
	case v == "o":
		if r.URL.Query().Has("dl") {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		}
		http.ServeFile(w, r, src)
	case v == "t", v == "p" && kind == "image":
		dst, err := h.Thumbs.Get(sha, src, kind, v)
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

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	var sha, path string
	if err := h.DB.QueryRow(`SELECT sha, path FROM media WHERE id = ? AND user_id = ?`, r.PathValue("id"), u.ID).Scan(&sha, &path); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(h.Data, path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, v := range []string{"t", "p"} {
		os.Remove(h.Thumbs.Path(sha, v))
	}
	_, err := h.DB.Exec(`DELETE FROM media WHERE id = ? AND user_id = ?`, r.PathValue("id"), u.ID)
	return err
}
