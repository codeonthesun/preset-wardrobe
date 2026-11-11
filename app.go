package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- what Preset Wardrobe keeps ---------------------------------------------------------------
//
// %LOCALAPPDATA%\PresetWardrobe\
//   fits\<hash>.json        one saved outfit each: the Fit Catalogue
//   pictures\<hash>.png     its picture (the main one)
//   pictures\<hash>\N.png   frames of its turn, 1 = facing front, for spinning the preview
//   rotations\<id>.json     a set of 10 fits to load into the game's slots
//   pending\<id>.png/.json  snaps that need "which slot was this?"
//   backups\<time>\         the whole game profile, copied before every load

const slotCount = 10

type Config struct {
	GameDir    string           `json:"game_dir"`
	Imported   map[string]int64 `json:"imported_snaps"` // walk-through snap file -> mtime already filed
	LastSingle int              `json:"last_single"`    // newest one-fit snap filed
	AckID      int              `json:"ack_id"`
	Migrated   bool             `json:"migrated"`
	SeenUpTo   time.Time        `json:"catalogue_seen_up_to"` // fits added after this show "New"
	// Board pictures as captured, without the lighting effect (light.go). Off by default: the
	// evened-out copies are shown, and the originals are always kept beside them.
	PlainBoards bool `json:"plain_boards"`
	// After a load into the running game: "ask" (or empty), "game" (go back to skate.) or "stay".
	AfterLoad string `json:"after_load,omitempty"`
	// Skater pictures as captured, without the upscale (upscale.go). Off by default, as above.
	// How a fit's preview looks when its "Save preview?" box is ticked: the zoom, how far it's moved
	// up or down, the turn it's held at, and whether it's showing the board. By fit.
	Views map[string]PreviewView `json:"preview_views,omitempty"`
	// How big a card was dragged to on a screen ("<screen>|<fit or slot>"): columns wide and extra lines shown.
	CardSizes map[string]CardSize `json:"card_sizes,omitempty"`
	// Fits whose pictures were asked for again (requests.go)
	Requests   []PicRequest `json:"picture_requests"`
	RequestSeq int          `json:"picture_request_seq"`
	// Settings > Updates: don't ask GitHub for a newer version when the app starts.
	NoUpdateCheck bool `json:"no_update_check,omitempty"`
}

type CardSize struct {
	W int `json:"w"`
	H int `json:"h"`
}

type Fit struct {
	Hash           string    `json:"hash"`
	Name           string    `json:"name,omitempty"` // empty: named after its main piece
	Added          time.Time `json:"added"`
	Hidden         bool      `json:"hidden,omitempty"` // kept only for a before-load copy
	Favorite       bool      `json:"favorite,omitempty"`
	RecipeFormat   int       `json:"recipe_format"`
	RecipesJSON    string    `json:"recipes_json"`
	ExtensionsJSON string    `json:"extensions_json"`
	// Made by Mix & match: its pictures are copied from two fits (the skater's from one, the board's from the
	// other). MixedStamp is the main picture's modification time when copied; a real snap replaces the picture,
	// and the fit then stops being "mixed".
	Mixed      bool  `json:"mixed,omitempty"`
	MixedStamp int64 `json:"mixed_stamp,omitempty"`
}

type Rotation struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Auto    bool      `json:"auto,omitempty"` // a before-load copy
	Slots   []string  `json:"slots"`          // fit hashes; "" leaves the game's slot as it is
}

type Pending struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Guess      int       `json:"guess"`      // slot number, 1-10
	Candidates []int     `json:"candidates"` // best first
}

// A saved preview look: Z 1 to 5, F -1 to 1 (down to up), N the frame it's held at.
type PreviewView struct {
	Z     float64 `json:"z"`
	F     float64 `json:"f"`
	X     float64 `json:"x"`
	N     int     `json:"n"`
	Board bool    `json:"board"`
}

type App struct {
	mu              sync.Mutex
	enh             *enhancer
	requestsWritten string            // requests.json as last written for the plugin
	outfitsWritten  string            // saved_outfits.json's slots as last written
	mods            map[string]string // item key (lower case) -> the mod it's from
	modsAt          time.Time
	modsPath        string
	dataDir         string
	localApp        string
	cfg             Config
	titles          map[string]string
	names           map[string]string // hash -> automatic name, worked out each refresh
	wantedSig       string
	lastSeen        time.Time
	pluginZip       []byte
	acks            []ackMsg
	act             actLog
}

func (a *App) dir(parts ...string) string {
	return filepath.Join(append([]string{a.dataDir}, parts...)...)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (a *App) load() {
	for _, d := range []string{"fits", "rotations", "pictures", "pending", "backups", "tmp"} {
		os.MkdirAll(a.dir(d), 0o755)
	}
	if err := readJSON(a.dir("config.json"), &a.cfg); err != nil && !os.IsNotExist(err) {
		// a settings file that can't be read is set aside, not written over by the defaults
		a.cfg = Config{}
		bad := a.dir("config.json.bad-" + time.Now().Format("20060102-150405"))
		if os.Rename(a.dir("config.json"), bad) == nil {
			a.note("warn", "Your settings file couldn't be read, so the app started with defaults. The old file was kept as %s.", filepath.Base(bad))
		} else {
			a.note("warn", "Your settings file couldn't be read, so the app started with defaults.")
		}
	}
	if a.cfg.Imported == nil {
		a.cfg.Imported = map[string]int64{}
	}
	a.titles = map[string]string{}
	readJSON(a.dir("titles.json"), &a.titles)
	if !a.cfg.Migrated {
		a.migrateFromBanks()
		a.cfg.Migrated = true
	}
	if a.cfg.GameDir == "" {
		a.cfg.GameDir = detectGameDir()
	}
	// Frames filed before they were lined up on the head get lined up now, once.
	if dirs, _ := os.ReadDir(a.dir("pictures")); dirs != nil {
		for _, d := range dirs {
			if d.IsDir() && hashRe.MatchString(d.Name()) {
				alignDir(a.dir("pictures", d.Name()))
			}
		}
		// Board pictures without an up-to-date cleaned-up copy get one.
		// Before the window opens, so the previews show cleaned up from the start: spread over the
		// computer's cores, and only boards without an up-to-date copy are done.
		var todo []string
		for _, d := range dirs {
			board := a.dir("pictures", d.Name(), "board")
			if d.IsDir() && hashRe.MatchString(d.Name()) && fileExists(filepath.Join(board, "1.png")) &&
				!fileExists(filepath.Join(board, litDir, litMark)) {
				todo = append(todo, board)
			}
		}
		work := make(chan string)
		var wg sync.WaitGroup
		for k := 0; k < max(1, min(runtime.NumCPU(), len(todo))); k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer drainOnPanic("lit", work)
				for board := range work {
					makeLit(board)
				}
			}()
		}
		for _, b := range todo {
			work <- b
		}
		close(work)
		wg.Wait()
	}
	// Enhanced pictures are made on request and kept in memory only. Upscaled copies an earlier test
	// version stored are removed.
	a.enh = newEnhancer(enhanceCacheBytes)
	if dirs, _ := os.ReadDir(a.dir("pictures")); dirs != nil {
		for _, d := range dirs {
			if strings.HasPrefix(d.Name(), ".up-") {
				os.RemoveAll(a.dir("pictures", d.Name()))
			} else if d.IsDir() && hashRe.MatchString(d.Name()) {
				os.RemoveAll(a.dir("pictures", d.Name(), "up"))
			}
		}
	}
	if a.cfg.SeenUpTo.IsZero() {
		a.cfg.SeenUpTo = time.Now() // what's there already isn't new
	}
	a.saveConfig()
}

func (a *App) previewViews() map[string]PreviewView {
	out := make(map[string]PreviewView, len(a.cfg.Views))
	for h, v := range a.cfg.Views {
		out[h] = v
	}
	return out
}

func (a *App) saveConfig() { writeJSON(a.dir("config.json"), a.cfg) }

// ---- where things are -------------------------------------------------------------------------

func (a *App) profileDB() string {
	base := filepath.Join(a.localApp, "ReSkate", "profiles")
	preferred := filepath.Join(base, "offline", "reskate.sqlite3")
	if _, err := os.Stat(preferred); err == nil {
		return preferred
	}
	if m, _ := filepath.Glob(filepath.Join(base, "*", "reskate.sqlite3")); len(m) > 0 {
		return m[0]
	}
	return preferred
}

func isGameDir(dir string) bool {
	if dir == "" {
		return false
	}
	for _, f := range []string{"Skate.exe", "ReSkateLauncher.exe"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}

func detectGameDir() string {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	var c []string
	if exe, err := os.Executable(); err == nil {
		c = append(c, filepath.Dir(exe))
	}
	for _, env := range []string{"OneDrive", "OneDriveConsumer", "OneDriveCommercial"} {
		if v := os.Getenv(env); v != "" {
			c = append(c, filepath.Join(v, "Documents", "ReSkate"))
		}
	}
	c = append(c, filepath.Join(home, "Documents", "ReSkate"))
	if more, _ := filepath.Glob(filepath.Join(home, "OneDrive*", "Documents", "ReSkate")); more != nil {
		c = append(c, more...)
	}
	for _, d := range []string{"C:", "D:", "E:"} {
		c = append(c, d+`\ReSkate`, d+`\Games\ReSkate`)
	}
	for _, x := range c {
		if isGameDir(x) {
			return x
		}
	}
	return ""
}

// The plugin's mod folder. ReSkate names a plugin, and its data folder, after that folder, which
// is "PresetWardrobe" from a zip but "<team>-PresetWardrobe" from Thunderstore (jrkmods-PresetWardrobe).
func (a *App) pluginFolder() string {
	if a.cfg.GameDir == "" {
		return ""
	}
	mods := filepath.Join(a.cfg.GameDir, "Mods")
	isPlugin := func(name string) bool {
		if _, err := os.Stat(filepath.Join(mods, name, "reskate-plugin.json")); err != nil {
			return false
		}
		var m struct {
			Name string `json:"name"`
		}
		if readJSON(filepath.Join(mods, name, "manifest.json"), &m) != nil {
			return false
		}
		return strings.EqualFold(m.Name, "PresetWardrobe") || strings.EqualFold(m.Name, "PresetPages") // PresetPages: test builds
	}
	// Every folder that is the plugin, whatever it's called: a zip installed by a mod manager can land
	// as "PresetWardrobe-plugin" beside an older "PresetWardrobe".
	var found []string
	entries, _ := os.ReadDir(mods)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && isPlugin(e.Name()) {
			found = append(found, e.Name())
		}
	}
	if len(found) == 0 {
		return ""
	}
	// More than one copy: the one whose data folder was written last is the one in use.
	best, newest := found[0], time.Time{}
	for _, n := range found {
		if st, err := os.Stat(filepath.Join(mods, ".reskate_plugin_data", n)); err == nil && st.ModTime().After(newest) {
			best, newest = n, st.ModTime()
		}
	}
	return best
}

func (a *App) pluginDataDir() string {
	folder := a.pluginFolder()
	if folder == "" {
		if a.cfg.GameDir == "" {
			return ""
		}
		folder = "PresetWardrobe"
	}
	return filepath.Join(a.cfg.GameDir, "Mods", ".reskate_plugin_data", folder)
}

func (a *App) pluginInstalled() bool { return a.pluginFolder() != "" }

// ---- fits ---------------------------------------------------------------------------------------

var hashRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (a *App) fitPath(hash string) (string, error) {
	if !hashRe.MatchString(hash) {
		return "", errors.New("bad fit id")
	}
	return a.dir("fits", hash+".json"), nil
}

func (a *App) getFit(hash string) (Fit, error) {
	p, err := a.fitPath(hash)
	if err != nil {
		return Fit{}, err
	}
	var f Fit
	if err := readJSON(p, &f); err != nil {
		return Fit{}, errors.New("that fit isn't in your catalogue any more")
	}
	return f, nil
}

func (a *App) putFit(f Fit) error {
	p, err := a.fitPath(f.Hash)
	if err != nil {
		return err
	}
	return writeJSON(p, f)
}

func (a *App) listFits() []Fit {
	files, _ := filepath.Glob(a.dir("fits", "*.json"))
	var out []Fit
	for _, f := range files {
		var fit Fit
		if readJSON(f, &fit) == nil && hashRe.MatchString(fit.Hash) {
			out = append(out, fit)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Added.After(out[j].Added) })
	return out
}

// Files a game preset in the catalogue (or finds it there). hidden: only for a before-load copy.
func (a *App) addFit(p StoredPreset, hidden bool) (Fit, bool) {
	h := outfitHash(p.RecipesJSON)
	if f, err := a.getFit(h); err == nil {
		if f.Hidden && !hidden {
			f.Hidden = false
			f.Added = time.Now()
			a.putFit(f)
			return f, true
		}
		return f, false
	}
	f := Fit{Hash: h, Added: time.Now(), Hidden: hidden, RecipeFormat: p.RecipeFormat,
		RecipesJSON: p.RecipesJSON, ExtensionsJSON: p.ExtensionsJSON}
	if f.RecipeFormat == 0 {
		f.RecipeFormat = 1
	}
	if f.ExtensionsJSON == "" {
		f.ExtensionsJSON = "{}"
	}
	a.putFit(f)
	return f, true
}

func (a *App) title(key string) string {
	if t, ok := a.titles[key]; ok && t != "" {
		return t
	}
	return readableKey(key)
}

// A fit's name: the one given, or its top (else hat, shoes...) + " fit".
func (a *App) fitName(f Fit) string {
	if f.Name != "" {
		return f.Name
	}
	return a.autoName(f.RecipesJSON)
}

// detail 0: "White Tee fit"; 1: "White Tee & Nolan Pant"; 2: adds the shoes.
func (a *App) autoNameAt(recipes string, detail int) string {
	pieces := piecesOf(recipes)
	pick := func(cats ...string) string {
		for _, cat := range cats {
			for _, p := range pieces {
				if p.Category == cat {
					return a.title(p.Key)
				}
			}
		}
		return ""
	}
	main := pick("Top", "Headwear", "Bottoms", "Shoes", "Accessory", "Deck")
	if main == "" {
		return "Fit"
	}
	if detail == 0 {
		return main + " fit"
	}
	parts := []string{main}
	for _, x := range []string{pick("Bottoms", "Headwear"), pick("Shoes"), pick("Headwear", "Deck")} {
		if x != "" && !contains(parts, x) && len(parts) <= detail {
			parts = append(parts, x)
		}
	}
	return strings.Join(parts, " & ")
}

func (a *App) autoName(recipes string) string {
	if n, ok := a.names[outfitHash(recipes)]; ok {
		return n
	}
	return a.autoNameAt(recipes, 0)
}

// Works out names that tell fits apart: more pieces are named only where two would look the same.
func (a *App) buildNames(recipes map[string]string) {
	a.names = map[string]string{}
	for detail := 0; detail <= 2; detail++ {
		count := map[string]int{}
		for h, r := range recipes {
			if _, done := a.names[h]; !done {
				count[a.autoNameAt(r, detail)]++
			}
		}
		for _, n := range a.names {
			count[n]++
		}
		for h, r := range recipes {
			if _, done := a.names[h]; done {
				continue
			}
			n := a.autoNameAt(r, detail)
			if count[n] == 1 || detail == 2 {
				a.names[h] = n
			}
		}
	}
}

// The URLs of a picture's turn frames, in order; none for a flat picture.
func framesIn(dir, urlBase string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	var nums []int
	for _, e := range entries {
		var n int
		if _, err := fmt.Sscanf(e.Name(), "%d.png", &n); err == nil && n >= 1 && n <= maxFrames {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	out := []string{}
	st, _ := os.Stat(dir)
	v := int64(0)
	if st != nil {
		v = st.ModTime().Unix()
	}
	for _, n := range nums {
		out = append(out, fmt.Sprintf("%s/%d.png?v=%d", urlBase, n, v))
	}
	return out
}

func (a *App) frameURLs(hash string) []string {
	return a.toHQ(hash, framesIn(a.dir("pictures", hash), "/pic/"+hash))
}

// The URLs of a picture's board views (bottom, side, griptape, other side); none without them.
func (a *App) boardURLs(hash string) []string {
	if !a.cfg.PlainBoards && fileExists(a.dir("pictures", hash, "board", litDir, litMark)) {
		return a.toHQ(hash, framesIn(a.dir("pictures", hash, "board", litDir), "/pic/"+hash+"/board/"+litDir))
	}
	return a.toHQ(hash, framesIn(a.dir("pictures", hash, "board"), "/pic/"+hash+"/board"))
}

func (a *App) pictureURL(hash string) string {
	st, err := os.Stat(a.dir("pictures", hash+".png"))
	if err != nil {
		return ""
	}
	return a.toHQ(hash, []string{fmt.Sprintf("/pic/%s.png?v=%d", hash, st.ModTime().Unix())})[0]
}

// ---- rotations ----------------------------------------------------------------------------------

var idSafe = regexp.MustCompile(`[^a-z0-9-]+`)

func newID(name string) string {
	slug := strings.Trim(idSafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(slug) > 24 {
		slug = slug[:24]
	}
	if slug == "" {
		slug = "rotation"
	}
	return fmt.Sprintf("%s-%d", slug, time.Now().UnixNano()%1e9)
}

func (a *App) rotationPath(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\.:`) {
		return "", errors.New("bad rotation id")
	}
	return a.dir("rotations", id+".json"), nil
}

func (a *App) listRotations() []Rotation {
	files, _ := filepath.Glob(a.dir("rotations", "*.json"))
	var out []Rotation
	for _, f := range files {
		var r Rotation
		if readJSON(f, &r) == nil && r.ID != "" {
			r.Slots = fixSlots(r.Slots)
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Auto != out[j].Auto {
			return !out[i].Auto
		}
		if out[i].Auto {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out
}

func fixSlots(s []string) []string {
	out := make([]string, slotCount)
	for i := 0; i < slotCount && i < len(s); i++ {
		if hashRe.MatchString(s[i]) {
			out[i] = s[i]
		}
	}
	return out
}

func (a *App) getRotation(id string) (Rotation, error) {
	p, err := a.rotationPath(id)
	if err != nil {
		return Rotation{}, err
	}
	var r Rotation
	if err := readJSON(p, &r); err != nil {
		return Rotation{}, errors.New("that rotation no longer exists")
	}
	r.Slots = fixSlots(r.Slots)
	return r, nil
}

func (a *App) putRotation(r Rotation) error {
	p, err := a.rotationPath(r.ID)
	if err != nil {
		return err
	}
	r.Slots = fixSlots(r.Slots)
	return writeJSON(p, r)
}

func (a *App) uniqueRotationName(base string) string {
	taken := map[string]bool{}
	for _, r := range a.listRotations() {
		taken[strings.ToLower(r.Name)] = true
	}
	if !taken[strings.ToLower(base)] {
		return base
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s %d", base, i)
		if !taken[strings.ToLower(n)] {
			return n
		}
	}
}

func defaultRotationName() string { return time.Now().Format("Jan 2") + " Rotation" }

func lineupHashes(current []StoredPreset) []string {
	out := make([]string, slotCount)
	for _, p := range current {
		for i, id := range presetIDs {
			if id == p.PresetID {
				out[i] = outfitHash(p.RecipesJSON)
			}
		}
	}
	return out
}

func rotationMatches(r Rotation, lineup []string) bool {
	filled := 0
	for i, h := range r.Slots {
		if h == "" {
			continue
		}
		filled++
		if i >= len(lineup) || lineup[i] != h {
			return false
		}
	}
	return filled > 0
}

// Keep the 8 newest before-load copies, and drop hidden fits nothing uses any more.
func (a *App) prune() {
	var autos []Rotation
	for _, r := range a.listRotations() {
		if r.Auto {
			autos = append(autos, r)
		}
	}
	for i := 8; i < len(autos); i++ {
		if p, err := a.rotationPath(autos[i].ID); err == nil {
			os.Remove(p)
		}
	}
	used := map[string]bool{}
	for _, r := range a.listRotations() {
		for _, h := range r.Slots {
			used[h] = true
		}
	}
	for _, f := range a.listFits() {
		if f.Hidden && !used[f.Hash] {
			if p, err := a.fitPath(f.Hash); err == nil {
				os.Remove(p)
			}
		}
	}
}

// ---- moving over from PresetBanks 0.1 ----------------------------------------------------------

func (a *App) migrateFromBanks() {
	old := filepath.Join(a.localApp, "PresetBanks")
	if _, err := os.Stat(filepath.Join(old, "banks")); err != nil {
		return
	}
	var oc struct {
		GameDir  string           `json:"game_dir"`
		Imported map[string]int64 `json:"imported_snaps"`
	}
	if readJSON(filepath.Join(old, "config.json"), &oc) == nil {
		if a.cfg.GameDir == "" {
			a.cfg.GameDir = oc.GameDir
		}
		for k, v := range oc.Imported {
			a.cfg.Imported[k] = v
		}
	}
	var t map[string]string
	if readJSON(filepath.Join(old, "titles.json"), &t) == nil {
		for k, v := range t {
			a.titles[k] = v
		}
		writeJSON(a.dir("titles.json"), a.titles)
	}
	pics, _ := filepath.Glob(filepath.Join(old, "pictures", "*.png"))
	for _, p := range pics {
		dst := a.dir("pictures", filepath.Base(p))
		if _, err := os.Stat(dst); err != nil {
			copyFile(p, dst)
		}
	}
	files, _ := filepath.Glob(filepath.Join(old, "banks", "*.json"))
	type oldBank struct {
		Name    string         `json:"name"`
		Created time.Time      `json:"created"`
		Updated time.Time      `json:"updated"`
		Auto    bool           `json:"auto"`
		Presets []StoredPreset `json:"presets"`
	}
	var banks []oldBank
	for _, f := range files {
		var b oldBank
		if readJSON(f, &b) == nil && len(b.Presets) > 0 {
			banks = append(banks, b)
		}
	}
	sort.Slice(banks, func(i, j int) bool { return banks[i].Created.Before(banks[j].Created) })
	numbered := regexp.MustCompile(`^(?i)bank \d+$`)
	for _, b := range banks {
		slots := make([]string, slotCount)
		for _, p := range b.Presets {
			f, _ := a.addFit(p, b.Auto)
			for i, id := range presetIDs {
				if id == p.PresetID {
					slots[i] = f.Hash
				}
			}
		}
		name := strings.TrimSpace(b.Name)
		if name == "" || numbered.MatchString(name) {
			name = b.Created.Format("Jan 2") + " Rotation"
		}
		name = strings.Replace(name, "Before loading Bank", "Before loading", 1)
		if !b.Auto {
			name = a.uniqueRotationName(name)
		}
		a.putRotation(Rotation{ID: newID(name), Name: name, Created: b.Created, Updated: b.Updated, Auto: b.Auto, Slots: slots})
		time.Sleep(time.Millisecond) // distinct ids
	}
}

// ---- pictures from the plugin -------------------------------------------------------------------

// A list of strings the plugin wrote from a Luau table: an empty one comes as {}, and one with gaps as an
// object keyed "1", "2", ... Both are read as lists, so one odd entry can't make the whole index unreadable
// (a snap with "worn": {} once made every later snap invisible).
type luaStrings []string

func (l *luaStrings) UnmarshalJSON(b []byte) error {
	var list []string
	if json.Unmarshal(b, &list) == nil {
		*l = list
		return nil
	}
	var keyed map[string]string
	if err := json.Unmarshal(b, &keyed); err != nil {
		*l = nil // something else (null, a number): no list, but the rest of the entry still counts
		return nil
	}
	keys := make([]string, 0, len(keyed))
	for k := range keyed {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		x, _ := strconv.Atoi(keys[i])
		y, _ := strconv.Atoi(keys[j])
		return x < y
	})
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyed[k])
	}
	*l = out
	return nil
}

type snapShot struct {
	N      int        `json:"n"`
	File   string     `json:"file"`
	Frames luaStrings `json:"frames"`
	Board  luaStrings `json:"board"`
	Turn   string     `json:"turn"`       // the turn's frames saved together in one animated PNG
	BoardT string     `json:"board_turn"` // the board views, the same way
	Worn   luaStrings `json:"worn"`
}

type snapSingle struct {
	Seq     int        `json:"seq"`
	Request int        `json:"request"` // the picture request it was taken for, if any
	File    string     `json:"file"`
	Frames  luaStrings `json:"frames"`
	Board   luaStrings `json:"board"`
	Turn    string     `json:"turn"`
	BoardT  string     `json:"board_turn"`
	Worn    luaStrings `json:"worn"`
	Slots   luaItems   `json:"slots"`  // what the skater wore when it was photographed (newer plugins)
	Params  luaParams  `json:"params"` // and the parameters of the items that have any
}

type snapIndex struct {
	Shots   []*snapShot
	Singles []*snapSingle
}

// The plugin writes its lists from Luau tables, so an empty list comes as {} and a list with gaps
// (slots 1, 2 and 5 of a walk-through) as an object keyed "1", "2", "5". Both are read as lists.
func luaList[T any](raw json.RawMessage) []*T {
	if len(raw) == 0 {
		return nil
	}
	var list []*T
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var keyed map[string]*T
	if json.Unmarshal(raw, &keyed) != nil {
		return nil
	}
	keys := make([]string, 0, len(keyed))
	for k := range keyed {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, _ := strconv.Atoi(keys[i])
		b, _ := strconv.Atoi(keys[j])
		return a < b
	})
	for _, k := range keys {
		list = append(list, keyed[k])
	}
	return list
}

func readSnapIndex(path string) (snapIndex, error) {
	var raw struct {
		Shots   json.RawMessage `json:"shots"`
		Singles json.RawMessage `json:"singles"`
	}
	if err := readJSON(path, &raw); err != nil {
		return snapIndex{}, err
	}
	return snapIndex{Shots: luaList[snapShot](raw.Shots), Singles: luaList[snapSingle](raw.Singles)}, nil
}

var snapName = regexp.MustCompile(`^(fit-\d+|\d\d|test)(-\d{1,2}|-b[1-4]|-turn|-board)?\.png$`)

const boardViews = 4

const maxFrames = 36

// The frames that exist, of those a snap lists, in order. Bad names are left out.
func existingFrames(dir string, frames []string) []string {
	var out []string
	for _, f := range frames {
		if !snapName.MatchString(f) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			out = append(out, filepath.Join(dir, f))
		}
	}
	if len(out) < 2 || len(out) > maxFrames {
		return nil
	}
	return out
}

// The board views that exist, of those a snap lists, in order; none unless all of them do.
func existingBoard(dir string, board []string) []string {
	out := existingFrames(dir, board)
	if len(out) != boardViews {
		return nil
	}
	return out
}

// Files a picture as <name>.png in dir, its turn frames as <name>/1.png ... and its board views as
// <name>/board/1.png ... 4.png, each set lined up (frames on the head, board views on the deck).
// framed: the pictures were framed together by the game (saved in one go), so they're kept as
// they are; otherwise each set is lined up first.
// afterStore is told of each picture filed (the app sets it, to make the upscaled copies).
var afterStore = func(dir, name string) {}

func storePicture(dir, name, main string, frames, board []string, framed bool) error {
	err := storePictureFiles(dir, name, main, frames, board, framed)
	if err == nil {
		afterStore(dir, name)
	}
	return err
}

func storePictureFiles(dir, name, main string, frames, board []string, framed bool) error {
	if err := copyFile(main, filepath.Join(dir, name+".png")); err != nil {
		return err
	}
	os.RemoveAll(filepath.Join(dir, name)) // the old frames go, even when the new picture has none
	write := func(sub string, paths []string, align func([]string) ([][]byte, error)) error {
		if len(paths) == 0 {
			return nil
		}
		folder := filepath.Join(dir, name, sub)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			return err
		}
		lined, err := align(paths)
		if err != nil {
			return err
		}
		for i, b := range lined {
			if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("%d.png", i+1)), b, 0o644); err != nil {
				return err
			}
		}
		return os.WriteFile(filepath.Join(folder, alignedMark), nil, 0o644)
	}
	frameAlign, boardAlign := alignFrames, alignBoard
	if framed {
		frameAlign, boardAlign = keepAsIs, keepAsIs
	}
	if err := write("", frames, frameAlign); err != nil {
		os.RemoveAll(filepath.Join(dir, name))
		return err
	}
	if err := write("board", board, boardAlign); err != nil {
		os.RemoveAll(filepath.Join(dir, name, "board")) // the turn frames are still good
	} else if len(board) > 0 {
		makeLit(filepath.Join(dir, name, "board"))
	}
	return nil
}

// storeBoard files a snap's board views (saved together in one animated PNG) for a picture that's
// filed already, in place of any it had.
func storeBoard(dir, name, src string) error {
	tmp, err := os.MkdirTemp("", "presetwardrobe-board-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	paths, err := splitTo(src, tmp, "b")
	if err != nil {
		return err
	}
	if len(paths) != boardViews {
		return fmt.Errorf("%d board views, not %d", len(paths), boardViews)
	}
	folder := filepath.Join(dir, name, "board")
	os.RemoveAll(folder)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return err
	}
	for i, p := range paths {
		if err := copyFile(p, filepath.Join(folder, fmt.Sprintf("%d.png", i+1))); err != nil {
			return err
		}
	}
	os.WriteFile(filepath.Join(folder, alignedMark), nil, 0o644)
	err = makeLit(folder)
	afterStore(dir, name)
	return err
}

func removeSnapFiles(main string, frames, board []string) {
	os.Remove(main)
	for _, f := range frames {
		os.Remove(f)
	}
	for _, f := range board {
		os.Remove(f)
	}
}

func boardAssets(recipes string) (deck string, all map[string]bool) {
	all = map[string]bool{}
	groups, err := parseRecipes(recipes)
	if err != nil || len(groups) < 2 {
		return "", all
	}
	for _, it := range groups[1].Items {
		if it.Asset == "" {
			continue
		}
		all[strings.ToLower(it.Asset)] = true
		if boardSlots[it.Slot] == "Deck" {
			deck = strings.ToLower(it.Asset)
		}
	}
	return deck, all
}

// Which slot did a one-fit snap picture? On the Skaters page the plugin can only read the board,
// so the deck, the other board parts and the game's selected slot are weighed together.
func guessSlot(current []StoredPreset, selected int, worn []string) (best []int, confident bool) {
	score := make([]int, slotCount)
	deckHit := make([]bool, slotCount)
	for _, p := range current {
		i := -1
		for k, id := range presetIDs {
			if id == p.PresetID {
				i = k
			}
		}
		if i < 0 {
			continue
		}
		deck, all := boardAssets(p.RecipesJSON)
		for _, w := range worn {
			lw := strings.ToLower(w)
			if lw == deck && deck != "" {
				score[i] += 3
				deckHit[i] = true
			} else if all[lw] {
				score[i]++
			}
		}
		if i == selected {
			score[i] += 2
		}
	}
	order := make([]int, slotCount)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool { return score[order[x]] > score[order[y]] })
	top := score[order[0]]
	ties := 0
	for _, i := range order {
		if score[i] == top {
			ties++
		}
		best = append(best, i+1)
	}
	confident = ties == 1 && top >= 3 && (deckHit[order[0]] || order[0] == selected)
	return best, confident
}

type ackMsg struct {
	ID      int    `json:"id"`
	Message string `json:"message"`
	Warn    bool   `json:"warn"`
}

// Tells the plugin what happened to its snaps; it shows these in the game.
func (a *App) ack(message string, warn bool) {
	dir := a.pluginDataDir()
	if dir == "" {
		return
	}
	a.cfg.AckID++
	a.acks = append(a.acks, ackMsg{ID: a.cfg.AckID, Message: message, Warn: warn})
	if len(a.acks) > 5 {
		a.acks = a.acks[len(a.acks)-5:]
	}
	b, _ := json.Marshal(map[string]any{"version": 2, "messages": a.acks})
	os.WriteFile(filepath.Join(dir, "added.json"), b, 0o644)
}

// Files new snaps: walk-through pictures by slot, one-fit snaps by best guess (or asks).
func (a *App) importSnaps(current []StoredPreset, selected int) (events []string) {
	dir := a.pluginDataDir()
	if dir == "" || len(current) == 0 {
		return nil
	}
	idx, err := readSnapIndex(filepath.Join(dir, "snaps", "index.json"))
	if err != nil {
		log.Println("couldn't read the plugin's snaps/index.json:", err)
		return nil
	}
	changed := false
	presetFor := func(slot int) *StoredPreset {
		for i := range current {
			if slot >= 1 && slot <= slotCount && current[i].PresetID == presetIDs[slot-1] {
				return &current[i]
			}
		}
		return nil
	}
	walked := 0
	for _, s := range idx.Shots {
		if s == nil {
			continue
		}
		name := s.File
		if name == "" {
			name = fmt.Sprintf("%02d.png", s.N)
		}
		if !snapName.MatchString(name) {
			continue
		}
		src := filepath.Join(dir, "snaps", name)
		st, err := os.Stat(src)
		if err != nil || a.cfg.Imported[name] == st.ModTime().UnixNano() {
			continue
		}
		if p := presetFor(s.N); p != nil {
			f, _ := a.addFit(*p, false)
			snaps := filepath.Join(dir, "snaps")
			frames, board, framed, tmp := snapSets(snaps, s.Frames, s.Board, s.Turn, s.BoardT)
			err := storePicture(a.dir("pictures"), f.Hash, src, frames, board, framed)
			os.RemoveAll(tmp)
			if err == nil {
				walked++
			}
		}
		a.cfg.Imported[name] = st.ModTime().UnixNano()
		changed = true
	}
	// A walk-through's boards come in a second pass, after their outfits were filed.
	boards := 0
	for _, s := range idx.Shots {
		if s == nil || s.BoardT == "" || !snapName.MatchString(s.BoardT) {
			continue
		}
		src := filepath.Join(dir, "snaps", s.BoardT)
		st, err := os.Stat(src)
		key := "board:" + s.BoardT
		if err != nil || a.cfg.Imported[key] == st.ModTime().UnixNano() {
			continue
		}
		if p := presetFor(s.N); p != nil {
			f, _ := a.addFit(*p, false)
			if storeBoard(a.dir("pictures"), f.Hash, src) == nil {
				boards++
			}
		}
		a.cfg.Imported[key] = st.ModTime().UnixNano()
		changed = true
	}
	if boards > 0 && walked == 0 {
		events = append(events, fmt.Sprintf("Added board pictures to %d fits from your walk-through.", boards))
	}
	if walked > 0 {
		events = append(events, fmt.Sprintf("Added %d fits from your walk-through to the Fit Catalogue.", walked))
		a.ack(fmt.Sprintf("Added %d fits to your Fit Catalogue.", walked), false)
	}
	for _, s := range idx.Singles {
		// A snap still in the folder hasn't been filed: filing removes it. (Not by number: the
		// plugin's counter starts again if it's reinstalled under another folder name.)
		if s == nil || !snapName.MatchString(s.File) || !strings.HasPrefix(s.File, "fit-") {
			continue
		}
		src := filepath.Join(dir, "snaps", s.File)
		frames, board, framed, tmp := snapSets(filepath.Join(dir, "snaps"), s.Frames, s.Board, s.Turn, s.BoardT)
		originals := snapFiles(filepath.Join(dir, "snaps"), s.Frames, s.Board, s.Turn, s.BoardT)
		if _, err := os.Stat(src); err != nil {
			continue // filed already, or not written yet
		}
		if s.Seq > a.cfg.LastSingle {
			a.cfg.LastSingle = s.Seq
		}
		changed = true
		best, confident := guessSlot(current, selected, s.Worn)
		// The snap says what the skater wore: it is filed as exactly that, on the preset it is closest to (edits made
		// in the game that the game hasn't saved are in the pictures, so they belong in the fit).
		current := current
		if slot, recipes, ok := snapOutfit(current, selected, best, skaterNow{N: len(s.Slots), Slots: s.Slots, Params: s.Params}); ok {
			current = append([]StoredPreset(nil), current...)
			for i := range current {
				if current[i].PresetID == presetIDs[slot-1] {
					current[i].RecipesJSON = recipes
				}
			}
			best, confident = []int{slot}, true
		}
		presetFor := func(slot int) *StoredPreset {
			for i := range current {
				if slot >= 1 && slot <= slotCount && current[i].PresetID == presetIDs[slot-1] {
					return &current[i]
				}
			}
			return nil
		}
		matchesSaved := len(s.Worn) == 0 // older snaps don't list it
		known := knownItems(current)
		for _, p := range current {
			if wornIsFit(s.Worn, p.RecipesJSON, known) {
				matchesSaved = true
			}
		}
		if pictureRequests && s.Request != 0 {
			var reqName string
			for _, r := range a.cfg.Requests {
				if r.ID == s.Request {
					if f, ok := a.fitByHash(r.Hash); ok {
						reqName = a.fitName(f)
					}
				}
			}
			msg, ok := a.requestSnap(s.Request, current, best, presetFor, s.Worn, func(hash string) {
				storePicture(a.dir("pictures"), hash, src, frames, board, framed)
			})
			if ok {
				os.RemoveAll(tmp)
				removeSnapFiles(src, originals, nil)
				events = append(events, msg)
				a.ack(msg, false)
				continue
			}
			if reqName != "" && matchesSaved {
				msg := fmt.Sprintf("That snap isn't %s any more (the outfit or board changed?), so it's filed as its own fit.", reqName)
				events = append(events, msg)
				a.ack(msg, true)
			}
		}
		// The clothes in the snap aren't any saved preset's: changes the game hadn't saved. There's no
		// saved outfit to file it as, and filing it by its board would put it on the wrong fit.
		if unsavedCheck && !matchesSaved {
			os.RemoveAll(tmp)
			removeSnapFiles(src, originals, nil)
			msg := "That snap's board has changes the game hadn't saved, so it wasn't added. Save the preset in the game, then snap it again."
			events = append(events, msg)
			a.ack(msg, true)
			continue
		}
		if confident {
			p := presetFor(best[0])
			f, isNew := a.addFit(*p, false)
			storePicture(a.dir("pictures"), f.Hash, src, frames, board, framed)
			os.RemoveAll(tmp)
			removeSnapFiles(src, originals, nil)
			if q := a.requestFor(f.Hash); q != nil {
				a.dropRequest(q.ID) // asked for, and snapped anyway with F8: that's the new picture
			}
			verb := "Added"
			if !isNew {
				verb = "Updated the picture of"
			}
			msg := fmt.Sprintf("%s %s (slot %d) in your Fit Catalogue.", verb, a.fitName(f), best[0])
			events = append(events, msg)
			a.ack(msg, false)
			continue
		}
		id := fmt.Sprintf("snap-%d", time.Now().UnixNano())
		err := storePicture(a.dir("pending"), id, src, frames, board, framed)
		os.RemoveAll(tmp)
		if err == nil {
			writeJSON(a.dir("pending", id+".json"), Pending{ID: id, At: time.Now(), Guess: best[0], Candidates: best})
			removeSnapFiles(src, originals, nil)
			events = append(events, "New snap: tell Preset Wardrobe which slot it was.")
			a.ack("Snapped! Pick its slot in Preset Wardrobe to add it.", false)
		}
	}
	if changed {
		a.saveConfig()
	}
	return events
}

func (a *App) listPending() []Pending {
	files, _ := filepath.Glob(a.dir("pending", "*.json"))
	var out []Pending
	for _, f := range files {
		var p Pending
		if readJSON(f, &p) == nil && p.ID != "" {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Asks the plugin for item names, and reads its answers.
func (a *App) syncTitles(recipes []string) {
	dir := a.pluginDataDir()
	if dir == "" {
		return
	}
	var t map[string]string
	if readJSON(filepath.Join(dir, "titles.json"), &t) == nil {
		changed := false
		for k, v := range t {
			if v != "" && a.titles[k] != v {
				a.titles[k] = v
				changed = true
			}
		}
		if changed {
			writeJSON(a.dir("titles.json"), a.titles)
		}
	}
	seen := map[string]bool{}
	var keys []string
	for _, r := range recipes {
		for _, p := range piecesOf(r) {
			if !seen[p.Key] {
				seen[p.Key] = true
				keys = append(keys, p.Key)
			}
		}
	}
	sort.Strings(keys)
	sig := strings.Join(keys, "\n")
	if sig == a.wantedSig {
		return
	}
	a.wantedSig = sig
	if _, err := os.Stat(filepath.Join(a.cfg.GameDir, "Mods")); err != nil {
		return
	}
	os.MkdirAll(dir, 0o755)
	b, _ := json.Marshal(keys)
	os.WriteFile(filepath.Join(dir, "wanted.json"), b, 0o644)
}

// ---- the state the page shows ---------------------------------------------------------------------

type SlotView struct {
	Slot        int      `json:"slot"`
	Hash        string   `json:"hash"`
	Name        string   `json:"name"`
	InCatalogue bool     `json:"in_catalogue"`
	Pieces      []Piece  `json:"pieces"`
	Picture     string   `json:"picture"`
	Frames      []string `json:"frames"`
	Board       []string `json:"board"`
	Selected    bool     `json:"selected"`
	Requested   bool     `json:"requested"` // a new picture is asked for
	New         bool     `json:"new"`
	Favorite    bool     `json:"favorite"`
	LiveChanges []string `json:"live_changes,omitempty"` // what the game's skater has that the preset doesn't (edited in the game, not saved)
	HQ          string   `json:"hq,omitempty"`           // "done" or "working": the HQ pictures, made in the background
}

type FitView struct {
	Hash      string    `json:"hash"`
	Name      string    `json:"name"`
	Pieces    []Piece   `json:"pieces"`
	Picture   string    `json:"picture"`
	Frames    []string  `json:"frames"`
	Board     []string  `json:"board"`
	Added     time.Time `json:"added"`
	New       bool      `json:"new"`
	Hidden    bool      `json:"hidden"`
	InGame    bool      `json:"in_game"`
	Requested bool      `json:"requested"`
	Rotations []string  `json:"rotations"`
	Mixed     bool      `json:"mixed,omitempty"` // pictures combined from two fits, not a real snap
	Favorite  bool      `json:"favorite"`
	HQ        string    `json:"hq,omitempty"` // "done" or "working": the HQ pictures, made in the background
}

type RotationView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Auto        bool      `json:"auto"`
	Updated     time.Time `json:"updated"`
	Slots       []string  `json:"slots"`
	Filled      int       `json:"filled"`
	MatchesGame bool      `json:"matches_game"`
}

type PendingView struct {
	ID         string   `json:"id"`
	Picture    string   `json:"picture"`
	Frames     []string `json:"frames"`
	Board      []string `json:"board"`
	Guess      int      `json:"guess"`
	Candidates []int    `json:"candidates"`
}

type apiState struct {
	GameRunning     bool                   `json:"game_running"`
	ProfileDB       string                 `json:"profile_db"`
	ProfileError    string                 `json:"profile_error,omitempty"`
	GameDir         string                 `json:"game_dir"`
	GameDirOK       bool                   `json:"game_dir_ok"`
	PlainBoards     bool                   `json:"plain_boards"`
	AfterLoad       string                 `json:"after_load"`
	Views           map[string]PreviewView `json:"views"`
	CardSizes       map[string]CardSize    `json:"card_sizes"`
	Features        map[string]bool        `json:"features"`
	PluginInstalled bool                   `json:"plugin_installed"`
	Lineup          []SlotView             `json:"lineup"`
	Fits            []FitView              `json:"fits"`
	Rotations       []RotationView         `json:"rotations"`
	Pending         []PendingView          `json:"pending"`
	Events          []string               `json:"events"`
	DefaultName     string                 `json:"default_rotation_name"`
	Hotkeys         Hotkeys                `json:"hotkeys"`
	Update          updateView             `json:"update"`
	UpdateCheck     bool                   `json:"update_check"`
	Version         string                 `json:"version"`
}

func (a *App) piecesWithTitles(recipes string) []Piece {
	pieces := piecesOf(recipes)
	if pieces == nil {
		return []Piece{}
	}
	mods := a.modItems()
	for i := range pieces {
		pieces[i].Title = a.title(pieces[i].Key)
		pieces[i].Mod = mods[strings.ToLower(pieces[i].Key)]
		pieces[i].Missing = itemWasMissing(pieces[i].Key)
	}
	return pieces
}

// Which mod each item comes from: ReSkate's merge records it in Mods/.reskate/reskate-items.json
// ({"mods": [{mod, title, author, items: [lower-case keys]}]}, highest priority first: where two
// mods list one item, the first one's is used). Read again when it changes.
func (a *App) modItems() map[string]string {
	if a.cfg.GameDir == "" {
		return nil
	}
	path := filepath.Join(a.cfg.GameDir, "Mods", ".reskate", "reskate-items.json")
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if a.modsAt.Equal(st.ModTime()) && a.modsPath == path {
		return a.mods
	}
	var file struct {
		Mods []struct {
			Mod, Title, Author string
			Items              []string
		}
	}
	a.mods, a.modsAt, a.modsPath = map[string]string{}, st.ModTime(), path
	if readJSON(path, &file) != nil {
		return a.mods
	}
	for _, m := range file.Mods {
		name := m.Title
		if name == "" {
			name = m.Mod
		}
		name = strings.ReplaceAll(name, "_", " ")
		if m.Author != "" {
			name += " by " + m.Author
		}
		for _, k := range m.Items {
			k = strings.ToLower(k)
			if _, taken := a.mods[k]; !taken {
				a.mods[k] = name
			}
		}
	}
	return a.mods
}

func (a *App) state() apiState {
	st := apiState{GameRunning: gameRunning(), ProfileDB: a.profileDB(), GameDir: a.cfg.GameDir,
		GameDirOK: isGameDir(a.cfg.GameDir), PlainBoards: a.cfg.PlainBoards, AfterLoad: a.cfg.AfterLoad, Views: a.previewViews(), CardSizes: a.cfg.CardSizes, Features: map[string]bool{"picture_requests": pictureRequests}, PluginInstalled: a.pluginInstalled(), Version: appVersion, Hotkeys: a.hotkeys(), Update: updateState(), UpdateCheck: !a.cfg.NoUpdateCheck,
		Lineup: []SlotView{}, Fits: []FitView{}, Rotations: []RotationView{}, Pending: []PendingView{}, Events: []string{}}
	current, sel, err := readPresets(st.ProfileDB, a.dir("tmp"))
	if err != nil {
		st.ProfileError = "Couldn't read your skater profile: " + err.Error()
	}
	current = a.withLive(a.withPending(current))
	if ev := a.importSnaps(current, sel); ev != nil {
		st.Events = ev
	}
	fits := a.listFits()
	rotations := a.listRotations()
	var recipes []string
	for _, p := range current {
		recipes = append(recipes, p.RecipesJSON)
	}
	for _, f := range fits {
		recipes = append(recipes, f.RecipesJSON)
	}
	a.syncTitles(recipes)
	named := map[string]string{}
	for _, p := range current {
		named[outfitHash(p.RecipesJSON)] = p.RecipesJSON
	}
	for _, f := range fits {
		if f.Name == "" {
			named[f.Hash] = f.RecipesJSON
		}
	}
	a.buildNames(named)

	lineup := lineupHashes(current)
	inGame := map[string]bool{}
	for _, h := range lineup {
		inGame[h] = true
	}
	byHash := map[string]Fit{}
	for _, f := range fits {
		byHash[f.Hash] = f
	}
	for _, p := range current {
		slot := 0
		for i, id := range presetIDs {
			if id == p.PresetID {
				slot = i + 1
			}
		}
		h := outfitHash(p.RecipesJSON)
		f, ok := byHash[h]
		name := a.autoName(p.RecipesJSON)
		if ok {
			name = a.fitName(f)
		}
		st.Lineup = append(st.Lineup, SlotView{Slot: slot, Hash: h, Name: name, InCatalogue: ok && !f.Hidden,
			Pieces: a.piecesWithTitles(p.RecipesJSON), Picture: a.pictureURL(h), Frames: a.frameURLs(h), Board: a.boardURLs(h), HQ: a.hqState(h), Selected: sel == slot-1,
			New: ok && !f.Hidden && f.Added.After(a.cfg.SeenUpTo), Requested: a.requestFor(h) != nil,
			Favorite: ok && !f.Hidden && f.Favorite})
		if ls := shownChanges(); ls != nil && ls.PresetID == p.PresetID {
			st.Lineup[len(st.Lineup)-1].LiveChanges = describeChanges(ls.Changes)
		}
	}
	usedIn := map[string][]string{}
	for _, r := range rotations {
		if r.Auto {
			continue
		}
		for _, h := range r.Slots {
			if h != "" && !contains(usedIn[h], r.Name) {
				usedIn[h] = append(usedIn[h], r.Name)
			}
		}
	}
	for _, f := range fits {
		rs := usedIn[f.Hash]
		if rs == nil {
			rs = []string{}
		}
		st.Fits = append(st.Fits, FitView{Hash: f.Hash, Name: a.fitName(f), Pieces: a.piecesWithTitles(f.RecipesJSON),
			Picture: a.pictureURL(f.Hash), Frames: a.frameURLs(f.Hash), Board: a.boardURLs(f.Hash), HQ: a.hqState(f.Hash), Added: f.Added,
			New: !f.Hidden && f.Added.After(a.cfg.SeenUpTo), Hidden: f.Hidden, InGame: inGame[f.Hash], Rotations: rs,
			Requested: a.requestFor(f.Hash) != nil, Mixed: a.stillMixed(f), Favorite: f.Favorite})
	}
	if err == nil && unsavedCheck {
		a.savedOutfits(current)
	}
	if pictureRequests {
		a.syncRequests(current, func(h string) string {
			if f, ok := byHash[h]; ok {
				return a.fitName(f)
			}
			return "a fit"
		})
	}
	for _, r := range rotations {
		rv := RotationView{ID: r.ID, Name: r.Name, Auto: r.Auto, Updated: r.Updated, Slots: r.Slots,
			MatchesGame: len(current) > 0 && rotationMatches(r, lineup)}
		for _, h := range r.Slots {
			if h != "" {
				rv.Filled++
			}
		}
		st.Rotations = append(st.Rotations, rv)
	}
	for _, p := range a.listPending() {
		st.Pending = append(st.Pending, PendingView{ID: p.ID, Picture: "/pending/" + p.ID + ".png",
			Frames: framesIn(a.dir("pending", p.ID), "/pending/"+p.ID),
			Board:  framesIn(a.dir("pending", p.ID, "board"), "/pending/"+p.ID+"/board"), Guess: p.Guess, Candidates: p.Candidates})
	}
	st.DefaultName = a.uniqueRotationName(defaultRotationName())
	return st
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- HTTP -----------------------------------------------------------------------------------------

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func cleanName(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

// The wardrobe file format (.wardrobe): a zip with wardrobe.json and pictures/<hash>.png.
type wardrobeFile struct {
	Kind     string    `json:"kind"` // "rotation" or "catalogue"
	Rotation *Rotation `json:"rotation,omitempty"`
	Fits     []Fit     `json:"fits"`
}

// localHostOnly turns away any request whose Host isn't this computer itself. The app listens only on 127.0.0.1, but
// a web page can make a browser send requests to it under another name (DNS rebinding), and those would pass the
// Origin check, which compares the two.
func localHostOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.LastIndex(host, ":"); i >= 0 && !strings.HasSuffix(host, "]") {
			host = host[:i]
		}
		switch strings.ToLower(host) {
		case "127.0.0.1", "localhost", "[::1]":
			h.ServeHTTP(w, r)
		default:
			http.Error(w, "not allowed", http.StatusForbidden)
		}
	})
}

func (a *App) routes(ui []byte) *http.ServeMux {
	mux := http.NewServeMux()
	lock := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.lastSeen = time.Now()
			h(w, r)
		}
	}
	post := func(h http.HandlerFunc) http.HandlerFunc {
		return lock(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				fail(w, 405, "POST only")
				return
			}
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				fail(w, 403, "not allowed")
				return
			}
			h(w, r)
		})
	}
	readCurrent := func(w http.ResponseWriter) ([]StoredPreset, bool) {
		current, _, err := readPresets(a.profileDB(), a.dir("tmp"))
		if err != nil {
			fail(w, 500, "Couldn't read your In-Game Lineup: "+err.Error())
			return nil, false
		}
		return a.withLive(a.withPending(current)), true
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(ui)
	})
	// /pic/<hash>.png, and its frames /pic/<hash>/<n>.png, and the board views /pic/<hash>/board/<n>.png
	// (or .../board/lit/<n>.png). With ?w=1 a smaller copy is sent, for the grid.
	picRe := regexp.MustCompile(`^([0-9a-f]{16})(?:\.png|/(\d{1,2})\.png|/board/(lit/)?([1-4])\.png)$`)
	// picPath is the file a /pic/ or /enhance/ path names, and a name for it (for caches).
	picPath := func(rest string) (path, name string, ok bool) {
		m := picRe.FindStringSubmatch(rest)
		if m == nil {
			return "", "", false
		}
		switch {
		case m[4] != "":
			return a.dir("pictures", m[1], "board", strings.TrimSuffix(m[3], "/"), m[4]+".png"), m[1] + "-b" + m[3][:min(1, len(m[3]))] + m[4], true
		case m[2] != "":
			return a.dir("pictures", m[1], m[2]+".png"), m[1] + "-" + m[2], true
		}
		return a.dir("pictures", m[1]+".png"), m[1] + "-0", true
	}
	mux.HandleFunc("/pic/", lock(func(w http.ResponseWriter, r *http.Request) {
		path, name, ok := picPath(strings.TrimPrefix(r.URL.Path, "/pic/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "max-age=31536000")
		if r.URL.Query().Get("w") != "" {
			path = thumbnail(path, a.dir("thumbs"), name)
		}
		http.ServeFile(w, r, path)
	}))
	// /hq/...: the HQ copies made in the background (see hq.go). Read-only, no lock needed.
	mux.HandleFunc("/hq/", func(w http.ResponseWriter, r *http.Request) {
		path, ok := a.hqFile(strings.TrimPrefix(r.URL.Path, "/hq/"))
		if !ok || !fileExists(path) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Cache-Control", "private, max-age=86400") // the address carries the picture's time
		http.ServeFile(w, r, path)
	})
	/* PARKED (come back to upscaling options): Enhance in the viewer, made on the spot and kept in memory.
	// /enhance/...: the same pictures, enlarged and sharpened on the spot (kept in memory only).
	// It doesn't take the app's lock: enlarging a picture takes a moment, and the rest of the app (and the other
	// pictures being enlarged) mustn't wait behind it.
	mux.HandleFunc("/enhance/", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.lastSeen = time.Now()
		a.mu.Unlock()
		path, name, ok := picPath(strings.TrimPrefix(r.URL.Path, "/enhance/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		st, err := os.Stat(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		scale := 0
		if strings.Contains(r.URL.Path, "/board/") {
			scale = upBoardScale
		}
		if n, err := strconv.Atoi(r.URL.Query().Get("s")); err == nil && n >= 1 && n <= 4 {
			scale = n // the size asked for in the viewer: 1 sharpens only
		}
		data, err := a.enh.get(fmt.Sprintf("%s@%d@%d", name, st.ModTime().UnixNano(), scale), path, scale)
		if err != nil {
			fail(w, 500, "Couldn't enhance the picture.")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		// the browser may keep it: the same address always gives the same picture (the address carries the
		// picture's time and the size), and fetching a large one again is what made spins flicker
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Write(data)
	})
	// Which sizes of these pictures are still kept in memory (so the viewer can show which sizes need no waiting).
	mux.HandleFunc("/api/enhance/have", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Pics []string `json:"pics"`
		}
		if decode(r, &req) != nil || len(req.Pics) > 500 {
			fail(w, 400, "bad request")
			return
		}
		out := map[int]bool{}
		for n := 1; n <= 4; n++ {
			all := len(req.Pics) > 0
			for _, u := range req.Pics {
				rel := strings.TrimPrefix(strings.SplitN(u, "?", 2)[0], "/pic/")
				path, name, ok := picPath(rel)
				st, err := os.Stat(path)
				if !ok || err != nil || !a.enh.has(fmt.Sprintf("%s@%d@%d", name, st.ModTime().UnixNano(), n)) {
					all = false
					break
				}
			}
			out[n] = all
		}
		reply(w, out)
	}))
	// Enhance turned off: the enhanced pictures kept in memory are let go.
	mux.HandleFunc("/api/enhance/clear", post(func(w http.ResponseWriter, r *http.Request) {
		a.enh.clear()
		reply(w, map[string]string{})
	}))
	PARKED end */
	// Storage: what the pictures take, and clearing what can be made again.
	mux.HandleFunc("/api/storage", lock(func(w http.ResponseWriter, r *http.Request) {
		var freed int64
		if r.Method == http.MethodPost {
			freed = a.freeSpace()
			tl.logf("APP: freed %d bytes of copies that can be made again", freed)
		}
		u := a.storageDetail()
		u.Freed = freed
		reply(w, u)
	}))
	pendRe := regexp.MustCompile(`^(snap-\d+)(?:\.png|/(\d{1,2})\.png|/board/([1-4])\.png)$`)
	mux.HandleFunc("/pending/", lock(func(w http.ResponseWriter, r *http.Request) {
		m := pendRe.FindStringSubmatch(strings.TrimPrefix(r.URL.Path, "/pending/"))
		if m == nil {
			http.NotFound(w, r)
			return
		}
		if m[3] != "" {
			http.ServeFile(w, r, a.dir("pending", m[1], "board", m[3]+".png"))
			return
		}
		if m[2] != "" {
			http.ServeFile(w, r, a.dir("pending", m[1], m[2]+".png"))
			return
		}
		http.ServeFile(w, r, a.dir("pending", m[1]+".png"))
	}))

	mux.HandleFunc("/api/log", a.logHandler)
	mux.HandleFunc("/api/state", lock(func(w http.ResponseWriter, r *http.Request) { reply(w, a.state()) }))

	// Add one slot of the In-Game Lineup to the catalogue.
	// Ask the plugin for a new picture of a fit, or stop asking.
	mux.HandleFunc("/api/picture/request", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Hash   string `json:"hash"`
			Cancel bool   `json:"cancel"`
		}
		if decode(r, &req) != nil || !hashRe.MatchString(req.Hash) {
			fail(w, 400, "bad request")
			return
		}
		if !pictureRequests {
			fail(w, 404, "Not available yet.")
			return
		}
		if req.Cancel {
			if q := a.requestFor(req.Hash); q != nil {
				a.dropRequest(q.ID)
			}
			reply(w, map[string]string{"message": "Request cancelled."})
			return
		}
		a.addRequest(req.Hash)
		reply(w, map[string]string{"message": "Sent to the game: it's under MESSAGES at the top of the plugin's page."})
	}))

	mux.HandleFunc("/api/fit/add", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Slot int }
		if decode(r, &req) != nil || req.Slot < 1 || req.Slot > slotCount {
			fail(w, 400, "bad slot")
			return
		}
		current, ok := readCurrent(w)
		if !ok {
			return
		}
		for _, p := range current {
			if p.PresetID == presetIDs[req.Slot-1] {
				f, isNew := a.addFit(p, false)
				msg := a.fitName(f) + " is already in your Fit Catalogue."
				if isNew {
					msg = "Added " + a.fitName(f) + " to your Fit Catalogue."
				}
				reply(w, map[string]string{"message": msg, "hash": f.Hash})
				return
			}
		}
		fail(w, 404, "That slot is empty in the game.")
	}))

	mux.HandleFunc("/api/fit/mix", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Skater, Board, Name string }
		if decode(r, &req) != nil {
			fail(w, 400, "bad request")
			return
		}
		f, existed, err := a.mixFits(req.Skater, req.Board, req.Name)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if existed {
			reply(w, map[string]string{"message": "That mix is already in your catalogue as " + a.fitName(f) + ".", "hash": f.Hash})
			return
		}
		reply(w, map[string]string{"message": "Saved " + a.fitName(f) + " to your Fit Catalogue.", "hash": f.Hash})
	}))

	mux.HandleFunc("/api/fit/rename", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Hash, Name string }
		if decode(r, &req) != nil {
			fail(w, 400, "bad request")
			return
		}
		f, err := a.getFit(req.Hash)
		if err != nil {
			fail(w, 404, err.Error())
			return
		}
		f.Name = cleanName(req.Name, 60)
		a.putFit(f)
		reply(w, map[string]string{"message": "Renamed to " + a.fitName(f) + "."})
	}))

	mux.HandleFunc("/api/fit/favorite", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Hash     string
			Favorite bool
		}
		if decode(r, &req) != nil {
			fail(w, 400, "bad request")
			return
		}
		f, err := a.getFit(req.Hash)
		if err != nil {
			fail(w, 404, err.Error())
			return
		}
		f.Favorite = req.Favorite
		if err := a.putFit(f); err != nil {
			fail(w, 500, "Couldn't save that: "+err.Error())
			return
		}
		reply(w, map[string]string{"message": ""})
	}))

	// Takes an item the game says isn't installed out of a fit; the fit's pictures and rotations follow it.
	mux.HandleFunc("/api/fit/remove-item", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Hash string
			Item string
		}
		if decode(r, &req) != nil || req.Item == "" {
			fail(w, 400, "bad request")
			return
		}
		name := a.title(req.Item)
		_, merged, err := a.removeMissingItem(req.Hash, req.Item)
		if err != nil {
			fail(w, 400, "Couldn't remove "+name+": "+err.Error())
			return
		}
		msg := name + " was taken out of this fit. Its pictures and rotations moved with it."
		if merged {
			msg = name + " was taken out, and the fit now matches one you already had, so they were merged."
		}
		a.note("ok", "%s", msg)
		reply(w, map[string]string{"message": msg})
	}))

	mux.HandleFunc("/api/fit/delete", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Hash string }
		decode(r, &req)
		p, err := a.fitPath(req.Hash)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		for _, rot := range a.listRotations() {
			changed := false
			for i, h := range rot.Slots {
				if h == req.Hash {
					rot.Slots[i] = ""
					changed = true
				}
			}
			if changed {
				rot.Updated = time.Now()
				a.putRotation(rot)
			}
		}
		os.Remove(p)
		os.Remove(a.dir("pictures", req.Hash+".png"))
		os.RemoveAll(a.dir("pictures", req.Hash))
		reply(w, map[string]string{"message": "Removed from your Fit Catalogue. Your game isn't changed."})
	}))

	// A snap the program couldn't place: which slot was it (0 throws it away)?
	mux.HandleFunc("/api/pending/resolve", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID   string
			Slot int
		}
		if decode(r, &req) != nil || !regexp.MustCompile(`^snap-\d+$`).MatchString(req.ID) {
			fail(w, 400, "bad request")
			return
		}
		pic := a.dir("pending", req.ID+".png")
		defer func() {
			os.Remove(pic)
			os.RemoveAll(a.dir("pending", req.ID))
			os.Remove(a.dir("pending", req.ID+".json"))
		}()
		if req.Slot == 0 {
			reply(w, map[string]string{"message": "Snap thrown away."})
			return
		}
		if req.Slot < 1 || req.Slot > slotCount {
			fail(w, 400, "bad slot")
			return
		}
		current, ok := readCurrent(w)
		if !ok {
			return
		}
		for _, p := range current {
			if p.PresetID == presetIDs[req.Slot-1] {
				f, _ := a.addFit(p, false)
				var frames []string
				for _, u := range framesIn(a.dir("pending", req.ID), "") {
					n := strings.SplitN(strings.TrimPrefix(u, "/"), "?", 2)[0]
					frames = append(frames, a.dir("pending", req.ID, n))
				}
				var board []string
				for i := 1; i <= boardViews; i++ {
					if p := a.dir("pending", req.ID, "board", fmt.Sprintf("%d.png", i)); fileExists(p) {
						board = append(board, p)
					}
				}
				if len(board) != boardViews {
					board = nil
				}
				storePicture(a.dir("pictures"), f.Hash, pic, frames, board, true) // lined up already
				reply(w, map[string]string{"message": "Added " + a.fitName(f) + " to your Fit Catalogue."})
				return
			}
		}
		fail(w, 404, "That slot is empty in the game.")
	}))

	// Create a rotation: from the In-Game Lineup (from_lineup) or from chosen fits.
	mux.HandleFunc("/api/rotation/create", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name       string   `json:"name"`
			Slots      []string `json:"slots"`
			FromLineup bool     `json:"from_lineup"`
		}
		if decode(r, &req) != nil {
			fail(w, 400, "bad request")
			return
		}
		slots := fixSlots(req.Slots)
		if req.FromLineup {
			current, ok := readCurrent(w)
			if !ok {
				return
			}
			slots = make([]string, slotCount)
			for _, p := range current {
				f, _ := a.addFit(p, false)
				for i, id := range presetIDs {
					if id == p.PresetID {
						slots[i] = f.Hash
					}
				}
			}
		} else {
			for i, h := range slots {
				if h != "" {
					if _, err := a.getFit(h); err != nil {
						slots[i] = ""
					}
				}
			}
		}
		name := cleanName(req.Name, 60)
		if name == "" {
			name = a.uniqueRotationName(defaultRotationName())
		}
		now := time.Now()
		rot := Rotation{ID: newID(name), Name: name, Created: now, Updated: now, Slots: slots}
		if err := a.putRotation(rot); err != nil {
			fail(w, 500, err.Error())
			return
		}
		reply(w, map[string]string{"id": rot.ID, "message": "Made " + name + "."})
	}))

	mux.HandleFunc("/api/rotation/update", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID    string    `json:"id"`
			Name  *string   `json:"name"`
			Slots *[]string `json:"slots"`
		}
		if decode(r, &req) != nil {
			fail(w, 400, "bad request")
			return
		}
		rot, err := a.getRotation(req.ID)
		if err != nil {
			fail(w, 404, err.Error())
			return
		}
		msg := "Saved."
		if req.Name != nil {
			n := cleanName(*req.Name, 60)
			if n == "" {
				fail(w, 400, "Give the rotation a name.")
				return
			}
			rot.Name, rot.Auto = n, false
			msg = "Renamed to " + n + "."
		}
		if req.Slots != nil {
			rot.Slots = fixSlots(*req.Slots)
			for i, h := range rot.Slots {
				if h != "" {
					if _, err := a.getFit(h); err != nil {
						rot.Slots[i] = ""
					}
				}
			}
		}
		rot.Updated = time.Now()
		a.putRotation(rot)
		reply(w, map[string]string{"message": msg})
	}))

	mux.HandleFunc("/api/rotation/delete", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID string }
		decode(r, &req)
		p, err := a.rotationPath(req.ID)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		os.Remove(p)
		a.prune()
		reply(w, map[string]string{"message": "Rotation deleted. Its fits stay in your Fit Catalogue."})
	}))

	// Put a rotation's fits into the game's slots: live while the game runs, into its file when it's closed.
	mux.HandleFunc("/api/rotation/load", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID   string
			Live bool // while the game is running: write into its memory
			Body bool // with Live: also the face and body, not just clothes and board
		}
		if decode(r, &req) != nil {
			fail(w, 400, "bad request")
			return
		}
		if gameRunning() && !req.Live {
			fail(w, 409, "Close skate. first: the game rewrites your presets when it quits.")
			return
		}
		rot, err := a.getRotation(req.ID)
		if err != nil {
			fail(w, 404, err.Error())
			return
		}
		a.loadRotation(w, rot, loadOpts{Live: req.Live, Body: req.Body, Slot: -1})
	}))

	// Swap one slot of the In-Game Lineup for a fit from the catalogue. No rotation needed.
	mux.HandleFunc("/api/slot/set", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Slot int
			Fit  string
			Live bool
			Body bool
		}
		if decode(r, &req) != nil || req.Slot < 0 || req.Slot >= slotCount {
			fail(w, 400, "bad request")
			return
		}
		if gameRunning() && !req.Live {
			fail(w, 409, "Close skate. first: the game rewrites your presets when it quits.")
			return
		}
		f, err := a.getFit(req.Fit)
		if err != nil {
			fail(w, 404, err.Error())
			return
		}
		slots := make([]string, slotCount)
		slots[req.Slot] = f.Hash
		now := time.Now()
		a.loadRotation(w, Rotation{ID: "swap", Name: f.Name, Created: now, Updated: now, Slots: slots},
			loadOpts{Live: req.Live, Body: req.Body, Slot: req.Slot})
	}))

	exportZip := func(w http.ResponseWriter, file wardrobeFile, name string) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		f, _ := zw.Create("wardrobe.json")
		b, _ := json.MarshalIndent(file, "", "  ")
		f.Write(b)
		for _, fit := range file.Fits {
			if pic, err := os.ReadFile(a.dir("pictures", fit.Hash+".png")); err == nil {
				f, _ := zw.Create("pictures/" + fit.Hash + ".png")
				f.Write(pic)
			}
			for i := range framesIn(a.dir("pictures", fit.Hash), "") {
				if pic, err := os.ReadFile(a.dir("pictures", fit.Hash, fmt.Sprintf("%d.png", i+1))); err == nil {
					f, _ := zw.Create(fmt.Sprintf("pictures/%s/%d.png", fit.Hash, i+1))
					f.Write(pic)
				}
			}
			for i := range framesIn(a.dir("pictures", fit.Hash, "board"), "") {
				if pic, err := os.ReadFile(a.dir("pictures", fit.Hash, "board", fmt.Sprintf("%d.png", i+1))); err == nil {
					f, _ := zw.Create(fmt.Sprintf("pictures/%s/board/%d.png", fit.Hash, i+1))
					f.Write(pic)
				}
			}
		}
		zw.Close()
		slug := strings.Trim(idSafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
		if slug == "" {
			slug = "wardrobe"
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+slug+`.wardrobe"`)
		w.Write(buf.Bytes())
	}
	mux.HandleFunc("/api/export", lock(func(w http.ResponseWriter, r *http.Request) {
		if id := r.URL.Query().Get("rotation"); id != "" {
			rot, err := a.getRotation(id)
			if err != nil {
				fail(w, 404, err.Error())
				return
			}
			file := wardrobeFile{Kind: "rotation", Rotation: &rot}
			for _, h := range rot.Slots {
				if f, err := a.getFit(h); err == nil {
					f.Hidden = false
					file.Fits = append(file.Fits, f)
				}
			}
			exportZip(w, file, rot.Name)
			return
		}
		file := wardrobeFile{Kind: "catalogue"}
		for _, f := range a.listFits() {
			if !f.Hidden {
				file.Fits = append(file.Fits, f)
			}
		}
		exportZip(w, file, "fit-catalogue")
	}))

	mux.HandleFunc("/api/import", post(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, 128<<20))
		if err != nil {
			fail(w, 400, "Couldn't read that file.")
			return
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			fail(w, 400, "That isn't a .wardrobe file.")
			return
		}
		var file wardrobeFile
		pics := map[string][]byte{}
		picName := regexp.MustCompile(`^pictures/([0-9a-f]{16})\.png$`)
		frameName := regexp.MustCompile(`^pictures/([0-9a-f]{16})/(\d{1,2})\.png$`)
		frames := map[string]map[int][]byte{}
		boardName := regexp.MustCompile(`^pictures/([0-9a-f]{16})/board/([1-4])\.png$`)
		boards := map[string]map[int][]byte{}
		for _, zf := range zr.File {
			if zf.UncompressedSize64 > 8<<20 {
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				continue
			}
			b, _ := io.ReadAll(rc)
			rc.Close()
			if zf.Name == "wardrobe.json" {
				json.Unmarshal(b, &file)
			} else if m := picName.FindStringSubmatch(zf.Name); m != nil {
				pics[m[1]] = b
			} else if m := boardName.FindStringSubmatch(zf.Name); m != nil {
				if bytes.HasPrefix(b, []byte("\x89PNG")) {
					if boards[m[1]] == nil {
						boards[m[1]] = map[int][]byte{}
					}
					boards[m[1]][int(m[2][0]-'0')] = b
				}
			} else if m := frameName.FindStringSubmatch(zf.Name); m != nil {
				var n int
				fmt.Sscanf(m[2], "%d", &n)
				if n >= 1 && n <= maxFrames && bytes.HasPrefix(b, []byte("\x89PNG")) {
					if frames[m[1]] == nil {
						frames[m[1]] = map[int][]byte{}
					}
					frames[m[1]][n] = b
				}
			}
		}
		added := 0
		hashes := map[string]string{} // hash in the file -> hash here
		for _, f := range file.Fits {
			var probe []any
			if json.Unmarshal([]byte(f.RecipesJSON), &probe) != nil {
				continue
			}
			var ext map[string]any
			if f.ExtensionsJSON == "" || json.Unmarshal([]byte(f.ExtensionsJSON), &ext) != nil {
				f.ExtensionsJSON = "{}"
			}
			fit, isNew := a.addFit(StoredPreset{RecipeFormat: 1, RecipesJSON: f.RecipesJSON, ExtensionsJSON: f.ExtensionsJSON}, false)
			if isNew && f.Name != "" {
				fit.Name = cleanName(f.Name, 60)
				a.putFit(fit)
			}
			if isNew {
				added++
			}
			hashes[f.Hash] = fit.Hash
			if b, ok := pics[f.Hash]; ok && bytes.HasPrefix(b, []byte("\x89PNG")) {
				dst := a.dir("pictures", fit.Hash+".png")
				if _, err := os.Stat(dst); err != nil {
					os.WriteFile(dst, b, 0o644)
					// Its frames, when they run 1, 2, 3... without a gap.
					fr := frames[f.Hash]
					whole := len(fr) >= 2
					for n := 1; n <= len(fr); n++ {
						whole = whole && fr[n] != nil
					}
					if whole {
						os.MkdirAll(a.dir("pictures", fit.Hash), 0o755)
						for n := 1; n <= len(fr); n++ {
							os.WriteFile(a.dir("pictures", fit.Hash, fmt.Sprintf("%d.png", n)), fr[n], 0o644)
						}
					}
					if bd := boards[f.Hash]; len(bd) == boardViews {
						os.MkdirAll(a.dir("pictures", fit.Hash, "board"), 0o755)
						for n := 1; n <= boardViews; n++ {
							os.WriteFile(a.dir("pictures", fit.Hash, "board", fmt.Sprintf("%d.png", n)), bd[n], 0o644)
						}
						makeLit(a.dir("pictures", fit.Hash, "board"))
					}
				}
			}
		}
		if len(hashes) == 0 {
			fail(w, 400, "That file has no fits in it.")
			return
		}
		msg := fmt.Sprintf("Added %d new fits to your Fit Catalogue.", added)
		resp := map[string]string{}
		if file.Kind == "rotation" && file.Rotation != nil {
			slots := make([]string, slotCount)
			for i, h := range fixSlots(file.Rotation.Slots) {
				slots[i] = hashes[h]
			}
			name := a.uniqueRotationName(cleanName(file.Rotation.Name, 60))
			if name == "" {
				name = a.uniqueRotationName("Shared Rotation")
			}
			now := time.Now()
			rot := Rotation{ID: newID(name), Name: name, Created: now, Updated: now, Slots: slots}
			a.putRotation(rot)
			resp["id"] = rot.ID
			msg = fmt.Sprintf("Imported %s (%d new fits).", name, added)
		}
		resp["message"] = msg
		reply(w, resp)
	}))

	// The player has looked through the Fit Catalogue: what's in it now is no longer new.
	mux.HandleFunc("/api/seen", post(func(w http.ResponseWriter, r *http.Request) {
		a.cfg.SeenUpTo = time.Now()
		a.saveConfig()
		reply(w, map[string]string{})
	}))

	mux.HandleFunc("/api/preview", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Hash string       `json:"hash"`
			View *PreviewView `json:"view"`
		}
		if decode(r, &req) != nil || !hashRe.MatchString(req.Hash) {
			fail(w, 400, "bad request")
			return
		}
		if req.View == nil {
			delete(a.cfg.Views, req.Hash)
		} else {
			v := *req.View
			v.Z = math.Min(5, math.Max(1, v.Z))
			v.F = math.Min(1, math.Max(-1, v.F))
			v.X = math.Min(1, math.Max(-1, v.X))
			v.N = max(0, min(v.N, maxFrames))
			if math.IsNaN(v.Z) || math.IsNaN(v.F) || math.IsNaN(v.X) {
				fail(w, 400, "bad request")
				return
			}
			if a.cfg.Views == nil {
				a.cfg.Views = map[string]PreviewView{}
			}
			a.cfg.Views[req.Hash] = v
		}
		a.saveConfig()
		reply(w, map[string]string{})
	}))
	mux.HandleFunc("/api/card-sizes", post(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]CardSize
		if decode(r, &req) != nil || len(req) > 5000 {
			fail(w, 400, "bad request")
			return
		}
		clean := map[string]CardSize{}
		for k, v := range req {
			if len(k) > 300 || v.W < 1 || v.W > 3 || v.H < 0 || v.H > 60 || (v.W == 1 && v.H == 0) {
				continue
			}
			clean[k] = v
		}
		if len(clean) == 0 {
			clean = nil
		}
		a.cfg.CardSizes = clean
		a.saveConfig()
		reply(w, map[string]string{})
	}))
	// The update's calls don't hold the app's lock: they wait on GitHub.
	postFree := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				fail(w, 405, "POST only")
				return
			}
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				fail(w, 403, "not allowed")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/update/check", postFree(func(w http.ResponseWriter, r *http.Request) {
		a.checkUpdate()
		reply(w, updateState())
	}))
	mux.HandleFunc("/api/update/install", postFree(func(w http.ResponseWriter, r *http.Request) {
		if v := updateState(); !v.Available || v.State == "downloading" || v.State == "installing" {
			fail(w, 409, "There's no update to install right now.")
			return
		}
		go func() {
			defer recoverLog("update install")
			a.installUpdate()
		}()
		reply(w, map[string]string{"message": "Downloading the update…"})
	}))
	mux.HandleFunc("/api/settings", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			GameDir     string  `json:"game_dir"`
			PlainBoards *bool   `json:"plain_boards"`
			AfterLoad   *string `json:"after_load"`
			UpdateCheck *bool   `json:"update_check"`
		}
		if decode(r, &req) != nil {
			fail(w, 400, "bad request")
			return
		}
		changed := false
		if req.PlainBoards != nil {
			a.cfg.PlainBoards = *req.PlainBoards
			changed = true
		}
		if req.UpdateCheck != nil {
			a.cfg.NoUpdateCheck = !*req.UpdateCheck
			changed = true
		}
		if req.AfterLoad != nil {
			switch *req.AfterLoad {
			case "ask", "game", "stay":
				a.cfg.AfterLoad = *req.AfterLoad
				changed = true
			}
		}
		if changed {
			a.saveConfig()
			if req.GameDir == "" {
				reply(w, map[string]string{"message": "Settings saved."})
				return
			}
		}
		dir := strings.Trim(strings.TrimSpace(req.GameDir), `"`)
		if !isGameDir(dir) {
			fail(w, 400, "That folder doesn't have Skate.exe in it.")
			return
		}
		a.cfg.GameDir = dir
		a.wantedSig = ""
		a.saveConfig()
		reply(w, map[string]string{"message": "Game folder saved."})
	}))

	// Back to skate.: its window to the front, the app's minimized.
	mux.HandleFunc("/api/focus-game", post(func(w http.ResponseWriter, r *http.Request) {
		if !focusGame() {
			fail(w, 409, "Couldn't bring skate. to the front. Is it still running?")
			return
		}
		reply(w, map[string]string{})
	}))
	// The game's save file backups: the list, and putting one back.
	mux.HandleFunc("/api/backups", lock(func(w http.ResponseWriter, r *http.Request) {
		reply(w, listBackups(a.dir("backups")))
	}))
	mux.HandleFunc("/api/backups/restore", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if decode(r, &req) != nil || !backupNameRe.MatchString(req.Name) || !fileExists(a.dir("backups", req.Name, "reskate.sqlite3")) {
			fail(w, 400, "That backup isn't there.")
			return
		}
		if gameRunning() {
			fail(w, 409, "Close skate. first: a backup can only be put back while the game is closed.")
			return
		}
		db := a.profileDB()
		if db == "" {
			fail(w, 409, "The game's save file wasn't found. Check the game folder in Settings.")
			return
		}
		var when string
		for _, b := range listBackups(a.dir("backups")) {
			if b.Name == req.Name {
				when = b.At.Format("Jan 2, 3:04 PM")
			}
		}
		// what is there now is kept too, so putting a backup back can itself be undone
		if _, err := backupProfile(db, a.dir("backups"), "before putting back the backup from "+when, nil); err != nil {
			fail(w, 500, "Couldn't back up the current save first, so nothing was changed: "+err.Error())
			return
		}
		if err := restoreProfile(a.dir("backups", req.Name), db); err != nil {
			fail(w, 500, "Putting the backup back failed: "+err.Error())
			return
		}
		pruneBackups(a.dir("backups"))
		tl.logf("APP: put back the save backup %s", req.Name)
		a.note("ok", "Put back the game's save from %s. The save from just before is kept as a backup too.", when)
		reply(w, map[string]string{"message": "Put back the save from " + when + "."})
	}))
	mux.HandleFunc("/api/open", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ What string }
		decode(r, &req)
		var target string
		switch req.What {
		case "backups":
			target = a.dir("backups")
		case "release":
			page := updateState().Page
			if !strings.HasPrefix(page, "https://github.com/") {
				fail(w, 400, "no release page")
				return
			}
			openURL(page)
			reply(w, map[string]string{})
			return
		case "plugin":
			target = a.dir("PresetWardrobe-plugin.zip")
			if err := os.WriteFile(target, a.pluginZip, 0o644); err != nil {
				fail(w, 500, err.Error())
				return
			}
		default:
			fail(w, 400, "unknown")
			return
		}
		if err := revealInExplorer(target); err != nil {
			fail(w, 500, err.Error())
			return
		}
		if req.What == "plugin" {
			reply(w, map[string]string{"message": "Showing the plugin zip in its folder."})
		} else {
			reply(w, map[string]string{"message": "Opened your backups folder."})
		}
	}))
	return mux
}

const enhanceCacheBytes = 400 << 20 // how much enhanced pictures may use in memory

func dirSize(root string) (n int64) {
	filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return
}

// storageUse: bytes of the pictures themselves, and of the copies that can be made again (thumbnails,
// the cleaned-up board copies and the HQ copies).
func (a *App) storageUse() (kept, rebuildable int64) {
	u := a.storageDetail()
	return u.Originals, u.Thumbs + u.HQ + u.Boards
}

type storageInfo struct {
	Fits      int    `json:"fits"`
	Originals int64  `json:"originals"` // the captured pictures: never removed by Free up space
	Thumbs    int64  `json:"thumbs"`    // small copies for the cards
	HQ        int64  `json:"hq"`        // the HQ copies
	Boards    int64  `json:"boards"`    // the cleaned-up board copies
	DiskFree  uint64 `json:"disk_free"` // free space on the drive the app keeps its pictures on (0: unknown)
	Freed     int64  `json:"freed,omitempty"`
}

func (a *App) storageDetail() storageInfo {
	var u storageInfo
	for _, f := range a.listFits() {
		if !f.Hidden {
			u.Fits++
		}
	}
	total := dirSize(a.dir("pictures"))
	if dirs, _ := os.ReadDir(a.dir("pictures")); dirs != nil {
		for _, d := range dirs {
			if d.IsDir() && hashRe.MatchString(d.Name()) {
				u.Boards += dirSize(a.dir("pictures", d.Name(), "board", litDir))
			}
		}
	}
	u.Originals = total - u.Boards // the cleaned-up board copies sit inside pictures
	u.Thumbs = dirSize(a.dir("thumbs"))
	u.HQ = dirSize(a.dir("hq"))
	u.DiskFree = diskFree(a.dir())
	return u
}

// freeSpace removes every copy that can be made again: the small copies, the cleaned-up board copies and the HQ
// copies (and enhanced pictures held in memory). The captured pictures themselves are never touched. The copies
// come back on their own: small ones when shown, HQ in the background, cleaned-up boards when the app next starts.
func (a *App) freeSpace() int64 {
	before := a.storageDetail()
	os.RemoveAll(a.dir("thumbs"))
	os.RemoveAll(a.dir("hq"))
	a.enh.clear()
	if dirs, _ := os.ReadDir(a.dir("pictures")); dirs != nil {
		for _, d := range dirs {
			if d.IsDir() && hashRe.MatchString(d.Name()) {
				os.RemoveAll(a.dir("pictures", d.Name(), "board", litDir))
			}
		}
	}
	after := a.storageDetail()
	return (before.Thumbs + before.HQ + before.Boards) - (after.Thumbs + after.HQ + after.Boards)
}

// loadOpts: how a rotation (or a single swapped slot) goes into the game.
type loadOpts struct {
	Live bool
	Body bool
	Slot int // -1 for a whole rotation, else the one slot being swapped
}

// CopyLabel is what the automatic before-load copy of the lineup is called.
func (o loadOpts) CopyLabel(name string) string {
	if o.Slot >= 0 {
		return fmt.Sprintf("swapping slot %d", o.Slot+1)
	}
	return "loading " + name
}

// loadRotation puts the fits of rot into the game's slots. The game running with opt.Live: into its memory, and
// into its file once it closes. The game closed: into its file now.
func (a *App) loadRotation(w http.ResponseWriter, rot Rotation, opt loadOpts) {
	opt.Body = true // a load always puts in the whole fit: clothes, board, face and body
	hs := time.Now()
	step := func(what string) {
		if opt.Live {
			tl.logf("APP: load timing: %s at %.2f s", what, time.Since(hs).Seconds())
		}
	}
	db := a.profileDB()
	current, sel, err := readPresets(db, a.dir("tmp"))
	if err != nil {
		fail(w, 500, "Couldn't read your In-Game Lineup, nothing was changed: "+err.Error())
		return
	}
	step("read the game's presets")
	current = a.withPending(current)
	if opt.Slot >= 0 && opt.Slot < slotCount && lineupHashes(current)[opt.Slot] == rot.Slots[opt.Slot] {
		reply(w, map[string]string{"message": fmt.Sprintf("Slot %d already has %s.", opt.Slot+1, rot.Name)})
		return
	}
	a.importSnaps(current, sel) // snaps of what's there now belong to what's there now
	step("kept snaps")
	var writes []StoredPreset
	for i, h := range rot.Slots {
		if h == "" {
			continue
		}
		f, err := a.getFit(h)
		if err != nil {
			continue
		}
		writes = append(writes, StoredPreset{PresetID: presetIDs[i], RecipeFormat: f.RecipeFormat,
			RecipesJSON: f.RecipesJSON, ExtensionsJSON: f.ExtensionsJSON})
	}
	if len(writes) == 0 {
		fail(w, 400, "This rotation has no fits in it yet.")
		return
	}
	step("collected the new fits")
	// Keep what's in the game, unless a rotation already holds exactly that.
	lineup := lineupHashes(current)
	kept := false
	for _, other := range a.listRotations() {
		same := true
		for i := range lineup {
			if other.Slots[i] != lineup[i] {
				same = false
			}
		}
		if same {
			kept = true
		}
	}
	if !kept && !rotationMatches(rot, lineup) {
		slots := make([]string, slotCount)
		for _, p := range current {
			f, _ := a.addFit(p, true)
			for i, id := range presetIDs {
				if id == p.PresetID {
					slots[i] = f.Hash
				}
			}
		}
		now := time.Now()
		copyRot := Rotation{ID: newID("before-load"), Name: "Lineup before " + opt.CopyLabel(rot.Name), Created: now, Updated: now, Auto: true, Slots: slots}
		if err := a.putRotation(copyRot); err != nil {
			fail(w, 500, "Couldn't keep a copy of your In-Game Lineup, nothing was changed: "+err.Error())
			return
		}
	}
	step("kept a copy of the lineup")
	if opt.Live && gameRunning() {
		markLiveLoad()
		// into the running game's memory; its save file is left to the game
		tl.logf("APP: live load of %d presets into the running game", len(writes))
		if d := settleLeft(); d > 0 {
			tl.logf("APP: waiting %.1f s for the game to settle after the last replay", d.Seconds())
			time.Sleep(d)
		}
		inMemory := a.withPendingMem(current) // the game's tables, as they are
		{
			selID := ""
			if sel >= 0 && sel < len(presetIDs) {
				selID = presetIDs[sel]
			}
			setLiveSelected(selID, sel)
			// what the selected preset's skater really wears now (the plugin says), so its tables are recognised
			// even after edits in the game that were never saved
			noteLiveOutfit("", nil, nil)
			if sk, ok := a.readSkaterNow(); ok && selID != "" {
				if _, ok := editsOf(current, selID, sk); ok {
					items, lens := map[uint32]uint32{}, map[uint32]int{}
					for name, item := range sk.Slots {
						items[itemHash(name)], lens[itemHash(name)] = itemHash(item), len(item)
					}
					noteLiveOutfit(selID, items, lens)
				}
			}
		}
		res, err := patchLiveMemory(inMemory, writes, opt.Body, false)
		if err == nil {
			// the game copies its outfit tables after a change; a second quick look catches the copies still holding
			// the old outfit. It runs right after the skater refresh so it never delays it.
			armSweep(inMemory, writes, opt.Body)
		}
		tl.logf("APP: live load: %s (error: %v)", strings.ReplaceAll(res, "\n", "; "), err)
		step("memory patch done")
		if err != nil {
			fail(w, 500, "Couldn't change the running game: "+err.Error())
			return
		}
		mem := map[string]string{}
		for _, o := range inMemory {
			for _, w := range writes {
				if o.PresetID == w.PresetID {
					mem[w.PresetID] = reshape(o.RecipesJSON, w.RecipesJSON, opt.Body)
				}
			}
		}
		a.saveLiveState(findSkatePID())
		a.addPending(writes, mem) // shown as what's in the game now; written to its file when it closes
		step("saved pending")
		a.prune()
		step("pruned")
		var loaded []string
		for _, p := range writes {
			loaded = append(loaded, p.PresetID)
		}
		{
			selID := "none"
			if sel >= 0 && sel < len(presetIDs) {
				selID = presetIDs[sel]
			}
			tl.logf("APP: live load: the game has preset %s (slot %d) selected; loading into %v", selID, sel, loaded)
		}
		// What happened, in plain lines for the activity log, and a short message for the pop-up.
		var lines []logLine
		for _, l := range strings.Split(res, "\n") {
			level := "ok"
			if strings.Contains(l, "isn't installed") || strings.Contains(l, "differs") || strings.Contains(l, "couldn't") || strings.Contains(l, "skipped") {
				level = "warn"
			}
			lines = append(lines, logLine{level, l})
		}
		next := ""
		if sel >= 0 && sel < len(presetIDs) {
			for _, id := range loaded {
				if id == presetIDs[sel] {
					a.armRefresh(loaded)
					next = "Your skater refreshes by itself once you're in the world with no menu open and skate. in front."
					break
				}
			}
		}
		if next != "" {
			lines = append(lines, logLine{"info", next})
		}
		lines = append(lines, logLine{"info", "It's saved to the game's file when you close skate."})
		short := fmt.Sprintf("Loaded %s into the game.", rot.Name)
		if opt.Slot >= 0 {
			short = fmt.Sprintf("Slot %d now has %s in the game.", opt.Slot+1, rot.Name)
		}
		headline := short
		for _, l := range lines {
			if l.Level == "warn" {
				short += " Some parts need a look: see the activity log."
				break
			}
		}
		reply(w, map[string]any{"message": short, "headline": headline, "log": lines})
		step("replied")
		return
	}
	var ids []string
	for _, p := range writes {
		ids = append(ids, p.PresetID)
	}
	a.dropPending(ids)
	tl.logf("APP: writing %d presets", len(writes))
	why := "loading " + rot.Name
	if opt.Slot >= 0 {
		why = fmt.Sprintf("loading %s into slot %d", rot.Name, opt.Slot+1)
	}
	backup, err := writePresets(db, a.dir("backups"), writes, false, why)
	tl.logf("APP: write finished (error: %v)", err)
	if err != nil {
		fail(w, 500, "Loading failed: "+err.Error()+". Your profile backup is in "+backup)
		return
	}
	a.prune()
	msg := fmt.Sprintf("Loaded %s into %d slots. Start skate. to wear them.", rot.Name, len(writes))
	if opt.Slot >= 0 {
		msg = fmt.Sprintf("Slot %d now has %s. It will be there next time you start skate.", opt.Slot+1, rot.Name)
	}
	reply(w, map[string]string{"message": msg})
}
