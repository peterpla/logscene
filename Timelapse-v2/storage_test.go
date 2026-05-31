package main

// storage_test.go tests the Storage interface implementations.
// By default tests use MemStorage; set TEST_STORAGE=local to exercise
// LocalStorage against a real temporary directory.

import (
	"context"
	"io"
	"os"
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
