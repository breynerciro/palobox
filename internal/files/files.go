// Package files es el explorador de archivos personales: <data>/<usuario>/archivos.
package files

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"palobox/internal/httpx"
	"palobox/internal/natsort"
)

type Handler struct {
	Data string
}

func (h *Handler) root(u httpx.User) string { return httpx.UserDir(h.Data, u, "archivos") }

func (h *Handler) List(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	root := h.root(u)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return listDir(w, httpx.Safe(root, r.URL.Query().Get("path")))
}

type entry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// listDir responde el contenido de dir: carpetas primero, sin ocultos.
func listDir(w http.ResponseWriter, dir string) error {
	es, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	out := []entry{}
	for _, e := range es {
		i, err := e.Info()
		if err != nil || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, entry{Name: e.Name(), Dir: e.IsDir(), Size: i.Size(), Mtime: i.ModTime().UnixMilli()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return natsort.Less(strings.ToLower(out[i].Name), strings.ToLower(out[j].Name))
	})
	return httpx.WriteJSON(w, 200, out)
}

func (h *Handler) Raw(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	p := httpx.Safe(h.root(u), r.URL.Query().Get("path"))
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fs.ErrNotExist
	}
	// Siempre como descarga: un HTML subido por un usuario no debe ejecutarse en este origen.
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(p)}))
	http.ServeFile(w, r, p)
	return nil
}

// userPath resuelve una ruta dentro de "archivos" y rechaza la raíz misma.
func (h *Handler) userPath(u httpx.User, rel string) (string, error) {
	root := h.root(u)
	p := httpx.Safe(root, rel)
	if p == root {
		return "", httpx.Error(400, "ruta inválida")
	}
	return p, nil
}

func (h *Handler) Mkdir(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	var in struct{ Path string }
	if err := httpx.ReadJSON(w, r, &in); err != nil {
		return err
	}
	p, err := h.userPath(u, in.Path)
	if err != nil {
		return err
	}
	return os.MkdirAll(p, 0o755)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	p, err := h.userPath(u, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	return os.RemoveAll(p)
}
