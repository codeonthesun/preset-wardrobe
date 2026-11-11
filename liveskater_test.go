package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func testRecipes() string {
	bits := func(f float32) uint32 { return math.Float32bits(f) }
	return fmt.Sprintf(`[{"items":[
{"asset":"Own_CustSkinType_Tone_00160","parameter_bits":[],"slot":%d},
{"asset":"Own_CustHead_00009","parameter_bits":[],"slot":%d},
{"asset":"Own_Shirt","parameter_bits":[],"slot":%d},
{"asset":"Own_Missing_Hat","parameter_bits":[],"slot":%d},
{"asset":"Own_TattooFlash_BarbedWire_00001","parameter_bits":[%d,%d],"slot":%d}]},
{"items":[{"asset":"Own_Deck","parameter_bits":[],"slot":1}]}]`,
		itemHash("cust_skintypes"), itemHash("cust_heads"), itemHash("cust_tops"), itemHash("cust_hats"),
		bits(0.5), bits(0.25), itemHash("tattoo_armL_1"))
}

func testNow() skaterNow {
	s := skaterNow{N: 30, Slots: map[string]string{}, Params: map[string][]float64{}}
	for i := 0; i < 30; i++ {
		s.Slots[fmt.Sprintf("cust_other_%d", i)] = "x"
	}
	s.Slots["cust_skintypes"] = "Own_CustSkinType_Tone_00160"
	s.Slots["cust_heads"] = "Own_CustHead_00009"
	s.Slots["cust_tops"] = "own_shirt" // the case differs only
	s.Slots["cust_hats"] = ""          // the game shows a default for an item it hasn't got
	s.Slots["tattoo_armL_1"] = "Own_TattooFlash_BarbedWire_00001"
	s.Params["tattoo_armL_1"] = []float64{0.5, 0.25}
	return s
}

func missingHat(n string) bool { return n == "Own_Missing_Hat" }

func hatIsMissing() { // the game "said" it hasn't got the hat
	itemMu.Lock()
	if itemKnown == nil {
		itemKnown = map[string]bool{}
	}
	itemKnown["Own_Missing_Hat"] = true
	itemMu.Unlock()
}

func TestLiveDiffNothingWhenSame(t *testing.T) {
	if d := liveDiff(testRecipes(), testNow(), missingHat); len(d) != 0 {
		t.Fatalf("expected no differences, got %+v", d)
	}
}

func TestLiveDiffFindsEdits(t *testing.T) {
	n := testNow()
	n.Slots["cust_skintypes"] = "Own_CustSkinType_Tone_00240"
	n.Slots["cust_heads"] = "Own_CustHead_00003"
	n.Params["tattoo_armL_1"] = []float64{0.9, 0.25}
	d := liveDiff(testRecipes(), n, missingHat)
	if len(d) != 3 {
		t.Fatalf("expected 3 differences, got %+v", d)
	}
	text := strings.Join(describeChanges(d), " | ")
	for _, want := range []string{"Skin tone: ", "Head: ", "Tattoo armL 1: adjusted"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from %q", want, text)
		}
	}
}

func TestApplyLiveChangesOnlyThoseItems(t *testing.T) {
	n := testNow()
	n.Slots["cust_skintypes"] = "Own_CustSkinType_Tone_00240"
	n.Params["tattoo_armL_1"] = []float64{0.75, 0.25}
	d := liveDiff(testRecipes(), n, missingHat)
	out := applyLive(testRecipes(), d)
	g, err := parseRecipes(out)
	if err != nil || len(g) != 2 {
		t.Fatalf("bad result: %v %s", err, out)
	}
	if g[0].Items[0].Asset != "Own_CustSkinType_Tone_00240" || g[0].Items[1].Asset != "Own_CustHead_00009" || g[0].Items[3].Asset != "Own_Missing_Hat" {
		t.Errorf("wrong items: %+v", g[0].Items)
	}
	var bits []uint32
	json.Unmarshal(g[0].Items[4].ParameterBit, &bits)
	if len(bits) != 2 || math.Abs(float64(math.Float32frombits(bits[0]))-0.75) > 1e-6 {
		t.Errorf("tattoo parameters not applied: %v", bits)
	}
	if g[1].Items[0].Asset != "Own_Deck" {
		t.Errorf("the board group changed: %+v", g[1])
	}
	// a changed skater is a different fit
	if outfitHash(out) == outfitHash(testRecipes()) {
		t.Error("the fit's identity didn't change")
	}
}

func TestWithLiveOverlaysOnlyTheSelectedPreset(t *testing.T) {
	n := testNow()
	n.Slots["cust_heads"] = "Own_CustHead_00003"
	d := liveDiff(testRecipes(), n, missingHat)
	liveSeenMu.Lock()
	liveShown = &liveSeen{PresetID: "b", Changes: d}
	liveSeenMu.Unlock()
	defer func() { liveSeenMu.Lock(); liveShown = nil; liveSeenMu.Unlock() }()
	a := &App{}
	in := []StoredPreset{{PresetID: "a", RecipesJSON: testRecipes()}, {PresetID: "b", RecipesJSON: testRecipes()}}
	out := a.withLive(in)
	if out[0].RecipesJSON != in[0].RecipesJSON {
		t.Error("another preset changed")
	}
	if outfitHash(out[1].RecipesJSON) == outfitHash(in[1].RecipesJSON) {
		t.Error("the selected preset didn't change")
	}
	if in[1].RecipesJSON != testRecipes() {
		t.Error("the input was modified")
	}
}

func TestEditsOfIgnoresAnotherPresetOnShow(t *testing.T) {
	hatIsMissing()
	other := strings.Replace(testRecipes(), "Own_CustHead_00009", "Own_CustHead_00003", 1)
	presets := []StoredPreset{{PresetID: presetIDs[0], RecipesJSON: testRecipes()}, {PresetID: presetIDs[1], RecipesJSON: other}}
	n := testNow()
	n.Slots["cust_heads"] = "Own_CustHead_00003" // exactly what preset 2 has
	if _, ok := editsOf(presets, presetIDs[0], n); ok {
		t.Error("the game showing another preset was taken for an edit")
	}
	n.Slots["cust_heads"] = "Own_CustHead_00005" // nobody's: an edit
	d, ok := editsOf(presets, presetIDs[0], n)
	if !ok || len(d) != 1 {
		t.Errorf("expected one edit, got %v %+v", ok, d)
	}
}

func TestSnapOutfitFilesWhatWasWorn(t *testing.T) {
	hatIsMissing()
	other := strings.Replace(testRecipes(), "Own_Shirt", "Own_Jacket", 1)
	presets := []StoredPreset{{PresetID: presetIDs[0], RecipesJSON: other}, {PresetID: presetIDs[1], RecipesJSON: testRecipes()}}
	n := testNow()
	n.Slots["cust_skintypes"] = "Own_CustSkinType_Tone_00240" // edited in the game, never saved
	slot, recipes, ok := snapOutfit(presets, 0, nil, n)
	if !ok || slot != 2 {
		t.Fatalf("expected slot 2 (the shirt matches it), got %d %v", slot, ok)
	}
	g, _ := parseRecipes(recipes)
	if g[0].Items[0].Asset != "Own_CustSkinType_Tone_00240" || g[0].Items[2].Asset != "Own_Shirt" {
		t.Errorf("wrong outfit filed: %+v", g[0].Items)
	}
	if _, _, ok := snapOutfit(presets, 0, nil, skaterNow{}); ok {
		t.Error("a snap without the skater's slots must fall back to the old way")
	}
}

func TestSkaterNowReadsEmptyTablesEitherWay(t *testing.T) {
	for _, js := range []string{`{"n":1,"slots":{"a":"b"},"params":[]}`, `{"n":1,"slots":{"a":"b"},"params":{}}`, `{"n":0,"slots":[],"params":{"a":[0.5]}}`} {
		var s skaterNow
		if err := json.Unmarshal([]byte(js), &s); err != nil {
			t.Errorf("%s: %v", js, err)
		}
	}
	var idx snapIndex
	if err := json.Unmarshal([]byte(`{"singles":[{"seq":1,"file":"fit-1.png","worn":{},"slots":[],"params":[]},{"seq":2,"file":"fit-2.png","slots":{"cust_tops":"x"},"params":{"t":[1,2]}}]}`), &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Singles) != 2 || idx.Singles[1].Slots["cust_tops"] != "x" {
		t.Errorf("snap index read wrong: %+v", idx.Singles)
	}
}
