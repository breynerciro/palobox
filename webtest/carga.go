//go:build ignore

// Generador de carga para webtest/carga.sh: -c usuarios, cada uno pidiendo -url a -q peticiones por segundo
// durante -z segundos con su propia conexión. Imprime una línea: peticiones/s, p50, p95, p99, máximo (ms) y errores.
// Se compila estático (CGO_ENABLED=0 go build -o carga webtest/carga.go) y se copia al servidor.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

func main() {
	url := flag.String("url", "", "ruta completa a pedir")
	cookie := flag.String("cookie", "", "cookie de sesión")
	users := flag.Int("c", 50, "usuarios a la vez")
	secs := flag.Int("z", 15, "segundos")
	rate := flag.Float64("q", 1, "peticiones por segundo de cada usuario")
	flag.Parse()
	// Sin descomprimir: el generador comparte CPU con el servidor y solo interesa cuánto tarda la respuesta completa.
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *users, MaxConnsPerHost: *users, DisableCompression: true}}
	var mu sync.Mutex
	var times []time.Duration
	errors := 0
	end := time.Now().Add(time.Duration(*secs) * time.Second)
	every := time.Duration(float64(time.Second) / *rate)
	var wg sync.WaitGroup
	for i := range *users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(every * time.Duration(i) / time.Duration(*users)) // escalonados: no todos en el mismo instante
			for next := time.Now(); next.Before(end); next = next.Add(every) {
				time.Sleep(time.Until(next))
				req, _ := http.NewRequest("GET", *url, nil)
				req.Header.Set("Cookie", *cookie)
				req.Header.Set("Accept-Encoding", "gzip")
				t0 := time.Now()
				res, err := client.Do(req)
				ok := err == nil
				if ok {
					_, err = io.Copy(io.Discard, res.Body)
					res.Body.Close()
					ok = err == nil && res.StatusCode == 200
				}
				d := time.Since(t0)
				mu.Lock()
				if ok {
					times = append(times, d)
				} else {
					errors++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(times) == 0 {
		fmt.Printf("0 0 0 0 0 %d\n", errors)
		return
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	p := func(q float64) int64 { return times[min(len(times)-1, int(q*float64(len(times))))].Milliseconds() }
	fmt.Printf("%.0f %d %d %d %d %d\n", float64(len(times))/float64(*secs), p(.50), p(.95), p(.99), times[len(times)-1].Milliseconds(), errors)
}
