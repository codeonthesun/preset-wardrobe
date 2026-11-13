package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalHostOnly(t *testing.T) {
	h := localHostOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for host, want := range map[string]int{
		"127.0.0.1:5000": 200, "localhost:5000": 200, "LOCALHOST": 200, "[::1]:5000": 200, "127.0.0.1": 200,
		"evil.example.com:5000": 403, "127.0.0.1.evil.com:5000": 403, "192.168.1.5:5000": 403, "": 403,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("host %q: got %d, want %d", host, w.Code, want)
		}
	}
}

func TestSuperviseRestartsAfterPanic(t *testing.T) {
	n := make(chan int, 4)
	calls := 0
	supervise("test", func() {
		calls++
		n <- calls
		if calls == 1 {
			panic("boom")
		}
	})
	deadline := time.After(6 * time.Second)
	for got := 0; got < 2; got++ {
		select {
		case <-n:
		case <-deadline:
			t.Fatal("not started again after a panic")
		}
	}
}

func TestDrainOnPanic(t *testing.T) {
	jobs := make(chan int)
	done := make(chan bool)
	go func() {
		defer func() { done <- true }()
		defer drainOnPanic("test", jobs)
		panic("boom")
	}()
	go func() {
		for i := 0; i < 5; i++ {
			jobs <- i
		}
		close(jobs)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker didn't finish")
	}
}

func TestUnreadableConfigIsKept(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &App{dataDir: dir, localApp: dir, lastSeen: time.Now()}
	a.load()
	files, _ := filepath.Glob(filepath.Join(dir, "config.json.bad-*"))
	if len(files) != 1 {
		t.Fatalf("expected the damaged file to be kept, found %v", files)
	}
	if b, _ := os.ReadFile(files[0]); string(b) != "{ not json" {
		t.Errorf("kept file changed: %q", b)
	}
}
