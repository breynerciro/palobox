package music

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"palobox/internal/httpx"
)

// sounds reduce la ortografía a cómo suena en español: "Alkolirykos" y "Alcolirykoz" quedan iguales.
// "ch" se deja tal cual para que la "h" muda no la deshaga; el Replacer no relee lo que reemplaza.
var sounds = strings.NewReplacer("ch", "ch", "ph", "f", "qu", "k", "ce", "se", "ci", "si", "ll", "i",
	"c", "k", "q", "k", "z", "s", "v", "b", "y", "i", "h", "")

func sound(s string) string {
	var b strings.Builder
	var last rune
	for _, r := range sounds.Replace(fold(s)) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			r = ' '
		}
		if r == last && !unicode.IsDigit(r) { // "Alcolirykozz", "Weeknd"/"Weekend": las letras dobles no cuentan
			continue
		}
		b.WriteRune(r)
		last = r
	}
	return b.String()
}

type term struct{ plain, key string }

// query es lo que escribió el usuario, ya en las dos formas con que se compara.
type query []term

func newQuery(q string) query {
	out := query{}
	for _, w := range strings.Fields(fold(q)) {
		out = append(out, term{w, strings.ReplaceAll(sound(w), " ", "")})
	}
	return out
}

// norm es un texto en las dos formas con que se compara; la instantánea lo guarda ya calculado
// porque fold y sound de toda la biblioteca en cada búsqueda eran casi todo su costo.
type norm struct{ plain, key string }

func normOf(s string) norm { return norm{fold(s), sound(s)} }

// matchNorm dice si cada palabra aparece en el texto y cuánto hubo que adivinar: 0 si está tal cual o suena
// igual, más por cada letra de diferencia. Las palabras cortas no toleran errores: "amor" no debe traer "amar".
func (q query) matchNorm(s norm) (int, bool) {
	total := 0
	for _, t := range q {
		// Contenida en el sonido es distancia 0: ahorra approx, que es lo caro.
		if strings.Contains(s.plain, t.plain) || strings.Contains(s.key, t.key) {
			continue
		}
		tol := 0
		if n := len([]rune(t.key)); n >= 8 {
			tol = 2
		} else if n >= 5 {
			tol = 1
		}
		if tol == 0 {
			return 0, false
		}
		d := approx(t.key, s.key, tol)
		if d > tol {
			return 0, false
		}
		total += d
	}
	return total, true
}

// approx es la menor distancia de edición entre p y cualquier pedazo de t (algoritmo de Sellers), o
// limit+1 si pasa de limit. El mínimo de una fila nunca baja en las siguientes: en cuanto supera limit
// se deja de calcular, y casi todas las pistas de una búsqueda se descartan así en pocas filas.
func approx(p, t string, limit int) int {
	a, b := []rune(p), []rune(t)
	row := make([]int, len(b)+1) // fila 0 en ceros: p puede empezar en cualquier parte de t
	for i := 1; i <= len(a); i++ {
		diag := row[0]
		row[0] = i
		low := i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur := min(row[j]+1, row[j-1]+1, diag+cost)
			diag, row[j] = row[j], cur
			low = min(low, cur)
		}
		if low > limit {
			return limit + 1
		}
	}
	return slices.Min(row)
}

// best deja lo que coincide con q, primero lo más exacto, hasta n (0 = todo); text dice dónde buscar.
// A igual distancia gana el menor rank (nil = sin preferencia) y luego el orden de entrada.
func best[T any](q query, items []T, n int, text func(T) norm, rank func(T) int) []T {
	type hit struct {
		it   T
		d, r int
	}
	hits := []hit{}
	for _, it := range items {
		if d, ok := q.matchNorm(text(it)); ok {
			h := hit{it: it, d: d}
			if rank != nil {
				h.r = rank(it)
			}
			hits = append(hits, h)
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Or(a.d-b.d, a.r-b.r) })
	if n > 0 && len(hits) > n {
		hits = hits[:n]
	}
	out := make([]T, len(hits))
	for i, h := range hits {
		out[i] = h.it
	}
	return out
}

// Search busca en el índice: cada palabra debe aparecer, sin importar tildes, mayúsculas ni ortografía
// ("alkolirykos" encuentra "Alcolirykoz"), y tolera algún error de tecleo en palabras largas.
// shortcut: recorre en Go toda la instantánea (ya normalizada) en cada búsqueda; sobra con el tope de
// MUSIC_MAX, pero con cientos de miles de pistas haría falta un índice de trigramas o similar.
func (l *Library) Search(w http.ResponseWriter, r *http.Request, u httpx.User) error {
	l.fresh()
	out := map[string]any{"tracks": []track{}, "albums": []album{}, "artists": []artist{}, "playlists": []playlist{}}
	raw := fold(r.URL.Query().Get("q"))
	if len(strings.Join(strings.Fields(raw), "")) < 2 {
		return httpx.WriteJSON(w, 200, out)
	}
	q := newQuery(raw)
	snap, albums, artists, err := l.library(u)
	if err != nil {
		return err
	}
	pls, err := l.playlists(u)
	if err != nil {
		return err
	}
	// Lo que coincide en el título sale antes que lo que coincide solo en el artista o el álbum.
	inTitle := func(t track) int {
		if _, ok := q.matchNorm(t.titleText); ok {
			return 0
		}
		return 1
	}
	out["tracks"] = best(q, snap.tracks, 30, func(t track) norm { return t.text }, inTitle)
	out["albums"] = best(q, albums, 12, func(a album) norm { return a.text }, nil)
	out["artists"] = best(q, artists, 8, func(a artist) norm { return a.text }, nil)
	out["playlists"] = best(q, pls, 0, func(p playlist) norm { return normOf(p.Name) }, nil)
	return httpx.WriteJSON(w, 200, out)
}
