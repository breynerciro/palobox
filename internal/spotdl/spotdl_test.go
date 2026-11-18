package spotdl

import (
	"strings"
	"testing"
)

func TestSpotifyURL(t *testing.T) {
	ok := map[string]string{
		"https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC":                  "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
		" https://open.spotify.com/intl-es/track/4uLU6hMCjMI75M1A2tKUQC?si=abc ": "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
		"https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX#frag":             "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX",
		"https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M":               "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
		"https://open.spotify.com/artist/06HL4z0CvFAxyc27GXpf02":                 "https://open.spotify.com/artist/06HL4z0CvFAxyc27GXpf02",
	}
	for raw, want := range ok {
		if got, err := spotifyURL(raw); err != nil || got != want {
			t.Errorf("spotifyURL(%q) = %q, %v; quiero %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"",
		"http://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
		"https://open.spotify.com.evil.example/track/4uLU6hMCjMI75M1A2tKUQC",
		"https://evil.example/open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
		"https://open.spotify.com/track/corto",
		"https://open.spotify.com/episode/4uLU6hMCjMI75M1A2tKUQC",
		"https://open.spotify.com/search/daft%20punk",
		"daft punk - one more time",
		"https://open.spotify.com/track/" + strings.Repeat("a", 600),
	} {
		if got, err := spotifyURL(raw); err == nil {
			t.Errorf("spotifyURL(%q) debía fallar, dio %q", raw, got)
		}
	}
}
