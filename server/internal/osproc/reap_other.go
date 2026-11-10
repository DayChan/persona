//go:build !linux && !darwin

package osproc

// ReapInherited does nothing here: on Windows a program does not replace itself in place, and on the other systems
// nothing lists this process's children yet.
func ReapInherited() {}
