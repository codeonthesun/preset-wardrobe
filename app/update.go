package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Updates from the GitHub releases. The latest full release (not a draft or pre-release) is checked when the app
// starts, unless the player turned that off in Settings. When its version is newer than this build's, the header
// shows "Update available". The button downloads its PresetWardrobe.exe, checks it against the release's SHA-256
// (GitHub's digest for the file, or the release's SHA256SUMS.txt), puts it in place of this .exe and starts it.
// Nothing is shown when there's no update or the check fails.

var releasesAPI = "https://api.github.com/repos/codeonthesun/preset-wardrobe/releases/latest"

type updateView struct {
	Available bool    `json:"available"`
	Version   string  `json:"version,omitempty"`
	Notes     string  `json:"notes,omitempty"`
	Page      string  `json:"page,omitempty"`
	State     string  `json:"state,omitempty"` // "", "checking", "downloading", "installing", "failed"
	Progress  float64 `json:"progress,omitempty"`
	Error     string  `json:"error,omitempty"`
	Checked   bool    `json:"checked"` // a check has finished (for Settings' "Check now")
}

var upd struct {
	sync.Mutex
	view   updateView
	exeURL string
	size   int64
	digest string // hex sha256, when GitHub gives it
	sums   string // SHA256SUMS.txt's URL
}

func init() {
	if u := os.Getenv("PB_UPDATE_URL"); u != "" { // for trying an update against a test release
		releasesAPI = u
	}
}

func updateState() updateView {
	upd.Lock()
	defer upd.Unlock()
	return upd.view
}

// semver: "v0.3.1" -> [0 3 1], ""; "0.3.0-test264" -> [0 3 0], "test264". ok false for "dev".
var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

func parseSemver(s string) (n [3]int, pre string, ok bool) {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return n, "", false
	}
	for i := 0; i < 3; i++ {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	return n, m[4], true
}

// newerVersion: whether remote is a later version than local. A test build (0.3.0-test264) is older than its
// release (0.3.0). A build without a version ("dev") never updates.
func newerVersion(remote, local string) bool {
	r, rp, ok1 := parseSemver(remote)
	l, lp, ok2 := parseSemver(local)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if r[i] != l[i] {
			return r[i] > l[i]
		}
	}
	return lp != "" && rp == ""
}

var httpClient = &http.Client{Timeout: 20 * time.Second}

func ghGet(url string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "PresetWardrobe/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	return httpClient.Do(req)
}

// checkUpdate: asks GitHub for the latest release. Quiet: a failure only leaves "no update".
func (a *App) checkUpdate() {
	upd.Lock()
	if upd.view.State == "downloading" || upd.view.State == "installing" || upd.view.State == "checking" {
		upd.Unlock()
		return
	}
	upd.view.State = "checking"
	upd.Unlock()
	view, url, size, digest, sums, err := fetchLatest()
	upd.Lock()
	defer upd.Unlock()
	if err != nil {
		tl.logf("APP: update check: %v", err)
		upd.view = updateView{Checked: true}
		return
	}
	upd.view, upd.exeURL, upd.size, upd.digest, upd.sums = view, url, size, digest, sums
	upd.view.Checked = true
	if view.Available {
		tl.logf("APP: update available: %s", view.Version)
	}
}

func fetchLatest() (v updateView, exeURL string, size int64, digest, sums string, err error) {
	resp, err := ghGet(releasesAPI)
	if err != nil {
		return v, "", 0, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return v, "", 0, "", "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Body       string `json:"body"`
		Page       string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return v, "", 0, "", "", err
	}
	// A pre-release is taken only when PB_UPDATE_URL points at it on purpose (testing an update).
	if rel.Draft || (rel.Prerelease && os.Getenv("PB_UPDATE_URL") == "") || !newerVersion(rel.Tag, appVersion) {
		return v, "", 0, "", "", nil
	}
	for _, as := range rel.Assets {
		switch {
		case strings.EqualFold(as.Name, "PresetWardrobe.exe"):
			exeURL, size = as.URL, as.Size
			digest = strings.ToLower(strings.TrimPrefix(as.Digest, "sha256:"))
		case strings.EqualFold(as.Name, "SHA256SUMS.txt"):
			sums = as.URL
		}
	}
	if exeURL == "" {
		return v, "", 0, "", "", errors.New("the release has no PresetWardrobe.exe")
	}
	notes := strings.TrimSpace(rel.Body)
	if len(notes) > 4000 {
		notes = notes[:4000] + "…"
	}
	return updateView{Available: true, Version: strings.TrimPrefix(rel.Tag, "v"), Notes: notes, Page: rel.Page}, exeURL, size, digest, sums, nil
}

func setUpd(f func(v *updateView)) {
	upd.Lock()
	f(&upd.view)
	upd.Unlock()
}

// installUpdate: downloads, checks and puts the new .exe in place, then starts it; the caller exits.
func (a *App) installUpdate() error {
	upd.Lock()
	if !upd.view.Available || upd.exeURL == "" {
		upd.Unlock()
		return errors.New("there's no update to install")
	}
	if upd.view.State == "downloading" || upd.view.State == "installing" {
		upd.Unlock()
		return errors.New("the update is already on its way")
	}
	url, size, digest, sums, version := upd.exeURL, upd.size, upd.digest, upd.sums, upd.view.Version
	upd.view.State, upd.view.Progress, upd.view.Error = "downloading", 0, ""
	upd.Unlock()

	fail := func(err error) error {
		tl.logf("APP: update to %s failed: %v", version, err)
		setUpd(func(v *updateView) { v.State, v.Error = "failed", err.Error() })
		return err
	}
	if digest == "" && sums != "" {
		digest = sumFromList(sums)
	}
	if len(digest) != 64 {
		return fail(errors.New("the release doesn't say what the file should be (no SHA-256), so it wasn't installed"))
	}
	dir := a.dir("update")
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(err)
	}
	newExe := filepath.Join(dir, "PresetWardrobe.exe")
	if err := download(url, newExe, size); err != nil {
		return fail(err)
	}
	if got, err := fileSHA256(newExe); err != nil || got != digest {
		os.Remove(newExe)
		return fail(errors.New("the download didn't match the release's checksum, so it wasn't installed"))
	}
	setUpd(func(v *updateView) { v.State = "installing" })
	if err := replaceAndRestart(newExe); err != nil {
		return fail(err)
	}
	tl.logf("APP: updated to %s, restarting", version)
	return nil
}

func download(url, dst string, size int64) error {
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("the download answered %s", resp.Status)
	}
	if size <= 0 {
		size = resp.ContentLength
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	var n int64
	buf := make([]byte, 256<<10)
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			if _, err := f.Write(buf[:k]); err != nil {
				f.Close()
				return err
			}
			n += int64(k)
			if size > 0 {
				p := float64(n) / float64(size)
				setUpd(func(v *updateView) { v.Progress = p })
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if size > 0 && n != size {
		return fmt.Errorf("the download stopped early (%d of %d bytes)", n, size)
	}
	head := make([]byte, 2)
	if g, err := os.Open(dst); err == nil {
		io.ReadFull(g, head)
		g.Close()
	}
	if !bytes.Equal(head, []byte("MZ")) {
		return errors.New("the download isn't a Windows program")
	}
	return nil
}

// sumFromList: PresetWardrobe.exe's hash from a SHA256SUMS.txt ("<hex>  PresetWardrobe.exe").
func sumFromList(url string) string {
	resp, err := ghGet(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.EqualFold(strings.TrimPrefix(f[1], "*"), "PresetWardrobe.exe") && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// watchUpdates: the check at start (a few seconds in, so it never slows the start), unless turned off.
func (a *App) watchUpdates() {
	time.Sleep(4 * time.Second)
	a.mu.Lock()
	off := a.cfg.NoUpdateCheck
	a.mu.Unlock()
	if !off {
		a.checkUpdate()
	}
}
