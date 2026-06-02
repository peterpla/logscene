package main

// storage_test.go tests the Storage interface implementations.
// By default tests use MemStorage; set TEST_STORAGE=local to exercise
// LocalStorage against a real temporary directory.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorage_WriteAndRead(t *testing.T) {
	store := testStorage(t)
	ctx := context.Background()

	key := tempKey(t, "hello.jpg")
	content := "fake image bytes"

	if err := store.Write(ctx, key, strings.NewReader(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rc, err := store.Read(ctx, key)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != content {
		t.Errorf("Read: want %q, got %q", content, string(got))
	}
}

func TestStorage_ReadMissing(t *testing.T) {
	store := testStorage(t)
	_, err := store.Read(context.Background(), tempKey(t, "missing.jpg"))
	if err == nil {
		t.Error("expected error reading missing key, got nil")
	}
}

func TestStorage_List(t *testing.T) {
	store := testStorage(t)
	ctx := context.Background()

	prefix := tempKey(t, "cam/My Cam ")
	keys := []string{
		prefix + "20260601060000.jpg",
		prefix + "20260601120000.jpg",
		prefix + "20260601180000.jpg",
	}
	for _, k := range keys {
		if err := store.Write(ctx, k, strings.NewReader("data")); err != nil {
			t.Fatalf("Write %s: %v", k, err)
		}
	}

	listed, err := store.List(ctx, prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != len(keys) {
		t.Fatalf("List: want %d keys, got %d: %v", len(keys), len(listed), listed)
	}
	// Verify lexicographic order.
	for i := 1; i < len(listed); i++ {
		if listed[i] < listed[i-1] {
			t.Errorf("List not sorted: listed[%d]=%s before listed[%d]=%s",
				i, listed[i], i-1, listed[i-1])
		}
	}
}

func TestStorage_ListEmpty(t *testing.T) {
	store := testStorage(t)
	listed, err := store.List(context.Background(), tempKey(t, "no-such-prefix/"))
	if err != nil && !isNotFoundError(err) {
		t.Fatalf("List unexpected error: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("expected empty list, got %v", listed)
	}
}

func TestStorage_OverwriteKey(t *testing.T) {
	store := testStorage(t)
	ctx := context.Background()
	key := tempKey(t, "img.jpg")

	store.Write(ctx, key, strings.NewReader("first"))
	store.Write(ctx, key, strings.NewReader("second"))

	rc, _ := store.Read(ctx, key)
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "second" {
		t.Errorf("overwrite: want %q, got %q", "second", string(got))
	}
}

// ---------------------------------------------------------------------------
// MemStorage-specific tests
// ---------------------------------------------------------------------------

func TestMemStorage_Keys(t *testing.T) {
	store := NewMemStorage()
	ctx := context.Background()
	store.Write(ctx, "a/1.jpg", strings.NewReader("x"))
	store.Write(ctx, "a/2.jpg", strings.NewReader("y"))
	store.Write(ctx, "b/3.jpg", strings.NewReader("z"))

	keys := store.Keys()
	if len(keys) != 3 {
		t.Errorf("want 3 keys, got %d: %v", len(keys), keys)
	}
}

// ---------------------------------------------------------------------------
// LocalStorage — direct tests (not gated by TEST_STORAGE env var)
// ---------------------------------------------------------------------------

// failReader always returns an error, used to exercise the io.Copy failure
// path inside LocalStorage.Write.
type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("intentional read error") }

func TestLocalStorage_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalStorage()
	ctx := context.Background()

	key := filepath.Join(dir, "cam", "frame.jpg")
	content := "fake-jpeg-bytes"

	if err := store.Write(ctx, key, strings.NewReader(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	rc, err := store.Read(ctx, key)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != content {
		t.Errorf("Read: want %q, got %q", content, string(got))
	}
}

func TestLocalStorage_ReadMissing(t *testing.T) {
	store := NewLocalStorage()
	_, err := store.Read(context.Background(), filepath.Join(t.TempDir(), "no-such-file.jpg"))
	if err == nil {
		t.Error("expected error reading missing file, got nil")
	}
}

func TestLocalStorage_Write_mkdirError(t *testing.T) {
	store := NewLocalStorage()
	tmp := t.TempDir()

	// Place a regular file where a directory is expected; MkdirAll must fail.
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	key := filepath.Join(blocker, "nested", "frame.jpg")
	if err := store.Write(context.Background(), key, strings.NewReader("data")); err == nil {
		t.Error("expected error when MkdirAll fails, got nil")
	}
}

func TestLocalStorage_Write_copyError(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalStorage()
	key := filepath.Join(dir, "frame.jpg")

	// Reader fails immediately; Write must propagate the error.
	// (On Windows, os.Remove of an open file is a no-op, so we don't assert
	// on file cleanup here — that behaviour is OS-specific.)
	if err := store.Write(context.Background(), key, failReader{}); err == nil {
		t.Error("expected error when reader fails, got nil")
	}
}

func TestLocalStorage_List_missingDir(t *testing.T) {
	store := NewLocalStorage()
	prefix := filepath.Join(t.TempDir(), "nonexistent-dir", "prefix")
	_, err := store.List(context.Background(), prefix)
	if err == nil {
		t.Error("expected error when directory does not exist, got nil")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// tempKey builds a storage key rooted in t.TempDir() for local storage tests,
// or a simple in-memory key for MemStorage tests.
func tempKey(t *testing.T, name string) string {
	t.Helper()
	if strings.EqualFold(os.Getenv("TEST_STORAGE"), "local") {
		return t.TempDir() + "/" + name
	}
	return t.Name() + "/" + name
}

// isNotFoundError returns true for errors that indicate a missing key/dir,
// which LocalStorage.List may return for a non-existent prefix directory.
func isNotFoundError(err error) bool {
	return strings.Contains(err.Error(), "no such file") ||
		strings.Contains(err.Error(), "cannot find")
}
