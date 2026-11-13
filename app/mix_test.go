package main

import "testing"

func TestMixRecipes(t *testing.T) {
	skater := `[{"items":[{"asset":"own_top_a","slot":1,"parameter_bits":0}]},{"items":[{"asset":"own_deck_a","slot":2,"parameter_bits":0}]},{"items":[{"asset":"gesture_a","slot":9,"parameter_bits":0}]}]`
	board := `[{"items":[{"asset":"own_top_b","slot":1,"parameter_bits":0}]},{"items":[{"asset":"own_deck_b","slot":2,"parameter_bits":0}]}]`
	mixed, err := mixRecipes(skater, board)
	if err != nil {
		t.Fatal(err)
	}
	g, err := parseRecipes(mixed)
	if err != nil || len(g) != 3 {
		t.Fatalf("groups: %v %d", err, len(g))
	}
	if g[0].Items[0].Asset != "own_top_a" || g[1].Items[0].Asset != "own_deck_b" || g[2].Items[0].Asset != "gesture_a" {
		t.Fatalf("wrong pieces in the mix: %s", mixed)
	}
	if outfitHash(mixed) == outfitHash(skater) || outfitHash(mixed) == outfitHash(board) {
		t.Fatal("a mix should be a fit of its own")
	}
	if _, err := mixRecipes(skater, `[{"items":[]}]`); err == nil {
		t.Fatal("a fit without a board can't give one")
	}
}
