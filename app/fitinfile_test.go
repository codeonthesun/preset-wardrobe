package main

import "testing"

func TestFitInFileReSkateHold(t *testing.T) {
	itemMu.Lock()
	itemKnown["Own_Missing_Trucks"] = true
	itemMu.Unlock()
	defer func() {
		itemMu.Lock()
		delete(itemKnown, "Own_Missing_Trucks")
		itemMu.Unlock()
	}()
	fit := `[{"items":[{"asset":"Own_Top","parameter_bits":[1,2],"slot":2890690067}]},{"items":[{"asset":"Own_Truck_Gen_Default_00001","parameter_bits":[],"slot":182294718}]}]`
	// the game saved the held item back where the fit has the default
	file := `[{"items":[{"asset":"Own_Top","parameter_bits":[1,2],"slot":2890690067}]},{"items":[{"asset":"Own_Missing_Trucks","parameter_bits":[],"slot":182294718}]}]`
	if same, held := fitInFile(file, fit, false); !same || held != 1 {
		t.Fatalf("ReSkate's held item: same %v held %d, want true 1", same, held)
	}
	// a real edit in the game still counts as different
	edited := `[{"items":[{"asset":"Own_Other_Top","parameter_bits":[1,2],"slot":2890690067}]},{"items":[{"asset":"Own_Truck_Gen_Default_00001","parameter_bits":[],"slot":182294718}]}]`
	if same, _ := fitInFile(edited, fit, false); same {
		t.Fatal("an edited top counted as the same fit")
	}
	// settings count
	recolored := `[{"items":[{"asset":"Own_Top","parameter_bits":[1,3],"slot":2890690067}]},{"items":[{"asset":"Own_Truck_Gen_Default_00001","parameter_bits":[],"slot":182294718}]}]`
	if same, _ := fitInFile(recolored, fit, false); same {
		t.Fatal("a recolored top counted as the same fit")
	}
}
