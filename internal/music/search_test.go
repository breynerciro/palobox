package music

import "testing"

func TestQueryMatch(t *testing.T) {
	for _, c := range []struct {
		q, s string
		ok   bool
	}{
		{"Alkolirykos", "Anarcolirykoz Alcolirykoz", true}, // ortografía distinta, suena igual
		{"alcolirikos", "Alcolirykoz", true},
		{"alcolirykz", "Alcolirykoz", true},              // una letra de menos en palabra larga
		{"cancion", "Canción triste", true},              // tildes
		{"villa", "Biya", true},                          // v/b, ll/y
		{"amar", "Amor eterno", false},                   // palabra corta: sin tolerancia
		{"queen rock", "Bohemian Rhapsody Queen", false}, // todas las palabras deben estar
		{"chico", "Hico", false},                         // la "ch" no se vuelve "c"
	} {
		if _, ok := newQuery(c.q).matchNorm(normOf(c.s)); ok != c.ok {
			t.Errorf("%q en %q: %v, quería %v", c.q, c.s, ok, c.ok)
		}
	}
}
