//go:build integration

package store_test

import "testing"

// SetSettings is all-or-nothing: one rejected value rolls back the others.
func TestSetSettingsIsAtomic(t *testing.T) {
	e := newEnv(t)
	if err := e.st.SetSettings(e.ctx, map[string]string{"a": "1", "b": "2"}); err != nil {
		t.Fatal(err)
	}
	// Postgres rejects a NUL byte in TEXT, whatever order the map is written in.
	err := e.st.SetSettings(e.ctx, map[string]string{"a": "changed", "b": "changed", "c": "new", "bad": "nul\x00byte"})
	if err == nil {
		t.Fatal("SetSettings with a NUL byte: want error, got nil")
	}
	got, err := e.st.GetAllSettings(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["a"] != "1" || got["b"] != "2" {
		t.Fatalf("a/b = %q/%q after a failed batch, want the old values", got["a"], got["b"])
	}
	if _, ok := got["c"]; ok {
		t.Fatal("a failed batch inserted a new key")
	}
}
