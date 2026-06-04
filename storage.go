// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// storage.go defines the Storage interface and its implementations.
//
// Storage abstracts where captured images are written and read from.
// The same interface works for local disk, cloud object storage, or
// an in-memory store used in tests.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Storage persists and retrieves captured images.
type Storage interface {
	// Write stores the content of r under the given key.
	// For local storage the key is a file path; for cloud storage it is an object key.
	Write(ctx context.Context, key string, r io.Reader) error

	// Read opens the object at key for reading. The caller must close it.
	Read(ctx context.Context, key string) (io.ReadCloser, error)

	// List returns all keys whose names begin with prefix, in lexicographic order.
	List(ctx context.Context, prefix string) ([]string, error)
}

// ---------------------------------------------------------------------------
// LocalStorage — writes to the local filesystem
// ---------------------------------------------------------------------------

// LocalStorage implements Storage using the local filesystem.
// Keys are treated as file paths; the directory portion is created as needed.
type LocalStorage struct{}

// NewLocalStorage creates a LocalStorage instance.
func NewLocalStorage() *LocalStorage { return &LocalStorage{} }

func (s *LocalStorage) Write(_ context.Context, key string, r io.Reader) error {
	dir := filepath.ToSlash(filepath.Dir(key))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("LocalStorage.Write: mkdir %s: %w", dir, err)
	}
	f, err := os.Create(key)
	if err != nil {
		return fmt.Errorf("LocalStorage.Write: create %s: %w", key, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		os.Remove(key)
		return fmt.Errorf("LocalStorage.Write: copy to %s: %w", key, err)
	}
	return nil
}

func (s *LocalStorage) Read(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(key)
	if err != nil {
		return nil, fmt.Errorf("LocalStorage.Read: %w", err)
	}
	return f, nil
}

func (s *LocalStorage) List(_ context.Context, prefix string) ([]string, error) {
	dir := filepath.ToSlash(filepath.Dir(prefix))
	base := filepath.Base(prefix)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("LocalStorage.List: readdir %s: %w", dir, err)
	}

	var keys []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), base) {
			keys = append(keys, dir+"/"+e.Name())
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// ---------------------------------------------------------------------------
// MemStorage — in-memory store used in tests
// ---------------------------------------------------------------------------

// MemStorage implements Storage entirely in memory.
// It is safe for concurrent use.
type MemStorage struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemStorage creates an empty MemStorage.
func NewMemStorage() *MemStorage {
	return &MemStorage{data: make(map[string][]byte)}
}

func (s *MemStorage) Write(_ context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("MemStorage.Write: %w", err)
	}
	s.mu.Lock()
	s.data[key] = b
	s.mu.Unlock()
	return nil
}

func (s *MemStorage) Read(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.RLock()
	b, ok := s.data[key]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("MemStorage.Read: key %q not found", key)
	}
	return io.NopCloser(strings.NewReader(string(b))), nil
}

func (s *MemStorage) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var keys []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Keys returns all stored keys (useful in tests for assertions).
func (s *MemStorage) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
