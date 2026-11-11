package main

import "testing"

func TestReshape(t *testing.T) {
	shape := `[{"items":[{"asset":"","parameter_bits":[],"slot":1},{"asset":"OldHat","parameter_bits":[],"slot":2},{"asset":"OldTop","parameter_bits":[],"slot":3}],"scalar_bits":7}]`
	fit := `[{"items":[{"asset":"NewTop","parameter_bits":[],"slot":3},{"asset":"NewHat","parameter_bits":[],"slot":2},{"asset":"Extra","parameter_bits":[],"slot":9}]}]`
	got, err := parseRecipes(reshape(shape, fit, true))
	if err != nil || len(got) != 1 || len(got[0].Items) != 3 {
		t.Fatalf("shape lost: %v %+v", err, got)
	}
	want := []string{"", "NewHat", "NewTop"} // memory's slot order, the fit's items, nothing extra
	for i, it := range got[0].Items {
		if it.Asset != want[i] {
			t.Errorf("item %d = %q, want %q", i, it.Asset, want[i])
		}
	}
}

func TestReshapeKeepsBodyWhenAsked(t *testing.T) {
	// slot 2889696005 is headwear (clothing); slot 12345 is not one of the clothing or board slots
	shape := `[{"items":[{"asset":"OldHat","parameter_bits":[],"slot":2889696005},{"asset":"OldHair","parameter_bits":[],"slot":12345}]}]`
	fit := `[{"items":[{"asset":"NewHat","parameter_bits":[],"slot":2889696005},{"asset":"NewHair","parameter_bits":[],"slot":12345}]}]`
	got, err := parseRecipes(reshape(shape, fit, false))
	if err != nil || len(got) != 1 || len(got[0].Items) != 2 {
		t.Fatalf("shape lost: %v %+v", err, got)
	}
	if got[0].Items[0].Asset != "NewHat" || got[0].Items[1].Asset != "OldHair" {
		t.Errorf("got %q and %q, want NewHat and OldHair", got[0].Items[0].Asset, got[0].Items[1].Asset)
	}
}
