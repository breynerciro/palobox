package main

// Subidas reanudables y en paralelo.
// POST /api/uploads devuelve un id, el tamaño de trozo y qué trozos faltan; PATCH ?chunk=i escribe
// el trozo i en su lugar, en cualquier orden y varios a la vez. El que completa el archivo lo cierra.
// Si la conexión se corta, el cliente repite el POST y manda solo los que faltan.
// Cada trozo trae su CRC32 en X-Chunk-CRC32 (8 hex): si no coincide con lo recibido, se rechaza
// y el cliente lo repite. CRC32 y no SHA-256: crypto.subtle solo existe en HTTPS y la app también
// se usa por http en la red local; para detectar daño accidental basta.
// Antes de cerrar, el servidor relee el .part completo y compara cada tramo con la huella que
// mandó el cliente para esa posición: detecta un trozo escrito donde no era o mal guardado.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"palobox/internal/media"
)

// maxUpload es el tamaño máximo de una subida: el tamaño lo declara el cliente.
const maxUpload = 50 << 30

// upChunk es el tamaño de cada trozo (el último puede ser menor). Variable para que las pruebas
// usen trozos pequeños.
var upChunk int64 = 16 << 20

func nChunks(size int64) int64 { return (size + upChunk - 1) / upChunk }

type upMeta struct {
	User  int64  `json:"user"`
	Kind  string `json:"kind"` // "photo" o "file"
	Dir   string `json:"dir"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"` // ms
}

var idRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func mediaKind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".heic", ".heif", ".gif", ".webp", ".avif", ".dng", ".tif", ".tiff":
		return "image"
	case ".mov", ".mp4", ".m4v", ".webm", ".mkv", ".avi", ".3gp":
		return "video"
	}
	return ""
}

func (a *app) upStart(w http.ResponseWriter, r *http.Request, u user) error {
	var m upMeta
	if err := readJSON(w, r, &m); err != nil {
		return err
	}
	m.User = u.id
	m.Name = filepath.Base(filepath.Clean("/" + m.Name))
	if m.Name == "/" || strings.HasPrefix(m.Name, ".") || m.Size <= 0 {
		return httpErr{400, "archivo inválido"}
	}
	if m.Size > maxUpload {
		return httpErr{413, "el archivo supera el máximo de 50 GiB por subida"}
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%s|%s|%s|%d|%d", m.User, m.Kind, m.Dir, m.Name, m.Size, m.Mtime))
	id := hex.EncodeToString(sum[:16])
	// Si el último PATCH está cerrando esta subida, se espera: borrar su .part la tumbaría.
	defer a.lockUp(id)()
	switch m.Kind {
	case "photo":
		if mediaKind(m.Name) == "" {
			return httpErr{400, "tipo de archivo no soportado"}
		}
		// iOS reescribe marcas de hora al exportar videos, así que su huella cambia en cada
		// subida: nombre + tamaño detecta esos duplicados antes de gastar datos.
		var one int
		if a.db.QueryRow(`SELECT 1 FROM media WHERE user_id = ? AND name = ? AND size = ?`, u.id, m.Name, m.Size).Scan(&one) == nil {
			return writeJSON(w, 200, map[string]any{"done": true, "dup": true})
		}
	case "file":
	default:
		return httpErr{400, "tipo inválido"}
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(a.tmp(id+".json"), b, 0o644); err != nil {
		return err
	}
	got := a.got(id)
	missing := []int64{}
	for i := range nChunks(m.Size) {
		if _, ok := got[i]; !ok {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 { // llegó todo pero no alcanzó a cerrarse
		return a.finish(w, u, m, id)
	}
	return writeJSON(w, 200, map[string]any{"id": id, "chunk": upChunk, "missing": missing})
}

// got lee qué trozos ya están escritos y su CRC32: "índice crc" por línea en id.got. Solo cuentan
// las líneas terminadas, por si el servidor se apagó a mitad de una. Sin .part no hay nada.
func (a *app) got(id string) map[int64]uint32 {
	m := map[int64]uint32{}
	if _, err := os.Stat(a.tmp(id + ".part")); err != nil {
		os.Remove(a.tmp(id + ".got"))
		return m
	}
	b, _ := os.ReadFile(a.tmp(id + ".got"))
	lines := strings.Split(string(b), "\n")
	for _, l := range lines[:len(lines)-1] {
		var i int64
		var crc uint32
		if _, err := fmt.Sscanf(l, "%d %08x", &i, &crc); err == nil {
			m[i] = crc
		}
	}
	return m
}

// lockUp toma el candado de la subida id y devuelve cómo soltarlo. La entrada se borra al
// soltar, así que solo vale el candado que sigue en el mapa.
func (a *app) lockUp(id string) (unlock func()) {
	for {
		v, _ := a.upMu.LoadOrStore(id, new(sync.Mutex))
		mu := v.(*sync.Mutex)
		mu.Lock()
		if cur, _ := a.upMu.Load(id); cur == v {
			return func() { a.upMu.Delete(id); mu.Unlock() }
		}
		mu.Unlock()
	}
}

func (a *app) upPart(w http.ResponseWriter, r *http.Request, u user) error {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		return fs.ErrNotExist
	}
	b, err := os.ReadFile(a.tmp(id + ".json"))
	if err != nil {
		return err
	}
	var m upMeta
	if json.Unmarshal(b, &m) != nil || m.User != u.id {
		return fs.ErrNotExist
	}
	i, err := strconv.ParseInt(r.URL.Query().Get("chunk"), 10, 64)
	if err != nil || i < 0 || i >= nChunks(m.Size) {
		return httpErr{400, "trozo inválido"}
	}
	want32, err := strconv.ParseUint(r.Header.Get("X-Chunk-CRC32"), 16, 32)
	if err != nil {
		return httpErr{400, "falta la huella del trozo"}
	}
	// Un trozo ya aceptado no se vuelve a escribir: un reenvío dañado pisaría bytes buenos, o el
	// archivo ya guardado si llega mientras se cierra.
	if _, ok := a.got(id)[i]; ok {
		return writeJSON(w, 200, map[string]any{"done": false})
	}
	off := i * upChunk
	want := min(upChunk, m.Size-off)
	// Sin candado: cada trozo escribe solo su tramo del .part, así varios entran a la vez.
	f, err := os.OpenFile(a.tmp(id+".part"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	crc := crc32.NewIEEE()
	if n, err := io.Copy(io.MultiWriter(io.NewOffsetWriter(f, off), crc), io.LimitReader(r.Body, want)); err != nil || n != want {
		return httpErr{400, "subida interrumpida"} // el trozo no se marca: el cliente lo repite
	}
	if crc.Sum32() != uint32(want32) { // tampoco se marca: el siguiente envío lo sobrescribe
		return httpErr{400, "trozo dañado en el camino"}
	}
	// En disco antes de marcarlo: tras un apagón, un trozo marcado nunca queda con ceros.
	if err := f.Sync(); err != nil {
		return err
	}
	defer a.lockUp(id)()
	if _, err := os.Stat(a.tmp(id + ".json")); err != nil { // un trozo repetido llegó tras el cierre
		return writeJSON(w, 200, map[string]any{"done": true})
	}
	g, err := os.OpenFile(a.tmp(id+".got"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(g, "%d %08x\n", i, want32)
	if cerr := g.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if int64(len(a.got(id))) < nChunks(m.Size) {
		return writeJSON(w, 200, map[string]any{"done": false})
	}
	return a.finish(w, u, m, id)
}

// finish cierra una subida completa; se llama con el candado de la subida tomado.
func (a *app) finish(w http.ResponseWriter, u user, m upMeta, id string) error {
	got := a.got(id)
	bad, err := verify(a.tmp(id+".part"), m.Size, got)
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		// Se desmarcan los tramos que no cuadran: el cliente ve que no cerró, repite el POST y
		// los manda otra vez. Se registra porque indica un bug o un disco con fallas.
		log.Printf("subida %s: %d tramos no coinciden con su huella al releer: %v", id, len(bad), bad)
		var b strings.Builder
		for i, crc := range got {
			if !slices.Contains(bad, i) {
				fmt.Fprintf(&b, "%d %08x\n", i, crc)
			}
		}
		if err := os.WriteFile(a.tmp(id+".got"), []byte(b.String()), 0o644); err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"done": false})
	}
	dup, err := a.finalize(u, m, a.tmp(id+".part"))
	if err != nil {
		return err
	}
	os.Remove(a.tmp(id + ".json"))
	os.Remove(a.tmp(id + ".got"))
	return writeJSON(w, 200, map[string]any{"done": true, "dup": dup})
}

// verify relee el .part y devuelve los trozos cuyo contenido en disco no coincide con la huella
// recibida. Un .part de otro tamaño es un bug: se marcan todos para volver a recibirlos.
func verify(part string, size int64, got map[int64]uint32) (bad []int64, err error) {
	f, err := os.Open(part)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() != size {
		for i := range nChunks(size) {
			bad = append(bad, i)
		}
		return bad, err
	}
	crc := crc32.NewIEEE()
	for i := range nChunks(size) {
		crc.Reset()
		if _, err := io.Copy(crc, io.NewSectionReader(f, i*upChunk, upChunk)); err != nil {
			return nil, err
		}
		if want, ok := got[i]; !ok || crc.Sum32() != want {
			bad = append(bad, i)
		}
	}
	return bad, nil
}

func (a *app) finalize(u user, m upMeta, part string) (dup bool, err error) {
	if m.Kind == "file" {
		_, err := moveTo(part, safe(a.dir(u, "archivos"), filepath.Join(m.Dir, m.Name)))
		return false, err
	}
	sha, err := hashFile(part)
	if err != nil {
		return false, err
	}
	// La fecha (vips/ffmpeg, hasta 15 s) solo lee el .part propio: va fuera del candado para
	// que las subidas en paralelo no esperen en fila.
	kind := mediaKind(m.Name)
	taken := media.TakenAt(part, kind)
	if taken.IsZero() {
		taken = time.UnixMilli(m.Mtime)
	}
	// Sin esto, dos subidas simultáneas de la misma foto pasan ambas el chequeo de duplicado
	// y eligen el mismo destino. Dentro solo quedan la consulta, el rename y el INSERT.
	a.finMu.Lock()
	defer a.finMu.Unlock()
	var one int
	if a.db.QueryRow(`SELECT 1 FROM media WHERE user_id = ? AND sha = ?`, u.id, sha).Scan(&one) == nil {
		return true, os.Remove(part)
	}
	dst, err := moveTo(part, filepath.Join(a.dir(u, "fotos"), taken.Format("2006/01"), m.Name))
	if err != nil {
		return false, err
	}
	rel, _ := filepath.Rel(a.data, dst)
	if _, err := a.db.Exec(`INSERT INTO media(user_id, sha, name, size, kind, taken, path) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		u.id, sha, m.Name, m.Size, kind, taken.UnixMilli(), rel); err != nil {
		os.Remove(dst) // sin fila en el índice el archivo quedaría huérfano
		return false, err
	}
	go a.thumbs.Get(sha, dst, kind, "t") // si falla, se reintenta al pedir la miniatura
	return false, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// moveTo mueve src a dst, o a "dst (1)", "dst (2)"... si ya existe, y devuelve dónde quedó:
// nunca se sobrescribe un archivo. Crear el destino con O_EXCL reserva el nombre de forma
// atómica, así que dos subidas simultáneas con el mismo nombre no se pisan.
func moveTo(src, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	ext := filepath.Ext(dst)
	base := strings.TrimSuffix(dst, ext)
	for i := 1; ; i++ {
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			if err := os.Rename(src, dst); err != nil {
				os.Remove(dst)
				return "", err
			}
			return dst, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		dst = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}
