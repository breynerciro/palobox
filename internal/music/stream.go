package music

// Audio para el flujo continuo (jam.Stream): iOS deja que una página siga sonando en segundo plano mientras sea
// el mismo audio, pero a veces bloquea cargar uno nuevo, que es lo que pasa al terminar cada canción. En el flujo
// el servidor manda las canciones una tras otra por una sola respuesta, como una emisora. Aquí se prepara cada
// canción para ir en medio de ese flujo: solo cuadros MP3, todos del mismo tipo.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"palobox/internal/httpx"
)

// StreamRate son los bytes por segundo de lo que sale de ffmpeg (MP3 a 128 kbps).
const StreamRate = 16000.0

// StreamHz es la frecuencia de todo lo que va por el flujo continuo o se descarga para escuchar sin conexión.
const StreamHz = 48000

// frameLen es el tamaño del cuadro MP3 cuya cabecera está en b (MPEG-1 capa III a 48 kHz y en estéreo, lo único
// que entra al flujo sin convertir), o 0 si b no empieza con una cabecera así. Todo lo que va por un mismo flujo
// tiene que ser del mismo tipo: un cambio de frecuencia o de mono a estéreo a mitad de camino rompe el audio.
// 48 kHz es lo que deja spotDL (el audio de YouTube viene así): con ese estándar la biblioteca sale tal cual.
func frameLen(b []byte) int64 {
	kbps := [16]int64{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xFE != 0xFA || b[2]>>4 == 0 || b[2]>>4 == 15 || (b[2]>>2)&3 != 1 || b[3]>>6 == 3 {
		return 0
	}
	return 144000*kbps[b[2]>>4]/StreamHz + int64((b[2]>>1)&1)
}

// mp3Body delimita el audio de un MP3: después de la etiqueta ID3v2 (con la carátula) y del cuadro de
// información de LAME, y antes de la etiqueta ID3v1. Ese cuadro no trae audio: declara cuántos cuadros tiene el
// archivo, y un reproductor que le crea daría el flujo por terminado al llegar a la duración de la primera
// canción. ok es falso si el archivo no es MPEG-1 capa III a 48 kHz en estéreo: ese hay que convertirlo.
func mp3Body(f *os.File, size int64) (start, end int64, ok bool) {
	var h [10]byte
	if _, err := f.ReadAt(h[:], 0); err == nil && string(h[:3]) == "ID3" {
		start = 10 + (int64(h[6]&0x7f)<<21 | int64(h[7]&0x7f)<<14 | int64(h[8]&0x7f)<<7 | int64(h[9]&0x7f))
		if h[5]&0x10 != 0 { // con pie de etiqueta
			start += 10
		}
	}
	var b [48]byte // cabecera (4) + datos laterales (hasta 32) + la marca
	if _, err := f.ReadAt(b[:], start); err != nil {
		return 0, 0, false
	}
	n := frameLen(b[:])
	if n == 0 {
		return 0, 0, false
	}
	if bytes.Contains(b[4:], []byte("Xing")) || bytes.Contains(b[4:], []byte("Info")) {
		start += n
	}
	end = size
	var tag [3]byte
	if _, err := f.ReadAt(tag[:], size-128); err == nil && string(tag[:]) == "TAG" {
		end = size - 128
	}
	return start, end, end > start
}

type sectionCloser struct {
	io.Reader
	io.Closer
}

type procCloser struct {
	io.ReadCloser
	cancel  context.CancelFunc
	cmd     *exec.Cmd
	release func() // devuelve el cupo de conversión, si lo tomó
}

func (p procCloser) Close() error {
	p.cancel()
	p.ReadCloser.Close()
	err := p.cmd.Wait()
	p.release()
	return err
}

// maxConv son las conversiones de ffmpeg que el flujo continuo deja correr a la vez. Medido en el servidor
// (i5-5200U, 4 hilos): 40 ocupan media CPU y dejan sitio a una descarga; con 60 se llena y pasadas las 60 el
// audio se corta para todos los que oyen, no solo para el que llegó de último.
const maxConv = 40

var convSlots = make(chan struct{}, maxConv)

// frameAt busca desde off el primer cuadro MP3 seguido de otro (una cabecera suelta puede ser casualidad dentro
// del audio) y dice dónde empieza, o -1 si no hay ninguno cerca.
func frameAt(f *os.File, off, end int64) int64 {
	b := make([]byte, 8<<10) // caben varios cuadros: el más largo (320 kbps) mide 961 bytes
	n, _ := f.ReadAt(b[:min(int64(len(b)), max(end-off, 0))], off)
	for i := 0; i+4 < n; i++ {
		if k := int(frameLen(b[i:n])); k > 0 && i+k+4 <= n && frameLen(b[i+k:n]) > 0 {
			return off + int64(i)
		}
	}
	return -1
}

// OpenStream abre el audio de una pista desde el segundo at para el flujo continuo y dice a cuántos bytes por
// segundo va. Desde el principio, un MP3 del tipo común sale tal cual. Lo demás lo convierte ffmpeg sobre la
// marcha: otros formatos (FLAC, M4A, MP3 a 44,1 kHz), porque en un mismo flujo no se puede cambiar de formato, y
// cualquier pista que se abre a mitad de camino (adelantar, reabrir el flujo), porque cada cuadro MP3 usa datos
// del anterior y empezar en uno suelto suena como un ruido; ffmpeg entrega un comienzo limpio. Con maxConv
// conversiones ya en curso, el MP3 común sale tal cual desde ese cuadro suelto.
func (l *Library) OpenStream(path string, dur, at float64) (io.ReadCloser, float64, error) {
	full := httpx.Safe(l.musicRoot(), path)
	f, err := os.Open(full)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		return nil, 0, os.ErrNotExist
	}
	start, end, plain := mp3Body(f, st.Size())
	plain = plain && dur > 0 && strings.EqualFold(filepath.Ext(path), ".mp3")
	// Menos de una décima de segundo es «desde el principio»: el flujo se abre un instante después del salto.
	if at < 0.1 {
		at = 0
		if plain {
			return sectionCloser{io.NewSectionReader(f, start, end-start), f}, float64(end-start) / dur, nil
		}
	}
	release := func() {}
	select {
	case convSlots <- struct{}{}:
		release = func() { <-convSlots }
	default:
		// Sin cupo: el archivo sale tal cual desde el cuadro más cercano. El primer instante puede sonar sucio,
		// pero el servidor no se satura. Lo que ffmpeg tiene que convertir sí o sí (otro formato) pasa sin cupo.
		if plain {
			rate := float64(end-start) / dur
			if off := frameAt(f, start+int64(at*rate), end); off >= 0 {
				return sectionCloser{io.NewSectionReader(f, off, end-off), f}, rate, nil
			}
		}
	}
	f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", full,
		"-vn", "-map_metadata", "-1", "-ac", "2", "-ar", strconv.Itoa(StreamHz), "-b:a", "128k", "-write_xing", "0", "-id3v2_version", "0", "-f", "mp3", "-")
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		release()
		return nil, 0, err
	}
	return procCloser{out, cancel, cmd, release}, StreamRate, nil
}

// Offline entrega una pista lista para guardarse en el celular y sonar sin conexión (web/offline.js): solo
// cuadros MP3 en estéreo a 48 kHz, sin etiquetas, igual que en el flujo continuo. Así varias pistas guardadas se
// pueden unir una tras otra en un solo audio dentro del teléfono, que es lo que deja sonar la siguiente con la
// pantalla bloqueada sin cargar nada.
func (l *Library) Offline(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	rel, err := l.musicRel(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	var dur float64
	if err := l.DB.QueryRow(`SELECT dur FROM tracks WHERE path = ?`, rel).Scan(&dur); err != nil {
		return err
	}
	// shortcut: la conversión no pasa por Sem (2 cupos, compartido con las carátulas): retenerlo toda una descarga
	// a un celular lento dejaría a todos sin carátulas. Si hay muchas pistas que no son MP3 a 48 kHz, convertirlas en disco.
	src, _, err := l.OpenStream(rel, dur, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "private, no-store")
	_, err = io.Copy(w, src)
	return err
}
