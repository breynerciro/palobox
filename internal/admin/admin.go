// Package admin es el panel del administrador: quién usa la app y cuánto consume el servidor.
package admin

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"palobox/internal/httpx"
)

type Handler struct {
	DB   *sql.DB
	Data string
}

func dirSize(root string) (n int64) {
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if i, err := d.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	if !u.Admin {
		return httpx.Error(403, "solo para el administrador")
	}
	type usage struct {
		Name     string `json:"name"`
		Seen     int64  `json:"seen"` // último uso, en segundos; 0 = nunca
		Sessions int    `json:"sessions"`
		Media    int    `json:"media"`
		Bytes    int64  `json:"bytes"`
	}
	rows, err := h.DB.Query(`SELECT u.name, u.seen,
		(SELECT COUNT(*) FROM sessions s WHERE s.user_id = u.id AND s.expires > ?),
		(SELECT COUNT(*) FROM media m WHERE m.user_id = u.id)
		FROM users u ORDER BY u.seen DESC, u.name`, time.Now().Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	users, isUser := []usage{}, map[string]bool{}
	for rows.Next() {
		var x usage
		if err := rows.Scan(&x.Name, &x.Seen, &x.Sessions, &x.Media); err != nil {
			return err
		}
		users, isUser[x.Name] = append(users, x), true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// shortcut: recorre todo el disco en cada consulta; guardar en caché si el panel tarda más de un par de segundos.
	for i := range users {
		users[i].Bytes = dirSize(filepath.Join(h.Data, users[i].Name))
	}
	other := []usage{} // carpetas que no son de un usuario: media, .palobox
	es, _ := os.ReadDir(h.Data)
	for _, e := range es {
		if e.IsDir() && !isUser[e.Name()] {
			other = append(other, usage{Name: e.Name(), Bytes: dirSize(filepath.Join(h.Data, e.Name()))})
		}
	}

	// /proc dentro del contenedor muestra los valores del servidor completo, no solo los de la app.
	var load [3]float64
	b, _ := os.ReadFile("/proc/loadavg")
	fmt.Sscan(string(b), &load[0], &load[1], &load[2])
	var up float64
	b, _ = os.ReadFile("/proc/uptime")
	fmt.Sscan(string(b), &up)
	mem := map[string]int64{}
	b, _ = os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		var k string
		var v int64
		if n, _ := fmt.Sscan(l, &k, &v); n == 2 {
			mem[k] = v << 10 // vienen en kB
		}
	}
	var st syscall.Statfs_t
	syscall.Statfs(h.Data, &st)
	return httpx.WriteJSON(w, 200, map[string]any{
		"users": users, "other": other,
		"cpus": runtime.NumCPU(), "load": load, "uptime": int64(up),
		"memTotal": mem["MemTotal:"], "memFree": mem["MemAvailable:"],
		"diskTotal": st.Blocks * uint64(st.Bsize), "diskFree": st.Bavail * uint64(st.Bsize),
	})
}
