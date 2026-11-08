//go:build !unix

package osproc

// SealInherited does nothing outside Unix: on Windows Go starts a child with only the handles it lists for it, and
// there is no socket activation.
func SealInherited() {}
