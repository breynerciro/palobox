package music

// Lectura de un M4A (AAC dentro de MP4) para el flujo continuo. Un MP4 no se puede mandar a pedazos uno tras otro:
// lleva al principio o al final un índice (moov) que dice dónde está cada cuadro de audio. Aquí se lee ese índice y
// el audio se entrega como cuadros ADTS (cada cuadro con su cabecera de 7 bytes), que sí van seguidos por una sola
// respuesta y se pueden empezar en cualquier cuadro. No se decodifica ni se convierte nada.

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

const (
	aacFrame = 1024    // muestras de audio por cuadro AAC
	maxMoov  = 8 << 20 // un índice más grande que esto no es de una canción
)

var errM4A = errors.New("m4a: no se pudo leer el índice")

// m4a es el índice de un archivo: dónde empieza y cuánto mide cada cuadro, y de qué tipo es el audio.
type m4a struct {
	offsets  []int64
	sizes    []uint32
	objType  int // 2 = AAC-LC
	freqIdx  int // 4 = 44,1 kHz
	channels int
	bytes    int64 // suma de sizes
}

// plain: es del tipo que va por el flujo tal cual (AAC-LC a StreamHz en estéreo); lo demás se convierte.
func (m *m4a) plain() bool {
	return m.objType == 2 && m.freqIdx == 4 && m.channels == 2 && len(m.sizes) > 0
}

// rate son los bytes por segundo del audio ya con las cabeceras ADTS.
func (m *m4a) rate() float64 {
	n := float64(len(m.sizes))
	return (float64(m.bytes) + 7*n) / (n * aacFrame / StreamHz)
}

// box es una caja de MP4 dentro de b: su tipo y su contenido.
type box struct {
	kind string
	data []byte
}

// boxes parte b en sus cajas. Una caja con el tamaño mal puesto termina la lectura: lo leído hasta ahí vale.
func boxes(b []byte) []box {
	var out []box
	for len(b) >= 8 {
		n, head := int64(binary.BigEndian.Uint32(b)), int64(8)
		switch n {
		case 0:
			n = int64(len(b))
		case 1:
			if len(b) < 16 {
				return out
			}
			n, head = int64(binary.BigEndian.Uint64(b[8:])), 16
		}
		if n < head || n > int64(len(b)) {
			return out
		}
		out = append(out, box{string(b[4:8]), b[head:n]})
		b = b[n:]
	}
	return out
}

func find(b []byte, path ...string) []byte {
	for _, name := range path {
		var next []byte
		for _, x := range boxes(b) {
			if x.kind == name {
				next = x.data
				break
			}
		}
		if next == nil {
			return nil
		}
		b = next
	}
	return b
}

// readM4A lee el índice del archivo: busca la caja moov (va al principio o al final, después del audio) y de ella
// saca la pista de sonido.
func readM4A(f *os.File) (*m4a, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var moov []byte
	for off := int64(0); off+8 <= st.Size(); {
		var h [16]byte
		if _, err := f.ReadAt(h[:8], off); err != nil {
			return nil, err
		}
		n, head := int64(binary.BigEndian.Uint32(h[:])), int64(8)
		switch n {
		case 0:
			n = st.Size() - off
		case 1:
			if _, err := f.ReadAt(h[8:], off+8); err != nil {
				return nil, err
			}
			n, head = int64(binary.BigEndian.Uint64(h[8:])), 16
		}
		if n < head || off+n > st.Size() {
			return nil, errM4A
		}
		if string(h[4:8]) == "moov" {
			if n-head > maxMoov {
				return nil, errM4A
			}
			moov = make([]byte, n-head)
			if _, err := f.ReadAt(moov, off+head); err != nil {
				return nil, err
			}
			break
		}
		off += n
	}
	for _, t := range boxes(moov) {
		if t.kind != "trak" {
			continue
		}
		if h := find(t.data, "mdia", "hdlr"); len(h) < 12 || string(h[8:12]) != "soun" {
			continue
		}
		return readTrack(find(t.data, "mdia", "minf", "stbl"))
	}
	return nil, errM4A
}

// readTrack arma el índice con las tablas de la pista: tamaños (stsz), en qué trozo va cada cuadro (stsc) y dónde
// empieza cada trozo (stco o co64).
func readTrack(stbl []byte) (*m4a, error) {
	stsd, stsz, stsc := find(stbl, "stsd"), find(stbl, "stsz"), find(stbl, "stsc")
	stco, wide := find(stbl, "stco"), false
	if stco == nil {
		stco, wide = find(stbl, "co64"), true
	}
	if len(stsz) < 12 || len(stsc) < 8 || len(stco) < 8 {
		return nil, errM4A
	}
	m := &m4a{}
	if !m.config(stsd) {
		return nil, errM4A
	}
	u32 := binary.BigEndian.Uint32
	fixed, n := u32(stsz[4:]), int(u32(stsz[8:]))
	if n <= 0 || (fixed == 0 && len(stsz) < 12+4*n) {
		return nil, errM4A
	}
	m.sizes = make([]uint32, n)
	for i := range m.sizes {
		m.sizes[i] = fixed
		if fixed == 0 {
			m.sizes[i] = u32(stsz[12+4*i:])
		}
		m.bytes += int64(m.sizes[i])
	}
	width := 4
	if wide {
		width = 8
	}
	chunks, runs := int(u32(stco[4:])), int(u32(stsc[4:]))
	if len(stco) < 8+width*chunks || len(stsc) < 8+12*runs || runs == 0 {
		return nil, errM4A
	}
	m.offsets = make([]int64, 0, n)
	run, per := 0, 0
	for c := 0; c < chunks && len(m.offsets) < n; c++ {
		// Cada entrada de stsc vale desde su trozo (se cuentan desde 1) hasta el de la entrada siguiente.
		for run < runs && int(u32(stsc[8+12*run:])) == c+1 {
			per = int(u32(stsc[8+12*run+4:]))
			run++
		}
		off := int64(u32(stco[8+width*c:]))
		if wide {
			off = int64(binary.BigEndian.Uint64(stco[8+width*c:]))
		}
		for k := 0; k < per && len(m.offsets) < n; k++ {
			m.offsets = append(m.offsets, off)
			off += int64(m.sizes[len(m.offsets)-1])
		}
	}
	if len(m.offsets) != n {
		return nil, errM4A
	}
	return m, nil
}

// config saca de stsd el tipo de audio: perfil, frecuencia y canales, que viajan en la caja esds de la entrada mp4a.
func (m *m4a) config(stsd []byte) bool {
	// La descripción trae campos fijos antes de sus cajas, y cuántos depende de la versión: se busca esds directamente.
	i := indexOf(stsd, "esds")
	if i < 0 {
		return false
	}
	if len(stsd) < i+12 {
		return false
	}
	b := stsd[i+12:] // tras el tamaño, el tipo, y la versión y banderas: descriptores anidados
	// Descriptores: etiqueta (1 byte), longitud (hasta 4 bytes de 7 bits) y contenido. 3 = pista, 4 = decodificador,
	// 5 = su configuración (AudioSpecificConfig), que es lo que se busca.
	for skip := 0; len(b) > 2; {
		tag := b[0]
		b = b[1:]
		n := 0
		for k := 0; k < 4 && len(b) > 0; k++ {
			c := b[0]
			b = b[1:]
			n = n<<7 | int(c&0x7f)
			if c&0x80 == 0 {
				break
			}
		}
		switch tag {
		case 3:
			skip = 3 // identificador (2) y banderas (1); lo que sigue son descriptores anidados
		case 4:
			skip = 13 // tipo de objeto, tipo de flujo, colchón y tasas
		case 5:
			if len(b) < 2 {
				return false
			}
			m.objType = int(b[0] >> 3)
			m.freqIdx = int(b[0]&7)<<1 | int(b[1]>>7)
			m.channels = int(b[1] >> 3 & 15)
			return true
		default:
			skip = n
		}
		if skip > len(b) {
			return false
		}
		b = b[skip:]
	}
	return false
}

func indexOf(b []byte, s string) int {
	for i := 4; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i - 4 // donde empieza la caja (su tamaño)
		}
	}
	return -1
}

// adts entrega los cuadros del archivo desde el cuadro next, cada uno con su cabecera ADTS.
type adts struct {
	f    *os.File
	m    *m4a
	next int
	buf  []byte // cuadros ya armados que aún no se han leído
	out  []byte // memoria de buf, que se reutiliza
	raw  []byte
}

func (a *adts) Close() error { return a.f.Close() }

func (a *adts) Read(p []byte) (int, error) {
	if len(a.buf) == 0 {
		if a.next >= len(a.m.sizes) {
			return 0, io.EOF
		}
		if err := a.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, a.buf)
	a.buf = a.buf[n:]
	return n, nil
}

// fill arma en buf los cuadros que siguen: hasta 64, mientras estén seguidos en el archivo, con una sola lectura.
func (a *adts) fill() error {
	m, first := a.m, a.next
	last, span := first, int64(0)
	for last < len(m.sizes) && last-first < 64 && m.offsets[last] == m.offsets[first]+span {
		span += int64(m.sizes[last])
		last++
	}
	if cap(a.raw) < int(span) {
		a.raw = make([]byte, span)
	}
	raw := a.raw[:span]
	if _, err := a.f.ReadAt(raw, m.offsets[first]); err != nil {
		return err
	}
	out := a.out[:0]
	for i := first; i < last; i++ {
		n := int(m.sizes[i])
		full := n + 7
		// Cabecera ADTS: marca de sincronía, MPEG-4 sin CRC, perfil, frecuencia, canales, tamaño del cuadro (13 bits),
		// colchón «variable» (0x7FF) y un solo bloque de audio.
		out = append(out,
			0xFF, 0xF1,
			byte(m.objType-1)<<6|byte(m.freqIdx)<<2|byte(m.channels>>2),
			byte(m.channels&3)<<6|byte(full>>11),
			byte(full>>3),
			byte(full&7)<<5|0x1F,
			0xFC)
		out = append(out, raw[:n]...)
		raw = raw[n:]
	}
	a.buf, a.out, a.next = out, out, last
	return nil
}
