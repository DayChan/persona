//go:build linux

package rpcio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOutstandingQueryPipeAndClosure(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	query := OutstandingQuery(writer)
	if query == nil {
		t.Fatal("pipe consumption was not measurable")
	}
	if n, ok := query(); !ok || n != 0 {
		t.Fatalf("empty pipe: outstanding=%d available=%v", n, ok)
	}
	if _, err := writer.Write([]byte("progress")); err != nil {
		t.Fatal(err)
	}
	if n, ok := query(); !ok || n != 8 {
		t.Fatalf("written pipe: outstanding=%d available=%v", n, ok)
	}
	if n, err := reader.Read(make([]byte, 3)); err != nil || n != 3 {
		t.Fatalf("partial read: bytes=%d error=%v", n, err)
	}
	if n, ok := query(); !ok || n != 5 {
		t.Fatalf("partially read pipe: outstanding=%d available=%v", n, ok)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := query(); ok {
		t.Fatal("closed descriptor produced a reliable observation")
	}
	if OutstandingQuery(writer) != nil {
		t.Fatal("closed descriptor acquired a consumption signal")
	}
}

func TestOutstandingQueryRejectsRegularFile(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "regular"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if OutstandingQuery(file) != nil {
		t.Fatal("regular file was treated as receiving-application consumption")
	}
}
