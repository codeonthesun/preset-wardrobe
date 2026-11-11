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
	afterUpdate()
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
	supervise("watchKeyRequests", app.watchKeyRequests)
	supervise("watchTimeline", app.watchTimeline)
	app.note("info", "Preset Wardrobe %s started", appVersion)
	supervise("watchGame", app.watchGame)
	supervise("watchPending", app.watchPending)
	supervise("watchRefresh", app.watchRefresh)
	supervise("watchSync", app.watchSync)
	liveCachePath = filepath.Join(data, "live_cache.json")
	itemDir = app.pluginDataDir
	loadItemStatus(app.dir("item_status.json"))
	supervise("watchItems", app.watchItems)
	supervise("watchWarm", app.watchWarm)
	supervise("watchSkaterNow", app.watchSkaterNow)
	supervise("watchHQ", app.watchHQ)
	supervise("watchFitStatus", app.watchFitStatus)
	supervise("watchHotkeys", app.watchHotkeys)
	go func() { defer recoverLog("update check"); app.watchUpdates() }()
	go func() { // a game left silent by an earlier refresh gets its sound back
		defer recoverLog("sound restore")
		time.Sleep(3 * time.Second)
		if gameRunning() {
			restoreGameSound()
		}
	}()
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
	srv := &http.Server{Handler: localHostOnly(app.routes(ui)), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)

	showWindow(url, app, data)
}
