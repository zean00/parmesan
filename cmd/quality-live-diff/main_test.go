package main

import "testing"

func TestDiffIDs(t *testing.T) {
	got := diffIDs([]string{"a", "b", "c"}, []string{"b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("diffIDs = %#v", got)
	}
}
