package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		r, l string
		want bool
	}{
		{"v0.3.1", "0.3.0", true},
		{"v0.3.0", "0.3.0", false},
		{"v0.3.0", "0.3.0-test264", true},
		{"v0.2.9", "0.3.0-test264", false},
		{"v0.10.0", "0.9.9", true},
		{"v1.0.0", "dev", false},
		{"nightly", "0.3.0", false},
		{"v0.3.0-beta", "0.3.0-test1", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.r, c.l); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.r, c.l, got)
		}
	}
}

func TestUpdateCheckAndDownload(t *testing.T) {
	exe := append([]byte("MZ"), make([]byte, 5000)...)
	sum := sha256.Sum256(exe)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.0.0","body":"notes","assets":[{"name":"PresetWardrobe.exe","browser_download_url":"%s/exe","size":%d},{"name":"SHA256SUMS.txt","browser_download_url":"%s/sums"}]}`, srv.URL, len(exe), srv.URL)
		case "/exe":
			w.Write(exe)
		case "/sums":
			fmt.Fprintf(w, "%x  PresetWardrobe.exe\n", sum)
		}
	}))
	defer srv.Close()
	releasesAPI = srv.URL + "/latest"
	old := appVersion
	appVersion = "0.3.0-test1"
	defer func() { appVersion = old }()
	v, url, size, digest, sums, err := fetchLatest()
	if err != nil || !v.Available || v.Version != "9.0.0" || digest != "" {
		t.Fatalf("%+v %v", v, err)
	}
	if sumFromList(sums) != hex.EncodeToString(sum[:]) {
		t.Fatal("sums")
	}
	dst := filepath.Join(t.TempDir(), "x.exe")
	if err := download(url, dst, size); err != nil {
		t.Fatal(err)
	}
	if got, _ := fileSHA256(dst); got != hex.EncodeToString(sum[:]) {
		t.Fatal("hash")
	}
}
