package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const riRecipes = `[{"items":[{"asset":"Own_Tee","parameter_bits":[1],"slot":10},{"asset":"Own_Blunt","parameter_bits":[2],"slot":20}]},{"items":[{"asset":"Own_Deck","parameter_bits":[],"slot":30}]},{"items":[{"asset":"Own_Blunt","parameter_bits":[],"slot":99}]}]`

func riApp(t *testing.T) *App {
	t.Helper()
	data := t.TempDir()
	a := &App{dataDir: data, cfg: Config{Imported: map[string]int64{}, Views: map[string]PreviewView{}}}
	for _, d := range []string{"fits", "rotations", "pictures", "backups", "thumbs"} {
		os.MkdirAll(a.dir(d), 0o755)
	}
	itemMu.Lock()
	itemKnown, itemMissing = map[string]bool{"Own_Blunt": true}, map[string]bool{"Own_Blunt": true}
	itemMu.Unlock()
	return a
}

func TestRemoveMissingItemMovesEverything(t *testing.T) {
	a := riApp(t)
	h := outfitHash(riRecipes)
	a.putFit(Fit{Hash: h, Name: "Mine", Favorite: true, RecipeFormat: 1, RecipesJSON: riRecipes, ExtensionsJSON: "{}"})
	os.WriteFile(a.dir("pictures", h+".png"), []byte("png"), 0o644)
	os.MkdirAll(a.dir("pictures", h), 0o755)
	os.WriteFile(a.dir("pictures", h, "1.png"), []byte("f"), 0o644)
	a.putRotation(Rotation{ID: "r1", Name: "R", Slots: []string{h}})
	a.cfg.Views[h] = PreviewView{Z: 2}

	nh, merged, err := a.removeMissingItem(h, "Own_Blunt")
	if err != nil || merged || nh == h {
		t.Fatalf("got %q merged=%v err=%v", nh, merged, err)
	}
	f, err := a.getFit(nh)
	if err != nil || !f.Favorite || f.Name != "Mine" || strings.Contains(f.RecipesJSON, "Own_Blunt") != true {
		// the gesture group (third) is left alone, so one Own_Blunt stays there
		t.Fatalf("new fit wrong: %+v %v", f, err)
	}
	if strings.Count(f.RecipesJSON, "Own_Blunt") != 1 || outfitHash(f.RecipesJSON) != nh {
		t.Fatalf("recipe not cleared in the skater group only: %s", f.RecipesJSON)
	}
	if _, err := a.getFit(h); err == nil {
		t.Fatal("old fit still there")
	}
	for _, p := range []string{nh + ".png", filepath.Join(nh, "1.png")} {
		if _, err := os.Stat(a.dir("pictures", p)); err != nil {
			t.Fatalf("picture %s didn't move", p)
		}
	}
	if r, _ := a.getRotation("r1"); r.Slots[0] != nh {
		t.Fatalf("rotation still points at %s", r.Slots[0])
	}
	if _, ok := a.cfg.Views[nh]; !ok {
		t.Fatal("preview setting didn't follow")
	}
	if m, _ := filepath.Glob(a.dir("backups", "removed-items", "*.json")); len(m) != 1 {
		t.Fatal("old fit not kept in backups")
	}
}

func TestRemoveMissingItemMerges(t *testing.T) {
	a := riApp(t)
	h := outfitHash(riRecipes)
	a.putFit(Fit{Hash: h, RecipeFormat: 1, RecipesJSON: riRecipes, ExtensionsJSON: "{}"})
	os.WriteFile(a.dir("pictures", h+".png"), []byte("old"), 0o644)
	clean, _, _ := clearItem(riRecipes, "Own_Blunt")
	ch := outfitHash(clean)
	a.putFit(Fit{Hash: ch, RecipeFormat: 1, RecipesJSON: clean, ExtensionsJSON: "{}"})
	os.WriteFile(a.dir("pictures", ch+".png"), []byte("keep"), 0o644)
	nh, merged, err := a.removeMissingItem(h, "Own_Blunt")
	if err != nil || !merged || nh != ch {
		t.Fatalf("got %q merged=%v err=%v", nh, merged, err)
	}
	if b, _ := os.ReadFile(a.dir("pictures", ch+".png")); string(b) != "keep" {
		t.Fatal("the existing fit's picture was replaced")
	}
	if m, _ := filepath.Glob(a.dir("backups", "removed-items", "*")); len(m) < 2 {
		t.Fatal("the old picture wasn't set aside")
	}
}

func TestRemoveMissingItemRefusesInstalled(t *testing.T) {
	a := riApp(t)
	h := outfitHash(riRecipes)
	a.putFit(Fit{Hash: h, RecipeFormat: 1, RecipesJSON: riRecipes, ExtensionsJSON: "{}"})
	if _, _, err := a.removeMissingItem(h, "Own_Tee"); err == nil {
		t.Fatal("removed an installed item")
	}
}
