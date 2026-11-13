package main

import "testing"

func TestEnhancerHas(t *testing.T) {
	e := newEnhancer(1 << 20)
	if e.has("x") {
		t.Fatal("empty cache says it has x")
	}
	e.mu.Lock()
	e.items["x"] = e.order.PushFront(&enhItem{"x", []byte("1")})
	e.mu.Unlock()
	if !e.has("x") {
		t.Fatal("has() missed a kept picture")
	}
}
