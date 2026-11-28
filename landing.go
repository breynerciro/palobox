package main

import (
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// Página de descarga (/app): la app de Android para el celular y el televisor, y los pasos para el iPhone y el
// computador. Es pública: se abre antes de tener la app o la sesión. Muestra primero lo del dispositivo que la abre.

//go:embed landing.html
var landingHTML string
var landingTmpl = template.Must(template.New("descarga").Parse(landingHTML))

// apkFile: la APK firmada, que viaja en la imagen (Dockerfile); a su lado, apkFile+".version" con su versionCode
// (android/build.sh). Sin ella /apk da 404 y la página no ofrece versión.
var apkFile = "/usr/share/palobox/palobox.apk"

// downloaderCode: el código de la app Downloader (go.aftvnews.com) que apunta a /apk; vacío = solo la dirección.
const downloaderCode = "1374218"

var tvUA = regexp.MustCompile(`(?i)Android TV|GoogleTV|Google TV|SmartTV|BRAVIA|AFT[A-Z]|\bTV\b|leanback`)

// device: qué mostrar primero según el navegador que pide la página.
func device(ua string) string {
	switch {
	case tvUA.MatchString(ua):
		return "tv"
	case strings.Contains(ua, "Android"):
		return "android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "ios"
	}
	return "web"
}

// apkVersion: el versionCode de la APK publicada, o "" si no hay.
func apkVersion() string {
	b, _ := os.ReadFile(apkFile + ".version")
	return strings.TrimSpace(string(b))
}

func landing(w http.ResponseWriter, r *http.Request) {
	size := ""
	if st, err := os.Stat(apkFile); err == nil {
		size = fmt.Sprintf("%.1f MB", float64(st.Size())/1e6)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-cache")
	landingTmpl.Execute(w, map[string]string{"Device": device(r.UserAgent()), "Version": apkVersion(), "Size": size, "Code": downloaderCode})
}

// apk entrega la app de Android. private, no-cache: si no, Cloudflare la guarda 4 h y una versión nueva tardaría en llegar.
func apk(w http.ResponseWriter, r *http.Request) {
	f, err := os.Open(apkFile)
	if err != nil {
		http.Error(w, "La app de Android todavía no está publicada.", 404)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="PaloBox.apk"`)
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "PaloBox.apk", st.ModTime(), f)
}
