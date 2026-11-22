package db

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestAuditConcurrentSQLiteWrites(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	if _, err := InitDB(); err != nil {
		t.Fatal(err)
	}

	const workers = 32
	const perWorker = 50
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := LogMessageWithTier(fmt.Sprintf("c-%d", id), "u", "user", "payload", "m", 1, "free", "a"); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	elapsed := time.Since(start)
	failures := 0
	for err := range errs {
		failures++
		t.Logf("write error: %v", err)
	}
	stats, err := GetSystemStats()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d writes in %s (%.0f writes/s), failures=%d, total_messages=%v",
		workers*perWorker, elapsed, float64(workers*perWorker)/elapsed.Seconds(), failures, stats["total_messages"])
	if failures > 0 {
		t.Fatalf("expected no SQLite write failures, got %d", failures)
	}
}
