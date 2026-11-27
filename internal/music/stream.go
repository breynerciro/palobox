package music

// Audio para el flujo continuo (jam.Stream): iOS deja que una página siga sonando en segundo plano mientras sea
// el mismo audio, pero a veces bloquea cargar uno nuevo, que es lo que pasa al terminar cada canción. En el flujo
// el servidor manda las canciones una tras otra por una sola respuesta, como una emisora. Aquí se prepara cada
// canción para ir en medio de ese flujo: solo cuadros AAC (ADTS), todos del mismo tipo.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"palobox/internal/httpx"
)

// StreamRate son los bytes por segundo de lo que sale de ffmpeg: AAC a 128 kbps más la cabecera de cada cuadro.
const StreamRate = 16000.0 + 7*StreamHz/aacFrame

// StreamHz es la frecuencia de todo lo que va por el flujo continuo o se descarga para escuchar sin conexión: la del
// AAC que entrega la fuente de la música (44,1 kHz), para que la biblioteca salga tal cual.
const StreamHz = 44100

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

// OpenStream abre el audio de una pista desde el segundo at para el flujo continuo y dice a cuántos bytes por
// segundo va. La biblioteca es AAC en M4A (AAC-LC, 44,1 kHz, estéreo): sus cuadros salen tal cual, desde el
// principio o desde cualquier segundo, sin convertir nada (un cuadro AAC no depende del anterior como en MP3). Lo
// que no sea de ese tipo (otro formato, otra frecuencia) lo convierte ffmpeg sobre la marcha, porque en un mismo
// flujo no se puede cambiar de formato; con maxConv conversiones ya en curso esa también se hace, sin cupo.
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
	}
	if strings.EqualFold(filepath.Ext(path), ".m4a") {
		if m, err := readM4A(f); err == nil && m.plain() {
			return &adts{f: f, m: m, next: min(int(at*StreamHz/aacFrame), len(m.sizes))}, m.rate(), nil
		}
	}
	release := func() {}
	select {
	case convSlots <- struct{}{}:
		release = func() { <-convSlots }
	default:
	}
	f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", full,
		"-vn", "-map_metadata", "-1", "-ac", "2", "-ar", strconv.Itoa(StreamHz), "-c:a", "aac", "-b:a", "128k", "-f", "adts", "-")
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
	if r.URL.Query().Get("fmt") == "opus" {
		return l.offlineOpus(w, r, rel)
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
	w.Header().Set("Content-Type", "audio/aac")
	w.Header().Set("Cache-Control", "private, no-store")
	_, err = io.Copy(w, src)
	return err
}

// opusSlots limita las conversiones a Opus que corren a la vez: cada una ocupa un hilo unos segundos y el servidor
// tiene cuatro, que también atienden el flujo de quienes están oyendo. Las demás descargas esperan su turno.
var opusSlots = make(chan struct{}, 2)

// offlineOpus entrega la pista en Opus a 64 kbps (WebM), la versión compacta para guardar en el celular: pesa la
// mitad que el original con una pérdida pequeña. Varias pistas así no se pueden unir en un solo audio; el
// reproductor las va agregando una tras otra al mismo audio (MSE, player.js). La conversión se hace una vez y
// queda en disco: las descargas siguientes de la misma canción no cuestan procesador.
func (l *Library) offlineOpus(w http.ResponseWriter, r *http.Request, rel string) error {
	full := httpx.Safe(l.musicRoot(), rel)
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		return os.ErrNotExist
	}
	// La llave lleva tamaño y fecha: si el archivo cambia, se convierte de nuevo.
	// shortcut: la copia vieja de un archivo que cambió o se borró se queda en disco; si llega a pesar, borrar las que lleven meses sin leerse.
	sum := sha1.Sum([]byte(rel + "|" + strconv.FormatInt(st.Size(), 10) + "|" + strconv.FormatInt(st.ModTime().UnixNano(), 10)))
	dir := filepath.Join(l.Data, ".palobox", "offline-opus")
	out := filepath.Join(dir, hex.EncodeToString(sum[:])+".webm")
	if _, err := os.Stat(out); err != nil {
		select {
		case opusSlots <- struct{}{}:
			defer func() { <-opusSlots }()
		case <-r.Context().Done():
			return r.Context().Err()
		}
		if _, err := os.Stat(out); err != nil { // otra descarga pudo dejarla lista mientras esta esperaba
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			tmp, err := os.CreateTemp(dir, "*.parcial")
			if err != nil {
				return err
			}
			tmp.Close()
			defer os.Remove(tmp.Name())
			cmd := exec.CommandContext(r.Context(), "ffmpeg", "-v", "error", "-nostdin", "-y", "-i", full,
				"-vn", "-map_metadata", "-1", "-c:a", "libopus", "-b:a", "64k", "-f", "webm", tmp.Name())
			if b, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("convertir %s a Opus: %w: %s", rel, err, bytes.TrimSpace(b))
			}
			if err := os.Rename(tmp.Name(), out); err != nil {
				return err
			}
		}
	}
	f, err := os.Open(out)
	if err != nil {
		return err
	}
	defer f.Close()
	w.Header().Set("Content-Type", "audio/webm")
	w.Header().Set("Cache-Control", "private, no-store")
	_, err = io.Copy(w, f)
	return err
}
