package music

// PRUEBA (etapa 1): flujo continuo de audio para iPhone.
//
// iOS deja que una página siga sonando en segundo plano mientras sea el mismo audio; lo que a veces bloquea es
// cargar un audio nuevo con la app en segundo plano, que es lo que pasa hoy al terminar cada canción. Aquí el
// servidor manda varias canciones seguidas por una sola respuesta, como una emisora: el teléfono nunca cambia de
// fuente. Esta ruta solo sirve para comprobar en un iPhone real si así sí pasa de una canción a otra con la
// pantalla bloqueada (web/flujo.html). Si funciona, se integra al reproductor; si no, se borra.

import (
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"palobox/internal/httpx"
)

const (
	streamTracks = 8                // canciones de la prueba
	streamCut    = 45.0             // segundos de cada una: cuatro cambios en tres minutos, sin esperar canciones enteras
	streamLead   = 12 * time.Second // lo que se adelanta el envío al reloj: el colchón del reproductor
)

type streamTrack struct {
	Path   string  `json:"-"`
	Title  string  `json:"title"`
	Artist string  `json:"artist"`
	Start  float64 `json:"start"` // segundo del flujo en que empieza (aproximado: el corte es por bytes)
	size   int64
	dur    float64
}

// streamPick elige las canciones de la prueba: MP3 de más de un minuto, las mismas para la misma semilla
// (la página pide primero la lista y luego el audio).
func (l *Library) streamPick(seed int64) ([]streamTrack, error) {
	l.fresh()
	rows, err := l.DB.Query(`SELECT path, title, artist, size, dur FROM tracks WHERE path LIKE '%.mp3' AND dur > 60 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []streamTrack
	for rows.Next() {
		var t streamTrack
		if err := rows.Scan(&t.Path, &t.Title, &t.Artist, &t.size, &t.dur); err != nil {
			return nil, err
		}
		all = append(all, t)
	}
	rnd := rand.New(rand.NewSource(seed))
	rnd.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	if len(all) > streamTracks {
		all = all[:streamTracks]
	}
	for i := range all {
		all[i].Start = float64(i) * streamCut
	}
	return all, rows.Err()
}

// id3Size es el tamaño de la etiqueta ID3v2 al inicio del archivo (carátula incluida), que no debe ir en medio
// del flujo: cientos de KB que no son audio harían tropezar al decodificador entre canción y canción.
func id3Size(f *os.File) int64 {
	var h [10]byte
	if _, err := io.ReadFull(f, h[:]); err != nil || string(h[:3]) != "ID3" {
		return 0
	}
	n := int64(h[6]&0x7f)<<21 | int64(h[7]&0x7f)<<14 | int64(h[8]&0x7f)<<7 | int64(h[9]&0x7f)
	if h[5]&0x10 != 0 { // con pie de etiqueta
		n += 10
	}
	return n + 10
}

// infoFrame es el tamaño del primer cuadro MP3 que empieza en at si es el de información (Xing/Info de LAME).
// Ese cuadro no trae audio: declara cuántos cuadros tiene el archivo, y un reproductor que le crea daría el
// flujo por terminado al llegar a la duración de la primera canción.
func infoFrame(f *os.File, at int64) int64 {
	var b [48]byte // cabecera (4) + datos laterales (hasta 32) + la marca
	if n, _ := f.ReadAt(b[:], at); n < len(b) || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return 0
	}
	version, layer := (b[1]>>3)&3, (b[1]>>1)&3 // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5; capa 1 = Layer III
	rate, freq := int64(b[2]>>4), (b[2]>>2)&3
	if layer != 1 || version == 1 || rate == 0 || rate == 15 || freq == 3 {
		return 0
	}
	if !bytes.Contains(b[4:], []byte("Xing")) && !bytes.Contains(b[4:], []byte("Info")) {
		return 0
	}
	kbps := [2][16]int64{{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}, {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}}
	hz := map[byte][3]int64{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}[version][freq]
	pad := int64((b[2] >> 1) & 1)
	if version == 3 {
		return 144000*kbps[0][rate]/hz + pad
	}
	return 72000*kbps[1][rate]/hz + pad
}

// StreamTest responde la lista (?list=1) o el audio: las canciones una tras otra en una sola respuesta, enviadas
// al ritmo en que suenan (con un colchón), como lo haría el flujo real al seguir la cola.
func (l *Library) StreamTest(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	seed, _ := strconv.ParseInt(r.URL.Query().Get("seed"), 10, 64)
	tracks, err := l.streamPick(seed)
	if err != nil {
		return err
	}
	if r.URL.Query().Get("list") != "" {
		return httpx.WriteJSON(w, 200, tracks)
	}
	fl, ok := w.(http.Flusher)
	if !ok || len(tracks) == 0 {
		return httpx.Error(404, "No hay canciones MP3 para la prueba.")
	}
	// Sin Content-Length ni rangos: para el reproductor es una emisión en vivo, no un archivo.
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(200)
	start, sent := time.Now(), 0.0 // segundos de audio ya enviados
	buf := make([]byte, 16<<10)
	for _, t := range tracks {
		f, err := os.Open(filepath.Join(l.musicRoot(), filepath.FromSlash(t.Path)))
		if err != nil {
			continue // se borró entre la lista y el audio: sigue la próxima
		}
		skip := id3Size(f)
		skip += infoFrame(f, skip)
		f.Seek(skip, io.SeekStart)
		rate := float64(t.size-skip) / t.dur // bytes por segundo de esta canción
		left := int64(streamCut * rate)
		for left > 0 {
			n, err := f.Read(buf[:min(int64(len(buf)), left)])
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					f.Close()
					return nil // el teléfono cerró el flujo
				}
				fl.Flush()
				left -= int64(n)
				sent += float64(n) / rate
			}
			if err != nil {
				break
			}
			if ahead := time.Duration(sent*float64(time.Second)) - time.Since(start) - streamLead; ahead > 0 {
				select {
				case <-time.After(ahead):
				case <-r.Context().Done():
					f.Close()
					return nil
				}
			}
		}
		f.Close()
	}
	return nil
}
