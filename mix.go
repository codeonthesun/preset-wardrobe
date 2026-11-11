package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Mix & match: a new fit made of one fit's skater (clothes, face and body) and another fit's board. The game
// stores a fit as a list of groups: the first is the skater, the second the board, and what follows is gestures.

// mixRecipes puts the board group of boardRecipes into skaterRecipes.
func mixRecipes(skaterRecipes, boardRecipes string) (string, error) {
	var sk, bd []json.RawMessage
	if json.Unmarshal([]byte(skaterRecipes), &sk) != nil || len(sk) == 0 {
		return "", errors.New("the skater's fit can't be read")
	}
	if json.Unmarshal([]byte(boardRecipes), &bd) != nil || len(bd) < 2 {
		return "", errors.New("the board's fit has no board in it")
	}
	if len(sk) < 2 {
		sk = append(sk, bd[1])
	} else {
		sk[1] = bd[1]
	}
	out, err := json.Marshal(sk)
	return string(out), err
}

var numPng = regexp.MustCompile(`^[0-9]+\.png$`)

// copyPictures gives the mixed fit pictures: the skater's from one fit, the board's from the other.
func (a *App) copyPictures(skater, board, mixed string) {
	pics := a.dir("pictures")
	os.MkdirAll(filepath.Join(pics, mixed), 0o755)
	copyFile(filepath.Join(pics, skater+".png"), filepath.Join(pics, mixed+".png"))
	if entries, err := os.ReadDir(filepath.Join(pics, skater)); err == nil {
		for _, e := range entries {
			if !e.IsDir() && (numPng.MatchString(e.Name()) || e.Name() == ".aligned3") {
				copyFile(filepath.Join(pics, skater, e.Name()), filepath.Join(pics, mixed, e.Name()))
			}
		}
	}
	src := filepath.Join(pics, board, "board")
	if _, err := os.Stat(src); err != nil {
		return
	}
	dst := filepath.Join(pics, mixed, "board")
	filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			os.MkdirAll(filepath.Join(dst, rel), 0o755)
			return nil
		}
		copyFile(p, filepath.Join(dst, rel))
		return nil
	})
}

// mixFits files the new fit. existed: the same fit was already there, and nothing was changed.
func (a *App) mixFits(skaterHash, boardHash, name string) (Fit, bool, error) {
	sk, err := a.getFit(skaterHash)
	if err != nil {
		return Fit{}, false, err
	}
	bd, err := a.getFit(boardHash)
	if err != nil {
		return Fit{}, false, err
	}
	recipes, err := mixRecipes(sk.RecipesJSON, bd.RecipesJSON)
	if err != nil {
		return Fit{}, false, err
	}
	h := outfitHash(recipes)
	if f, err := a.getFit(h); err == nil {
		if f.Hidden {
			f.Hidden, f.Added = false, time.Now()
			a.putFit(f)
		}
		return f, true, nil
	}
	f := Fit{Hash: h, Name: cleanName(name, 60), Added: time.Now(), RecipeFormat: sk.RecipeFormat,
		RecipesJSON: recipes, ExtensionsJSON: sk.ExtensionsJSON, Mixed: true}
	if f.RecipeFormat == 0 {
		f.RecipeFormat = 1
	}
	if f.ExtensionsJSON == "" {
		f.ExtensionsJSON = "{}"
	}
	a.copyPictures(skaterHash, boardHash, h)
	if st, err := os.Stat(a.dir("pictures", h+".png")); err == nil {
		f.MixedStamp = st.ModTime().UnixNano()
	}
	if err := a.putFit(f); err != nil {
		return Fit{}, false, err
	}
	return f, false, nil
}

// stillMixed: the fit's pictures are still the copied ones, not a real snap.
func (a *App) stillMixed(f Fit) bool {
	if !f.Mixed {
		return false
	}
	st, err := os.Stat(a.dir("pictures", f.Hash+".png"))
	return err != nil || f.MixedStamp == 0 || st.ModTime().UnixNano() == f.MixedStamp
}
