package music

// Limpieza de la biblioteca: Spotify publica la misma canción como sencillo y dentro del
// álbum, y spotDL las baja a carpetas distintas; además un álbum puede quedar partido en
// carpetas que solo cambian en mayúsculas o tildes. Se conserva la versión del álbum y todo
// lo del usuario (me gusta, escuchas, playlists, álbumes guardados) pasa a ella.

import (
	"errors"
	"io/fs"
	"log"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
)

type idxTrack struct {
	path, title, album, artist string
	dur                        float64
	mtime                      int64
}

// dedupe corre al final de Sync, con idxMu tomado y el índice ya igual al disco.
func (l *Library) dedupe() {
	// Cada paso relee el índice: el segundo debe ver las pistas ya movidas por el primero.
	for _, step := range []func([]idxTrack){l.mergeAlbums, l.dropDuplicates} {
		rows, err := l.DB.Query(`SELECT path, title, album_dir, artist_dir, dur, mtime FROM tracks`)
		if err != nil {
			log.Print("limpieza de música: ", err)
			return
		}
		var all []idxTrack
		for rows.Next() {
			var t idxTrack
			rows.Scan(&t.path, &t.title, &t.album, &t.artist, &t.dur, &t.mtime)
			all = append(all, t)
		}
		rows.Close()
		step(all)
	}
	l.repairPlaylists()
}

// mergeAlbums une las carpetas hermanas cuyo nombre solo difiere en mayúsculas o tildes
// en la que tenga más pistas (en empate, la de archivos más antiguos).
func (l *Library) mergeAlbums(all []idxTrack) {
	type dir struct {
		n      int
		oldest int64
		files  []string
	}
	dirs := map[string]*dir{}
	groups := map[string][]string{}
	for _, t := range all {
		if t.artist == "" || t.album == t.artist { // pistas sueltas o directo en la carpeta del artista
			continue
		}
		d := dirs[t.album]
		if d == nil {
			d = &dir{oldest: t.mtime}
			dirs[t.album] = d
			k := path.Dir(t.album) + "/" + fold(path.Base(t.album))
			groups[k] = append(groups[k], t.album)
		}
		d.n++
		d.oldest = min(d.oldest, t.mtime)
		d.files = append(d.files, t.path)
	}
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		sort.Slice(g, func(i, j int) bool {
			a, b := dirs[g[i]], dirs[g[j]]
			if a.n != b.n {
				return a.n > b.n
			}
			return a.oldest < b.oldest
		})
		for _, from := range g[1:] {
			for _, p := range dirs[from].files {
				l.relocate(p, g[0]+"/"+path.Base(p), true)
			}
		}
	}
}

// dropDuplicates borra la misma canción repetida en otro álbum del mismo artista y se queda
// con la que no está en carpeta de sencillo; en empate, la del álbum con más pistas y luego la más antigua.
// Son la misma si duran ±3 s, o a cualquier duración si una es sencillo y la otra no (radio edit,
// otra intro); entre dos álbumes se exige la duración para no mezclar dos "Intro" distintas.
func (l *Library) dropDuplicates(all []idxTrack) {
	size := map[string]int{}
	groups := map[string][]idxTrack{}
	for _, t := range all {
		size[t.album]++
		// Sin duración no se distinguen dos canciones distintas con el mismo título ("Intro").
		if t.artist != "" && t.dur > 0 {
			k := t.artist + "/" + fold(t.title)
			groups[k] = append(groups[k], t)
		}
	}
	// spotDL nombra la carpeta del sencillo como la canción: el álbum gana siempre, aunque tenga una sola pista.
	// Spotify solo llama sencillo a un lanzamiento de 1 a 3 canciones: un álbum que se llama como una de ellas no lo es.
	single := func(t idxTrack) bool { return fold(path.Base(t.album)) == fold(t.title) && size[t.album] <= 3 }
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool {
			if a, b := single(g[i]), single(g[j]); a != b {
				return b
			}
			if a, b := size[g[i].album], size[g[j].album]; a != b {
				return a > b
			}
			return g[i].mtime < g[j].mtime
		})
		var kept []idxTrack
	next:
		for _, t := range g {
			for _, k := range kept {
				if k.album != t.album && (math.Abs(k.dur-t.dur) <= 3 || single(k) != single(t)) {
					l.relocate(t.path, k.path, false)
					continue next
				}
			}
			kept = append(kept, t)
		}
	}
}

// relocate deja la pista old en keep: si keep ya existe, old sobra y se borra; si no y move
// está activo, old se mueve a keep. Solo si el disco cambió se remapea la base.
func (l *Library) relocate(old, keep string, move bool) {
	root := l.musicRoot()
	src, dst := filepath.Join(root, filepath.FromSlash(old)), filepath.Join(root, filepath.FromSlash(keep))
	did := "borrado, queda " + keep
	_, err := os.Stat(dst)
	switch {
	case err == nil:
		if err = os.Remove(src); err == nil {
			l.removed.Add(1)
		}
	case move && errors.Is(err, fs.ErrNotExist):
		did = "movido a " + keep
		err = os.Rename(src, dst)
	}
	if err != nil {
		log.Printf("limpieza de música: %s: %v", old, err)
		return
	}
	log.Printf("limpieza de música: %s %s", old, did)
	os.Remove(filepath.Dir(src)) // falla sin daño si la carpeta aún tiene archivos
	if err := l.remap(old, keep); err != nil {
		log.Printf("limpieza de música: remapear %s: %v", old, err)
	}
}

// remap pasa a keep todo lo que apuntaba a old, sin duplicar filas de un mismo usuario o playlist.
func (l *Library) remap(old, keep string) error {
	oldDir, keepDir := path.Dir(old), path.Dir(keep)
	tx, err := l.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// ?1 old, ?2 keep, ?3 carpeta vieja, ?4 carpeta nueva, ?5 y ?6 sus nombres.
	for _, q := range []string{
		// OR IGNORE deja quieta la fila que chocaría con una que ya apunta a keep; el DELETE la quita.
		`UPDATE OR IGNORE likes SET path = ?2 WHERE path = ?1`,
		`DELETE FROM likes WHERE path = ?1`,
		`UPDATE OR IGNORE playlist_tracks SET path = ?2 WHERE path = ?1`,
		`DELETE FROM playlist_tracks WHERE path = ?1`,
		`UPDATE plays SET path = ?2 WHERE path = ?1`,
		// Si se movió, el archivo conserva tamaño y fecha: el índice no lo vuelve a pasar por ffprobe.
		`UPDATE OR IGNORE tracks SET path = ?2, album_dir = ?4, album = CASE album WHEN ?5 THEN ?6 ELSE album END WHERE path = ?1`,
		`DELETE FROM tracks WHERE path = ?1`,
		// El álbum guardado pasa al que queda solo cuando ya no le quedan pistas.
		`UPDATE OR IGNORE saved SET path = ?4 WHERE kind = 'album' AND path = ?3 AND NOT EXISTS(SELECT 1 FROM tracks WHERE album_dir = ?3)`,
		`DELETE FROM saved WHERE kind = 'album' AND path = ?3 AND NOT EXISTS(SELECT 1 FROM tracks WHERE album_dir = ?3)`,
	} {
		if _, err := tx.Exec(q, old, keep, oldDir, keepDir, path.Base(oldDir), path.Base(keepDir)); err != nil {
			return err
		}
	}
	l.changed = true // remap corre dentro de Sync: al terminar se rehace la instantánea
	return tx.Commit()
}
