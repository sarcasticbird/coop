package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDurableUATBudgetRefusesDuplicatesAndExhaustion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turns.json")
	if err := reserve(path, "session/message-one", 2); err != nil {
		t.Fatal(err)
	}
	if err := reserve(path, "session/message-one", 2); err == nil {
		t.Fatal("duplicate submitted")
	}
	if err := reserve(path, "session/message-two", 2); err != nil {
		t.Fatal(err)
	}
	if err := reserve(path, "session/message-three", 2); err == nil {
		t.Fatal("budget exceeded")
	}
}

func TestUATBudgetSerializesConcurrentReservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turns.json")
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if reserve(path, fmt.Sprint(i), 4) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 4 {
		t.Fatalf("accepted %d turns", accepted.Load())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var turns []string
	if json.Unmarshal(raw, &turns) != nil || len(turns) != 4 {
		t.Fatal("lost durable budget state")
	}
}

func TestUATBudgetHoldsCorruptJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turns.json")
	if err := os.WriteFile(path, []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reserve(path, "new-intent", 2); err == nil {
		t.Fatal("corrupt budget silently reset")
	}
}
