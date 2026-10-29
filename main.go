package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Set by the release build: -ldflags "-X main.appVersion=1.2.3".
var appVersion = "dev"

//go:embed ui/index.html
var uiFS embed.FS

//go:embed all:PresetWardrobe
var pluginFS embed.FS

func pluginZip() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fs.WalkDir(pluginFS, "PresetWardrobe", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := pluginFS.ReadFile(p)
		f, _ := zw.Create(p)
		f.Write(b)
		return nil
	})
	zw.Close()
	return buf.Bytes()
}

func main() {
	local := os.Getenv("PB_LOCALAPPDATA")
	if local == "" {
		local = os.Getenv("LOCALAPPDATA")
	}
	if local == "" {
		local, _ = os.UserCacheDir()
	}
	data := os.Getenv("PB_DATA")
	if data == "" {
		data = filepath.Join(local, "PresetWardrobe")
	}
	os.MkdirAll(data, 0o755)
	logf, _ := os.OpenFile(filepath.Join(data, "PresetWardrobe.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if logf != nil {
		log.SetOutput(logf)
	}

	app := &App{dataDir: data, localApp: local, pluginZip: pluginZip(), lastSeen: time.Now()}
	app.load()
	ui, _ := uiFS.ReadFile("ui/index.html")

	addr := "127.0.0.1:0"
	if p := os.Getenv("PB_PORT"); p != "" {
		addr = "127.0.0.1:" + p
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fatal("Couldn't start: " + err.Error())
	}
	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	log.Println("serving", url)
	srv := &http.Server{Handler: app.routes(ui), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)

	showWindow(url, app, data)
}
