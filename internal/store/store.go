// Package store is a small JSON-file key/value store for service state
// (settings, the local user, sub-app flags, view history...).
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Store struct {
	path string
	mu   sync.Mutex
	data map[string]json.RawMessage
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			// Keep the corrupt file for inspection and start fresh.
			os.Rename(path, path+".corrupt")
			s.data = map[string]json.RawMessage{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, os.MkdirAll(filepath.Dir(path), 0o755)
}

// Get decodes key into v and reports whether it existed.
func (s *Store) Get(key string, v any) bool {
	s.mu.Lock()
	raw, ok := s.data[key]
	s.mu.Unlock()
	if !ok {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

func (s *Store) Set(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.data[key] = b
	s.mu.Unlock()
	return s.flush()
}

func (s *Store) Delete(key string) error {
	s.mu.Lock()
	delete(s.data, key)
	s.mu.Unlock()
	return s.flush()
}

func (s *Store) flush() error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s.data, "", " ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
