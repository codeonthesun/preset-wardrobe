package main

import "sync"

// withCatalogMatch: a preset the game saved as a catalogue fit, apart from what the game can't hold, is shown as that
// fit. ReSkate saves items that aren't installed back into a preset (it keeps them for when they're installed again),
// so a preset loaded from the catalogue can come back from the game's save with those items in a few slots and match
// no fit exactly: it's still the fit (fitInFile), with its pictures. Only presets that match no fit exactly are looked
// at, and the answers are remembered until the catalogue changes. Called with a.mu held.
var catalogMatch struct {
	sync.Mutex
	fits  int
	cache map[string]string // preset recipes -> the catalogue fit's recipes ("" for none)
}

func (a *App) withCatalogMatch(cur []StoredPreset) []StoredPreset {
	fits := a.listFits()
	exact := map[string]bool{}
	for _, f := range fits {
		exact[f.Hash] = true
	}
	catalogMatch.Lock()
	defer catalogMatch.Unlock()
	if catalogMatch.cache == nil || catalogMatch.fits != len(fits) {
		catalogMatch.cache, catalogMatch.fits = map[string]string{}, len(fits)
	}
	out := make([]StoredPreset, len(cur))
	copy(out, cur)
	for i, p := range out {
		if exact[outfitHash(p.RecipesJSON)] {
			continue
		}
		match, known := catalogMatch.cache[p.RecipesJSON]
		if !known {
			for _, f := range fits {
				if f.Hidden {
					continue
				}
				if same, held := fitInFile(p.RecipesJSON, f.RecipesJSON, true); same && held > 0 {
					match = f.RecipesJSON
					break
				}
			}
			catalogMatch.cache[p.RecipesJSON] = match
		}
		if match != "" {
			out[i].RecipesJSON = match
		}
	}
	return out
}
