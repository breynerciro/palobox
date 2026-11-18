package main

// Archivos.

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (a *app) filesList(w http.ResponseWriter, r *http.Request, u user) error {
	root := a.dir(u, "archivos")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return listDir(w, safe(root, r.URL.Query().Get("path")))
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
		return natLess(strings.ToLower(out[i].Name), strings.ToLower(out[j].Name))
	})
	return writeJSON(w, 200, out)
}

// natLess compara con los números por su valor: "Season 2" va antes que "Season 10".
func natLess(a, b string) bool {
	digits := func(s string) int {
		n := 0
		for n < len(s) && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		return n
	}
	for a != "" && b != "" {
		if da, db := digits(a), digits(b); da > 0 && db > 0 {
			x, y := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(x) != len(y) {
				return len(x) < len(y)
			}
			if x != y {
				return x < y
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func (a *app) filesRaw(w http.ResponseWriter, r *http.Request, u user) error {
	p := safe(a.dir(u, "archivos"), r.URL.Query().Get("path"))
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
func (a *app) userPath(u user, rel string) (string, error) {
	root := a.dir(u, "archivos")
	p := safe(root, rel)
	if p == root {
		return "", httpErr{400, "ruta inválida"}
	}
	return p, nil
}

func (a *app) mkdir(w http.ResponseWriter, r *http.Request, u user) error {
	var in struct{ Path string }
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	p, err := a.userPath(u, in.Path)
	if err != nil {
		return err
	}
	return os.MkdirAll(p, 0o755)
}

func (a *app) filesDelete(w http.ResponseWriter, r *http.Request, u user) error {
	p, err := a.userPath(u, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	return os.RemoveAll(p)
}
