package main

// Miniaturas y fecha de captura, con vips y ffmpeg.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// takenAt lee la fecha de captura de los metadatos; cero si no la hay.
func takenAt(path, kind string) time.Time {
	// Un archivo corrupto no puede dejar colgada la subida.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if kind == "image" {
		out, _ := exec.CommandContext(ctx, "vipsheader", "-f", "exif-ifd2-DateTimeOriginal", path).Output()
		if len(out) >= 19 {
			if t, err := time.ParseInLocation("2006:01:02 15:04:05", string(out[:19]), time.Local); err == nil {
				return t
			}
		}
		return time.Time{}
	}
	out, _ := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format_tags", "-of", "default=nw=1", path).Output()
	var t time.Time
	for _, l := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
		switch k {
		case "TAG:com.apple.quicktime.creationdate": // la captura real; creation_time la reescribe iOS al exportar
			if p, err := time.Parse("2006-01-02T15:04:05-0700", v); err == nil {
				return p
			}
		case "TAG:creation_time":
			if p, err := time.Parse(time.RFC3339Nano, v); err == nil {
				t = p
			}
		}
	}
	return t
}

// thumb genera (una vez) la versión v del medio: "t" miniatura, "p" vista previa.
func (a *app) thumb(sha, src, kind, v string) (string, error) {
	dir := filepath.Join(a.data, ".palobox", "thumbs")
	dst := filepath.Join(dir, sha+"_"+v+".jpg")
	// Antes del cupo: una miniatura que ya existe no espera a que ffmpeg termine con un video recién subido.
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	a.sem <- struct{}{}
	defer func() { <-a.sem }()
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	// Marca de "sin imagen" (p. ej. una canción sin carátula): evita relanzar ffmpeg en cada petición.
	if _, err := os.Stat(dst + ".none"); err == nil {
		return "", errNoThumb
	}
	f, err := os.CreateTemp(dir, "*.jpg")
	if err != nil {
		return "", err
	}
	f.Close()
	defer os.Remove(f.Name())
	size := map[string]string{"t": "400", "p": "2048"}[v]
	ff := func(args ...string) []string {
		return append(append([]string{"ffmpeg", "-y", "-v", "error"}, args...), "-frames:v", "1", "-vf", "scale="+size+":-2", f.Name())
	}
	var cmds [][]string
	limit := 60 * time.Second // un archivo corrupto no puede retener un cupo de a.sem para siempre
	switch kind {
	case "video":
		cmds = [][]string{ff("-i", src)}
	case "audio": // carátula incrustada en el archivo
		cmds = [][]string{ff("-i", src, "-an")}
	default:
		cmds = [][]string{{"vipsthumbnail", src, "--size", size + "x" + size + ">", "-o", f.Name() + "[Q=82]"}}
	}
	for _, c := range cmds {
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		out, err := exec.CommandContext(ctx, c[0], c[1:]...).CombinedOutput()
		cancel()
		if err != nil && kind != "audio" {
			return "", fmt.Errorf("miniatura de %s: %v: %s", src, err, out)
		}
		if st, err := os.Stat(f.Name()); err == nil && st.Size() > 0 {
			return dst, os.Rename(f.Name(), dst)
		}
	}
	os.WriteFile(dst+".none", nil, 0o644)
	return "", errNoThumb
}

var errNoThumb = fs.ErrNotExist
