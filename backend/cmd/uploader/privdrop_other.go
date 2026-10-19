//go:build !linux

package main

// dropPrivileges is a no-op on non-Linux platforms (local dev builds); the
// privilege drop only matters in the Linux container runtime.
func dropPrivileges(string) error { return nil }
