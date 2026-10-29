package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- what Preset Wardrobe keeps ---------------------------------------------------------------
//
// %LOCALAPPDATA%\PresetWardrobe\
//   fits\<hash>.json        one saved outfit each: the Fit Catalogue
//   pictures\<hash>.png     its picture
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
}

type Fit struct {
	Hash           string    `json:"hash"`
	Name           string    `json:"name,omitempty"` // empty: named after its main piece
	Added          time.Time `json:"added"`
	Hidden         bool      `json:"hidden,omitempty"` // kept only for a before-load copy
	RecipeFormat   int       `json:"recipe_format"`
	RecipesJSON    string    `json:"recipes_json"`
	ExtensionsJSON string    `json:"extensions_json"`
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

type App struct {
	mu        sync.Mutex
	dataDir   string
	localApp  string
	cfg       Config
	titles    map[string]string
	names     map[string]string // hash -> automatic name, worked out each refresh
	wantedSig string
	lastSeen  time.Time
	pluginZip []byte
	acks      []ackMsg
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
	readJSON(a.dir("config.json"), &a.cfg)
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
	a.saveConfig()
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
	var found []string
	for _, n := range []string{"jrkmods-PresetWardrobe", "PresetWardrobe", "PresetPages"} {
		if isPlugin(n) {
			found = append(found, n)
		}
	}
	if len(found) == 0 {
		entries, _ := os.ReadDir(mods)
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && isPlugin(e.Name()) {
				found = append(found, e.Name())
			}
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

func (a *App) pictureURL(hash string) string {
	st, err := os.Stat(a.dir("pictures", hash+".png"))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("/pic/%s.png?v=%d", hash, st.ModTime().Unix())
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
	dirs, _ := filepath.Glob(a.dir("backups", "*"))
	sort.Strings(dirs)
	for i := 0; i < len(dirs)-20; i++ {
		os.RemoveAll(dirs[i])
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

type snapIndex struct {
	Shots []*struct {
		N    int      `json:"n"`
		Worn []string `json:"worn"`
	} `json:"shots"`
	Singles []*struct {
		Seq  int      `json:"seq"`
		File string   `json:"file"`
		Worn []string `json:"worn"`
	} `json:"singles"`
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
	var idx snapIndex
	if readJSON(filepath.Join(dir, "snaps", "index.json"), &idx) != nil {
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
		name := fmt.Sprintf("%02d.png", s.N)
		src := filepath.Join(dir, "snaps", name)
		st, err := os.Stat(src)
		if err != nil || a.cfg.Imported[name] == st.ModTime().UnixNano() {
			continue
		}
		if p := presetFor(s.N); p != nil {
			f, _ := a.addFit(*p, false)
			if copyFile(src, a.dir("pictures", f.Hash+".png")) == nil {
				walked++
			}
		}
		a.cfg.Imported[name] = st.ModTime().UnixNano()
		changed = true
	}
	if walked > 0 {
		events = append(events, fmt.Sprintf("Added %d fits from your walk-through to the Fit Catalogue.", walked))
		a.ack(fmt.Sprintf("Added %d fits to your Fit Catalogue.", walked), false)
	}
	for _, s := range idx.Singles {
		// A snap still in the folder hasn't been filed: filing removes it. (Not by number: the
		// plugin's counter starts again if it's reinstalled under another folder name.)
		if s == nil || !regexp.MustCompile(`^fit-\d+\.png$`).MatchString(s.File) {
			continue
		}
		src := filepath.Join(dir, "snaps", s.File)
		if _, err := os.Stat(src); err != nil {
			continue // filed already, or not written yet
		}
		if s.Seq > a.cfg.LastSingle {
			a.cfg.LastSingle = s.Seq
		}
		changed = true
		best, confident := guessSlot(current, selected, s.Worn)
		if confident {
			p := presetFor(best[0])
			f, isNew := a.addFit(*p, false)
			copyFile(src, a.dir("pictures", f.Hash+".png"))
			os.Remove(src)
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
		if copyFile(src, a.dir("pending", id+".png")) == nil {
			writeJSON(a.dir("pending", id+".json"), Pending{ID: id, At: time.Now(), Guess: best[0], Candidates: best})
			os.Remove(src)
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
	Slot        int     `json:"slot"`
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	InCatalogue bool    `json:"in_catalogue"`
	Pieces      []Piece `json:"pieces"`
	Picture     string  `json:"picture"`
	Selected    bool    `json:"selected"`
}

type FitView struct {
	Hash      string    `json:"hash"`
	Name      string    `json:"name"`
	Pieces    []Piece   `json:"pieces"`
	Picture   string    `json:"picture"`
	Added     time.Time `json:"added"`
	Hidden    bool      `json:"hidden"`
	InGame    bool      `json:"in_game"`
	Rotations []string  `json:"rotations"`
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
	ID         string `json:"id"`
	Picture    string `json:"picture"`
	Guess      int    `json:"guess"`
	Candidates []int  `json:"candidates"`
}

type apiState struct {
	GameRunning     bool           `json:"game_running"`
	ProfileDB       string         `json:"profile_db"`
	ProfileError    string         `json:"profile_error,omitempty"`
	GameDir         string         `json:"game_dir"`
	GameDirOK       bool           `json:"game_dir_ok"`
	PluginInstalled bool           `json:"plugin_installed"`
	Lineup          []SlotView     `json:"lineup"`
	Fits            []FitView      `json:"fits"`
	Rotations       []RotationView `json:"rotations"`
	Pending         []PendingView  `json:"pending"`
	Events          []string       `json:"events"`
	DefaultName     string         `json:"default_rotation_name"`
	Version         string         `json:"version"`
}

func (a *App) piecesWithTitles(recipes string) []Piece {
	pieces := piecesOf(recipes)
	if pieces == nil {
		return []Piece{}
	}
	for i := range pieces {
		pieces[i].Title = a.title(pieces[i].Key)
	}
	return pieces
}

func (a *App) state() apiState {
	st := apiState{GameRunning: gameRunning(), ProfileDB: a.profileDB(), GameDir: a.cfg.GameDir,
		GameDirOK: isGameDir(a.cfg.GameDir), PluginInstalled: a.pluginInstalled(), Version: appVersion,
		Lineup: []SlotView{}, Fits: []FitView{}, Rotations: []RotationView{}, Pending: []PendingView{}, Events: []string{}}
	current, sel, err := readPresets(st.ProfileDB, a.dir("tmp"))
	if err != nil {
		st.ProfileError = "Couldn't read your skater profile: " + err.Error()
	}
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
			Pieces: a.piecesWithTitles(p.RecipesJSON), Picture: a.pictureURL(h), Selected: sel == slot-1})
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
			Picture: a.pictureURL(f.Hash), Added: f.Added, Hidden: f.Hidden, InGame: inGame[f.Hash], Rotations: rs})
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
		st.Pending = append(st.Pending, PendingView{ID: p.ID, Picture: "/pending/" + p.ID + ".png", Guess: p.Guess, Candidates: p.Candidates})
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
		return current, true
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
	picRe := regexp.MustCompile(`^[0-9a-f]{16}\.png$`)
	mux.HandleFunc("/pic/", lock(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/pic/")
		if !picRe.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "max-age=31536000")
		http.ServeFile(w, r, a.dir("pictures", name))
	}))
	pendRe := regexp.MustCompile(`^snap-\d+\.png$`)
	mux.HandleFunc("/pending/", lock(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/pending/")
		if !pendRe.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, a.dir("pending", name))
	}))

	mux.HandleFunc("/api/state", lock(func(w http.ResponseWriter, r *http.Request) { reply(w, a.state()) }))

	// Add one slot of the In-Game Lineup to the catalogue.
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
				copyFile(pic, a.dir("pictures", f.Hash+".png"))
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

	// Put a rotation's fits into the game's slots (game closed).
	mux.HandleFunc("/api/rotation/load", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID string }
		if decode(r, &req) != nil {
			fail(w, 400, "bad request")
			return
		}
		if gameRunning() {
			fail(w, 409, "Close skate. first: the game rewrites your presets when it quits.")
			return
		}
		rot, err := a.getRotation(req.ID)
		if err != nil {
			fail(w, 404, err.Error())
			return
		}
		db := a.profileDB()
		current, sel, err := readPresets(db, a.dir("tmp"))
		if err != nil {
			fail(w, 500, "Couldn't read your In-Game Lineup, nothing was changed: "+err.Error())
			return
		}
		a.importSnaps(current, sel) // snaps of what's there now belong to what's there now
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
			copyRot := Rotation{ID: newID("before-load"), Name: "Before loading " + rot.Name, Created: now, Updated: now, Auto: true, Slots: slots}
			if err := a.putRotation(copyRot); err != nil {
				fail(w, 500, "Couldn't keep a copy of your In-Game Lineup, nothing was changed: "+err.Error())
				return
			}
		}
		backup, err := writePresets(db, a.dir("backups"), writes)
		if err != nil {
			fail(w, 500, "Loading failed: "+err.Error()+". Your profile backup is in "+backup)
			return
		}
		a.prune()
		msg := fmt.Sprintf("Loaded %s into %d slots. Start skate. to wear them.", rot.Name, len(writes))
		reply(w, map[string]string{"message": msg})
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

	mux.HandleFunc("/api/settings", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			GameDir string `json:"game_dir"`
		}
		if decode(r, &req) != nil {
			fail(w, 400, "bad request")
			return
		}
		dir := strings.Trim(strings.TrimSpace(req.GameDir), `"`)
		if !isGameDir(dir) {
			fail(w, 400, "That folder doesn't have Skate.exe in it.")
			return
		}
		a.cfg.GameDir = dir
		a.wantedSig = ""
		a.saveConfig()
		reply(w, map[string]string{"message": "Game folder set."})
	}))

	mux.HandleFunc("/api/open", post(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ What string }
		decode(r, &req)
		var target string
		switch req.What {
		case "backups":
			target = a.dir("backups")
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
		reply(w, map[string]string{"message": "Opened."})
	}))
	return mux
}
