package tools

import (
	"os"
	"sync"
	"testing"
	"time"
)

// Tests answer questions instantly; a person doesn't. Each look at the
// clock moves it on 2 s, so test answers count as a person's (the check
// itself is tested with its own clock in confirm_fast_test.go).
func TestMain(m *testing.M) {
	var mu sync.Mutex
	t := time.Now()
	confirmNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(2 * time.Second)
		return t
	}
	os.Exit(m.Run())
}
