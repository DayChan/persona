//go:build !unix

package hub

// listenHelpers has nothing to do where there is no socket activation (listen_unix_test.go).
func listenHelpers() {}
