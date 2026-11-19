package music

import (
	"slices"
	"strings"
	"unicode"
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

func (q query) match(s string) (int, bool) { return q.matchNorm(normOf(s)) }

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
