package music

// Géneros por artista. Los MP3 que baja spotDL no traen género, así que sale de dos capas: la lista hecha a mano
// de genres.json, que manda, y para los demás lo que se averigua solo (tabla artist_genre): Gemini al llegar un
// artista nuevo o la playlist con la que llegó.

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"
)

//go:embed genres.json
var genresJSON []byte

// «familia/género» → artistas (carpetas). Genres es la lista cerrada de géneros, en orden.
var (
	baseGenre     = map[string]string{}
	genreExamples = map[string][]string{}
	// Como inicializador y no en init(): el esquema y las instrucciones de Gemini (variables de paquete) la necesitan ya cargada.
	Genres = func() []string {
		var by map[string][]string
		if err := json.Unmarshal(genresJSON, &by); err != nil {
			panic("genres.json: " + err.Error())
		}
		var out []string
		for g, artists := range by {
			out = append(out, g)
			genreExamples[g] = artists[:min(3, len(artists))]
			for _, a := range artists {
				baseGenre[fold(a)] = g
			}
		}
		sort.Strings(out)
		return out
	}()
)

// genres arma artista → género para la instantánea.
func (l *Library) genres() (map[string]string, error) {
	out := make(map[string]string, len(baseGenre))
	for a, g := range baseGenre {
		out[a] = g
	}
	rows, err := l.DB.Query(`SELECT artist, genre FROM artist_genre`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, g string
		if err := rows.Scan(&a, &g); err != nil {
			return nil, err
		}
		if g != "" && out[a] == "" {
			out[a] = g
		}
	}
	return out, rows.Err()
}

// genreOf es el género de una pista por la carpeta de su artista (el primer tramo de la ruta).
func (s *snapshot) genreOf(path string) string {
	dir, _, _ := strings.Cut(path, "/")
	return s.genre[fold(dir)]
}

// GenreList da cada artista con su género y de dónde salió (lista, gemini, playlist o vacío si nadie lo ha clasificado).
func (l *Library) GenreList() (map[string]any, error) {
	s, err := l.snap()
	if err != nil {
		return nil, err
	}
	source := map[string]string{}
	rows, err := l.DB.Query(`SELECT artist, source FROM artist_genre`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, src string
		rows.Scan(&a, &src)
		source[a] = src
	}
	type row struct {
		Name   string `json:"name"`
		Genre  string `json:"genre"`
		Source string `json:"source"`
		Tracks int    `json:"tracks"`
	}
	out := []row{}
	for _, ar := range s.artists {
		a := fold(ar.Path)
		src := source[a]
		if _, ok := baseGenre[a]; ok {
			src = "lista"
		}
		out = append(out, row{ar.Path, s.genre[a], src, ar.Tracks})
	}
	return map[string]any{"genres": Genres, "artists": out, "ai": l.Ask != nil}, rows.Err()
}

// Clasificación con Gemini

const unknownGenre = "desconocido"

// Lo que distingue a los géneros que se confunden (medido: sin esto Flash-Lite manda merengues y salsas a
// «tropical» y el pop latino a «balada»).
var genreNotes = map[string]string{
	"tropical/tropical":   "cumbia, porro y orquestas tropicales bailables; NO merengue, salsa, bachata ni vallenato, que tienen su género",
	"tropical/merengue":   "incluye tecnomerengue venezolano y merenhouse",
	"pop/balada":          "balada romántica clásica y bolero (José Luis Perales, Leo Dan); el pop romántico actual es pop-latino",
	"pop/pop-latino":      "pop en español, incluido el romántico y el tropipop",
	"pop/pop":             "pop que no es en español",
	"popular/popular":     "música popular colombiana: despecho, carrilera, parrandera",
	"popular/ranchera":    "ranchera y mariachi",
	"rap/rap-espanol":     "rap en español, de cualquier país",
	"rap/hip-hop":         "rap y R&B que no son en español",
	"rock/rock-espanol":   "rock y pop rock en español",
	"rock/rock":           "rock y pop rock que no son en español",
	"regional/corridos":   "corridos tumbados y sierreño",
	"regional/grupero":    "grupero y tejano romántico",
	"urbano/regueton":     "reguetón, trap latino, dembow y pop urbano",
	"electronica/dance":   "electrónica, dance y eurodance",
	"cristiana/cristiana": "música cristiana en cualquier ritmo",
}

var genreSystem = func() string {
	var b strings.Builder
	b.WriteString("Clasificas artistas musicales de una biblioteca de música en Colombia. Para cada artista eliges UN género de la lista " +
		"(formato familia/género) o «" + unknownGenre + "». El álbum y la canción de muestra son pistas: el mismo nombre puede ser de artistas " +
		"distintos, y un artista se clasifica por lo que hay de él en la biblioteca. Si no reconoces al artista y las pistas no lo dejan claro, " +
		"responde «" + unknownGenre + "»: no adivines.\n\nGéneros:\n")
	for _, g := range Genres {
		b.WriteString("- " + g)
		if n := genreNotes[g]; n != "" {
			b.WriteString(": " + n)
		}
		b.WriteString(". Ej.: " + strings.Join(genreExamples[g], ", ") + "\n")
	}
	return b.String()
}()

var genreSchema = map[string]any{"type": "ARRAY", "items": map[string]any{"type": "OBJECT", "required": []string{"i", "g"},
	"properties": map[string]any{"i": map[string]any{"type": "INTEGER"}, "g": map[string]any{"type": "STRING", "enum": append(slices.Clone(Genres), unknownGenre)}}}}

// sample es un artista por clasificar con una muestra de lo que hay de él.
type sample struct{ Artist, Album, Title string }

// askGenres devuelve el género de cada artista, en el mismo orden; "" = Gemini no lo reconoce.
func (l *Library) askGenres(ctx context.Context, hard bool, batch []sample) ([]string, error) {
	var b strings.Builder
	for i, s := range batch {
		fmt.Fprintf(&b, "%d. %s | álbum: %s | canción: %s\n", i, s.Artist, s.Album, s.Title)
	}
	var got []struct {
		I int
		G string
	}
	if err := l.Ask(ctx, hard, genreSystem, b.String(), genreSchema, &got); err != nil {
		return nil, err
	}
	out := make([]string, len(batch))
	for _, x := range got {
		if x.I >= 0 && x.I < len(batch) && slices.Contains(Genres, x.G) { // fuera de la lista = desconocido
			out[x.I] = x.G
		}
	}
	return out, nil
}

// classify le pregunta a Gemini el género de los artistas que nadie ha clasificado, una sola vez por artista:
// la respuesta queda en artist_genre, también cuando es «desconocido», para no volver a preguntar.
// Lo llama cada recorrido del índice; sin artistas nuevos no gasta nada.
func (l *Library) classify() {
	if l.Ask == nil || !l.classMu.TryLock() {
		return
	}
	defer l.classMu.Unlock()
	if time.Now().Before(l.classWait) {
		return
	}
	rows, err := l.DB.Query(`SELECT t.artist_dir, MIN(t.album), MIN(t.title) FROM tracks t WHERE t.artist_dir != '' GROUP BY t.artist_dir`)
	if err != nil {
		log.Print("géneros: ", err)
		return
	}
	done := map[string]bool{}
	var all, pending []sample
	for rows.Next() {
		var s sample
		rows.Scan(&s.Artist, &s.Album, &s.Title)
		all = append(all, s)
	}
	rows.Close()
	if rows, err = l.DB.Query(`SELECT artist FROM artist_genre`); err != nil {
		log.Print("géneros: ", err)
		return
	}
	for rows.Next() {
		var a string
		rows.Scan(&a)
		done[a] = true
	}
	rows.Close()
	for _, s := range all {
		if a := fold(s.Artist); baseGenre[a] == "" && !done[a] {
			pending = append(pending, s)
		}
	}
	if len(pending) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	defer l.rebuild()
	for len(pending) > 0 {
		batch := pending[:min(40, len(pending))]
		pending = pending[len(batch):]
		got, err := l.askGenres(ctx, false, batch)
		if err != nil {
			log.Print("géneros: ", err)
			l.classWait = time.Now().Add(10 * time.Minute)
			return
		}
		// Segunda opinión del modelo más capaz para los que el liviano no reconoció, en una sola solicitud.
		var doubt []sample
		var at []int
		for i, g := range got {
			if g == "" {
				doubt, at = append(doubt, batch[i]), append(at, i)
			}
		}
		if len(doubt) > 0 {
			if again, err := l.askGenres(ctx, true, doubt); err == nil {
				for j, g := range again {
					got[at[j]] = g
				}
			}
		}
		for i, s := range batch {
			if _, err := l.DB.Exec(`INSERT OR IGNORE INTO artist_genre(artist, genre, source) VALUES(?, ?, 'gemini')`, fold(s.Artist), got[i]); err != nil {
				log.Print("géneros: ", err)
			}
		}
		log.Printf("géneros: %d artistas clasificados con Gemini, %d sin reconocer", len(batch), len(doubt))
	}
}

// inherit le pone a los artistas sin género de una playlist recién descargada el género de la mayoría de sus
// canciones, si es claro (7 de cada 10 entre al menos 5 conocidas): una playlist casi siempre es de un solo género.
func (l *Library) inherit(paths []string) {
	s, err := l.snap()
	if err != nil {
		return
	}
	count, known := map[string]int{}, 0
	missing := map[string]bool{}
	for _, p := range paths {
		if g := s.genreOf(p); g != "" {
			count[g]++
			known++
		} else if dir, _, ok := strings.Cut(p, "/"); ok {
			missing[fold(dir)] = true
		}
	}
	top := ""
	for g, n := range count {
		if n > count[top] {
			top = g
		}
	}
	if len(missing) == 0 || known < 5 || count[top]*10 < known*7 {
		return
	}
	for a := range missing {
		// No pisa un género que Gemini sí reconoció.
		l.DB.Exec(`INSERT INTO artist_genre(artist, genre, source) VALUES(?, ?, 'playlist')
			ON CONFLICT(artist) DO UPDATE SET genre = excluded.genre, source = excluded.source WHERE artist_genre.genre = ''`, a, top)
	}
	l.rebuild()
}
