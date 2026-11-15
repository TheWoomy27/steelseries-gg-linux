package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// HTTP handlers save state at the same time. Every save succeeds, and
// the file always holds a complete, current copy of the state: a torn
// file would be set aside as corrupt at the next start, losing it all.
func TestConcurrentSetsKeepTheFileWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const writers, rounds = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// Values of different sizes, so overlapping writes of one
			// temporary file leave a mix of both.
			big := strings.Repeat("x", 2000*(w+1))
			for i := 0; i < rounds; i++ {
				if err := s.Set(fmt.Sprintf("key%d", w), map[string]any{"round": i, "pad": big}); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Set: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]struct {
		Round int `json:"round"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatalf("the saved file is not JSON: %v", err)
	}
	for w := 0; w < writers; w++ {
		if got := data[fmt.Sprintf("key%d", w)].Round; got != rounds-1 {
			t.Errorf("key%d: saved round %d, want %d", w, got, rounds-1)
		}
	}
}
