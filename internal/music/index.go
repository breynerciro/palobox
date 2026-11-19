package music

// Índice de pistas: pone la tabla tracks al día con el disco y lee las etiquetas con ffprobe.
// Su par es dedupe.go, que después de cada recorrido une álbumes partidos y borra duplicadas.

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"palobox/internal/media"
)

var leadNum = regexp.MustCompile(`^(\d{1,3})[\s._-]+(.+)$`)

func leadInt(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	v, _ := strconv.Atoi(s[:n])
	return v
}

// fromPath deduce los datos de una pista de su ruta: Artista / Álbum / 01 - Título.mp3.
func fromPath(rel string) track {
	parts := strings.Split(rel, "/")
	t := track{Path: rel, Title: strings.TrimSuffix(parts[len(parts)-1], path.Ext(rel)), Album: "Sin álbum"}
	if m := leadNum.FindStringSubmatch(t.Title); m != nil {
		t.Num, t.Title = leadInt(m[1]), m[2]
	}
	if len(parts) > 1 {
		t.AlbumPath, t.ArtistPath = path.Dir(rel), parts[0]
		t.Album, t.Artist = parts[len(parts)-2], parts[0]
	}
	return t
}

// applyTags pisa los datos de la ruta con las etiquetas que ffprobe haya encontrado.
func applyTags(t *track, probe []byte) {
	var p struct {
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if json.Unmarshal(probe, &p) != nil {
		return
	}
	tags := map[string]string{}
	for _, s := range p.Streams { // Ogg y Opus llevan las etiquetas en la pista, no en el contenedor
		for k, v := range s.Tags {
			tags[strings.ToLower(k)] = strings.TrimSpace(v)
		}
	}
	for k, v := range p.Format.Tags {
		tags[strings.ToLower(k)] = strings.TrimSpace(v)
	}
	for k, dst := range map[string]*string{"title": &t.Title, "artist": &t.Artist, "album": &t.Album} {
		if tags[k] != "" {
			*dst = tags[k]
		}
	}
	if n := leadInt(tags["track"]); n > 0 { // "3/12"
		t.Num = n
	}
	t.disc, t.genre = leadInt(tags["disc"]), tags["genre"]
	if t.Year = leadInt(tags["date"]); t.Year == 0 {
		t.Year = leadInt(tags["year"])
	}
	t.Dur, _ = strconv.ParseFloat(p.Format.Duration, 64)
}

const idxEvery = 20 * time.Second // el recorrido solo hace stat, pero no vale la pena en cada petición

// syncTracks pone el índice al día con el disco: solo pasa por ffprobe lo nuevo o cambiado.
func (l *Library) Sync() {
	if !l.idxMu.TryLock() { // ya hay un recorrido en curso: se responde con lo indexado hasta ahora
		return
	}
	defer l.idxMu.Unlock()
	if time.Since(l.idxAt) < idxEvery {
		return
	}
	l.scanning.Store(true)
	defer l.scanning.Store(false)
	// También si el recorrido se corta a medias: lo que alcanzó a escribir ya está en la tabla.
	defer func() {
		if l.changed {
			l.changed = false
			l.rebuild()
		}
	}()
	root := l.musicRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		log.Print("índice de música: ", err)
		return
	}
	type stamp struct{ size, mtime int64 }
	old := map[string]stamp{}
	rows, err := l.DB.Query(`SELECT path, size, mtime FROM tracks`)
	if err != nil {
		log.Print("índice de música: ", err)
		return
	}
	for rows.Next() {
		var p string
		var s stamp
		rows.Scan(&p, &s.size, &s.mtime)
		old[p] = s
	}
	rows.Close()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != root {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !media.IsAudio(d.Name()) {
			return nil
		}
		i, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		now := stamp{i.Size(), i.ModTime().UnixMilli()}
		was, ok := old[rel]
		delete(old, rel)
		if ok && was == now {
			return nil
		}
		t := fromPath(rel)
		l.Sem <- struct{}{}
		// Un archivo corrupto no puede detener el índice.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "a",
			"-show_entries", "format=duration:format_tags:stream_tags", "-of", "json", p).Output()
		late := ctx.Err() == context.DeadlineExceeded
		cancel()
		<-l.Sem
		// Se pasó del tiempo (equipo cargado o un archivo que cuelga a ffprobe): sin fila, para reintentarlo en los
		// recorridos siguientes; a la tercera entra con lo de la ruta, para no gastar 15 s en cada recorrido. Si queda
		// sin duración, el flujo continuo avanza igual al acabarse el archivo (jam/stream.go).
		if late {
			if l.late == nil {
				l.late = map[string]int{}
			}
			if l.late[rel]++; l.late[rel] < 3 {
				log.Print("índice de música: ", rel, ": ffprobe se pasó del tiempo; se reintenta")
				return nil
			}
			log.Print("índice de música: ", rel, ": ffprobe se pasó del tiempo tres veces; entra con lo de la ruta")
		}
		delete(l.late, rel)
		// No arrancó el proceso (falta de memoria o de procesos, algo pasajero y barato de repetir): sin fila, para
		// que el siguiente recorrido lo intente de nuevo. Si ffprobe terminó con error, la pista entra con lo de la ruta.
		var exit *exec.ExitError
		if !late && err != nil && !errors.As(err, &exit) && !errors.Is(err, exec.ErrNotFound) {
			log.Print("índice de música: ", rel, ": ", err)
			return nil
		}
		applyTags(&t, out)
		l.changed = true
		if _, err := l.DB.Exec(`INSERT OR REPLACE INTO tracks(path, size, mtime, title, artist, album, album_dir, artist_dir, year, disc, num, dur, fold, genre)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rel, now.size, now.mtime, t.Title, t.Artist, t.Album, t.AlbumPath, t.ArtistPath,
			t.Year, t.disc, t.Num, t.Dur, fold(t.Title+" "+t.Artist+" "+t.Album), t.genre); err != nil {
			log.Print("índice de música: ", err)
		}
		return nil
	})
	if err != nil { // disco sin montar o ilegible: mejor un índice viejo que uno vacío
		log.Print("índice de música: ", err)
		return
	}
	for rel := range old {
		l.DB.Exec(`DELETE FROM tracks WHERE path = ?`, rel)
		l.changed = true
	}
	l.dedupe()
	l.idxAt = time.Now()
	l.scanned.Store(l.idxAt.Unix())
}

// fresh actualiza el índice antes de responder, sin hacer esperar más de 2 s por una biblioteca recién copiada.
func (l *Library) fresh() {
	done := make(chan struct{})
	go func() { l.Sync(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
