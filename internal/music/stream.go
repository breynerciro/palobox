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

// frameLen es el tamaño del cuadro MP3 cuya cabecera está en b (MPEG-1 capa III a 44,1 kHz y en estéreo, lo
// único que entra al flujo sin convertir), o 0 si b no empieza con una cabecera así. Todo lo que va por un mismo
// flujo tiene que ser del mismo tipo: un cambio de frecuencia o de mono a estéreo a mitad de camino rompe el audio.
func frameLen(b []byte) int64 {
	kbps := [16]int64{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xFE != 0xFA || b[2]>>4 == 0 || b[2]>>4 == 15 || (b[2]>>2)&3 != 0 || b[3]>>6 == 3 {
		return 0
	}
	return 144000*kbps[b[2]>>4]/44100 + int64((b[2]>>1)&1)
}

// mp3Body delimita el audio de un MP3: después de la etiqueta ID3v2 (con la carátula) y del cuadro de
// información de LAME, y antes de la etiqueta ID3v1. Ese cuadro no trae audio: declara cuántos cuadros tiene el
// archivo, y un reproductor que le crea daría el flujo por terminado al llegar a la duración de la primera
// canción. ok es falso si el archivo no es MPEG-1 capa III a 44,1 kHz: ese hay que convertirlo.
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
	cancel context.CancelFunc
	cmd    *exec.Cmd
}

func (p procCloser) Close() error {
	p.cancel()
	p.ReadCloser.Close()
	return p.cmd.Wait()
}

// OpenStream abre el audio de una pista desde el segundo at para el flujo continuo y dice a cuántos bytes por
// segundo va. Desde el principio, un MP3 del tipo común sale tal cual. Lo demás lo convierte ffmpeg sobre la
// marcha: otros formatos (FLAC, M4A, MP3 a 48 kHz), porque en un mismo flujo no se puede cambiar de formato, y
// cualquier pista que se abre a mitad de camino (adelantar, reabrir el flujo), porque cada cuadro MP3 usa datos
// del anterior y empezar en uno suelto suena como un ruido; ffmpeg entrega un comienzo limpio.
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
	// Menos de una décima de segundo es «desde el principio»: el flujo se abre un instante después del salto.
	if at < 0.1 {
		at = 0
		if start, end, ok := mp3Body(f, st.Size()); ok && dur > 0 && strings.EqualFold(filepath.Ext(path), ".mp3") {
			return sectionCloser{io.NewSectionReader(f, start, end-start), f}, float64(end-start) / dur, nil
		}
	}
	f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", full,
		"-vn", "-map_metadata", "-1", "-ac", "2", "-ar", "44100", "-b:a", "128k", "-write_xing", "0", "-id3v2_version", "0", "-f", "mp3", "-")
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		return nil, 0, err
	}
	return procCloser{out, cancel, cmd}, StreamRate, nil
}

// Offline entrega una pista lista para guardarse en el celular y sonar sin conexión: solo cuadros MP3 en estéreo a
// 44,1 kHz, sin etiquetas, igual que en el flujo continuo. Así varias pistas guardadas se pueden unir una tras
// otra en un solo audio dentro del teléfono, que es lo que deja sonar la siguiente con la pantalla bloqueada.
// PRUEBA (etapa 1 del modo sin conexión): ?secs= corta la pista, para oír varios cambios sin esperar canciones enteras.
func (l *Library) Offline(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	rel, err := l.musicRel(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	var dur float64
	if err := l.DB.QueryRow(`SELECT dur FROM tracks WHERE path = ?`, rel).Scan(&dur); err != nil {
		return err
	}
	src, rate, err := l.OpenStream(rel, dur, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "private, no-store")
	var body io.Reader = src
	if secs, _ := strconv.ParseFloat(r.URL.Query().Get("secs"), 64); secs > 0 {
		body = io.LimitReader(src, int64(secs*rate))
	}
	_, err = io.Copy(w, body)
	return err
}
