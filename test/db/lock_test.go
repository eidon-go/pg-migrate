//go:build integration

package db_test

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/eidon-go/pg-migrate/internal/db"
	"github.com/eidon-go/pg-migrate/internal/dbconn"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

// advisoryLockCount counts advisory locks held in the current database only.
// pg_locks is cluster-wide and these tests run in parallel against sibling
// databases, so an unfiltered count sees other tests' locks.
func advisoryLockCount(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()

	const query = `
		SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory'
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`

	var count int
	if err := sqlDB.QueryRowContext(t.Context(), query).Scan(&count); err != nil {
		t.Fatalf("Failed to count advisory locks: %v", err)
	}

	return count
}

func TestPostgresLocker_AcquireAndRelease(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	pg, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Acquire lock
	err = pg.AcquireLock(t.Context())
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Release lock
	err = pg.ReleaseLock(t.Context())
	if err != nil {
		t.Fatalf("Failed to release lock: %v", err)
	}
}

func TestPostgresLocker_AcquireTwice(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	pg, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Acquire lock first time
	err = pg.AcquireLock(t.Context())
	if err != nil {
		t.Fatalf("Failed to acquire lock first time: %v", err)
	}

	// Try to acquire lock second time - should fail
	err = pg.AcquireLock(t.Context())
	if err == nil {
		t.Fatal("Expected error when acquiring lock twice, got nil")
	}

	if err.Error() != "lock already acquired" {
		t.Errorf("Expected 'lock already acquired' error, got: %v", err)
	}

	// Cleanup
	_ = pg.ReleaseLock(t.Context())
}

func TestPostgresLocker_ReleaseTwice(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	pg, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Acquire lock
	err = pg.AcquireLock(t.Context())
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Release lock first time
	err = pg.ReleaseLock(t.Context())
	if err != nil {
		t.Fatalf("Failed to release lock first time: %v", err)
	}

	// Release lock second time - should fail
	err = pg.ReleaseLock(t.Context())
	if err == nil {
		t.Fatal("Expected error when releasing lock twice, got nil")
	}

	if err.Error() != "lock not acquired" {
		t.Errorf("Expected 'lock not acquired' error, got: %v", err)
	}
}

func TestPostgresLocker_Timeout(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)
	cfg.LockTimeout = 60 * time.Second

	pg1, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	cfg.LockTimeout = 2 * time.Second

	pg2, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// First locker acquires lock
	err = pg1.AcquireLock(t.Context())
	if err != nil {
		t.Fatalf("Failed to acquire lock with locker1: %v", err)
	}

	defer func() {
		_ = pg1.ReleaseLock(t.Context())
	}()

	// Second locker tries to acquire same lock - should timeout
	start := time.Now()
	err = pg2.AcquireLock(t.Context())
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected timeout error, got nil")
	}

	if !errors.Is(err, db.ErrLockTimeout) {
		t.Errorf("Expected errors.Is(err, ErrLockTimeout), got: %v", err)
	}

	// Verify that it took approximately the timeout duration
	if duration < 2*time.Second || duration > 3*time.Second {
		t.Errorf("Expected timeout around 2.0 seconds, got: %v", duration)
	}

	// The waiter must not have acquired the lock: Postgres aborted the statement
	// server-side, so pg1 still holds it and pg2's connection went back to the
	// pool clean.
	if got := advisoryLockCount(t, pg1.GetDB()); got != 1 {
		t.Errorf("Expected exactly 1 advisory lock (pg1's), got %d", got)
	}
}

func TestPostgresLocker_ConcurrentAcquire(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)
	cfg.LockTimeout = 3 * time.Second

	const numGoroutines = 5

	var wg sync.WaitGroup

	successCount := make(chan int, numGoroutines)
	failureCount := make(chan int, numGoroutines)

	// Launch multiple goroutines trying to acquire the same lock
	for i := range numGoroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			pg, err := dbconn.NewPostgres(cfg)
			if err != nil {
				t.Errorf("Failed to create postgres instance: %v", err)

				return
			}

			err = pg.AcquireLock(t.Context())
			if err != nil {
				t.Logf("Goroutine %d failed to acquire lock: %v", id, err)

				failureCount <- 1

				return
			}

			// Hold lock briefly
			time.Sleep(100 * time.Millisecond)

			err = pg.ReleaseLock(t.Context())
			if err != nil {
				t.Logf("Goroutine %d failed to release lock: %v", id, err)
			}

			successCount <- 1
		}(i)
	}

	wg.Wait()
	close(successCount)
	close(failureCount)

	// Count successes and failures
	successes := 0
	for range successCount {
		successes++
	}

	failures := 0
	for range failureCount {
		failures++
	}

	t.Logf("Successes: %d, Failures: %d", successes, failures)

	// At least one should succeed, others should fail or succeed sequentially
	if successes == 0 {
		t.Fatal("Expected at least one goroutine to succeed acquiring lock")
	}

	// With timeout of 3 seconds and 100ms hold time, multiple goroutines should succeed
	// since they release locks quickly
	if successes+failures != numGoroutines {
		t.Errorf("Expected %d total attempts, got %d", numGoroutines, successes+failures)
	}
}

// TestPostgresLocker_PoolTooSmall proves AcquireLock fails fast (rather than
// hanging forever) when the pool can't spare a second connection for the
// executor once the lock pins one for itself.
func TestPostgresLocker_PoolTooSmall(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	pg, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to create postgres instance: %v", err)
	}
	defer pg.Close()

	pg.GetDB().SetMaxOpenConns(1)

	done := make(chan error, 1)
	go func() {
		done <- pg.AcquireLock(t.Context())
	}()

	select {
	case err := <-done:
		if !errors.Is(err, db.ErrPoolTooSmall) {
			t.Fatalf("expected errors.Is(err, db.ErrPoolTooSmall)=true, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AcquireLock did not return promptly with SetMaxOpenConns(1); it appears to hang instead of failing fast")
	}
}

func TestPostgresLocker_DifferentLockIDs(t *testing.T) {
	t.Parallel()

	const (
		lockID1 int64 = 12345
		lockID2 int64 = 67890
	)

	cfg := dbtest.SetupTestPostgres(t)
	cfg.LockID = lockID1

	pg1, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	cfg.LockID = lockID2

	pg2, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Both should be able to acquire their locks simultaneously
	err = pg1.AcquireLock(t.Context())
	if err != nil {
		t.Fatalf("Failed to acquire lock1: %v", err)
	}
	defer func() {
		_ = pg1.ReleaseLock(t.Context())
	}()

	err = pg2.AcquireLock(t.Context())
	if err != nil {
		t.Fatalf("Failed to acquire lock2: %v", err)
	}
	defer func() {
		_ = pg2.ReleaseLock(t.Context())
	}()

	// Both locks should be held independently
	t.Log("Both locks acquired successfully with different lock IDs")
}

// The lock connection goes back to the pool on release, and it must not carry
// the transaction-scoped lock_timeout with it — otherwise the next query to get
// that connection would silently inherit a lock timeout it never asked for.
func TestPostgresLocker_DoesNotLeakLockTimeoutToPool(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)
	cfg.LockTimeout = 1234 * time.Millisecond

	pg, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to create postgres: %v", err)
	}
	defer pg.Close()

	// Pin the pool to a single reusable connection so the query below is
	// guaranteed to land on the one the lock used.
	pg.GetDB().SetMaxIdleConns(1)

	if err := pg.AcquireLock(t.Context()); err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	if err := pg.ReleaseLock(t.Context()); err != nil {
		t.Fatalf("Failed to release lock: %v", err)
	}

	var lockTimeout string
	if err := pg.GetDB().QueryRowContext(t.Context(), "SHOW lock_timeout").Scan(&lockTimeout); err != nil {
		t.Fatalf("Failed to read lock_timeout: %v", err)
	}

	if lockTimeout != "0" {
		t.Errorf("lock_timeout on the pooled connection = %q, want %q (the default)", lockTimeout, "0")
	}
}

// Releasing must actually release. (*sql.Conn).Close only returns the connection
// to the pool, so a lock left behind would be stranded on a live session with
// nothing tracking it and no way for any process to migrate again.
func TestPostgresLocker_ReleaseLeavesNoLockBehind(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	pg, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to create postgres: %v", err)
	}
	defer pg.Close()

	observer, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to create observer: %v", err)
	}
	defer observer.Close()

	for i := range 3 {
		if err := pg.AcquireLock(t.Context()); err != nil {
			t.Fatalf("iteration %d: failed to acquire lock: %v", i, err)
		}

		if err := pg.ReleaseLock(t.Context()); err != nil {
			t.Fatalf("iteration %d: failed to release lock: %v", i, err)
		}

		if got := advisoryLockCount(t, observer.GetDB()); got != 0 {
			t.Fatalf("iteration %d: %d advisory lock(s) still held after release", i, got)
		}
	}
}

// ReleaseLock on a lock nobody holds must report it rather than claim success.
func TestPostgresLocker_ReleaseReportsLockNotHeld(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)
	// Pin the ID explicitly: leaving it zero means the default is substituted
	// internally, and the out-of-band unlock below needs the real value.
	cfg.LockID = 424242

	pg, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to create postgres: %v", err)
	}
	defer pg.Close()

	if acquireErr := pg.AcquireLock(t.Context()); acquireErr != nil {
		t.Fatalf("Failed to acquire lock: %v", acquireErr)
	}

	// Release it out of band, on the very session that holds it.
	var unlocked bool
	if scanErr := pg.ExportLockConn().QueryRowContext(t.Context(),
		"SELECT pg_advisory_unlock($1)", cfg.LockID,
	).Scan(&unlocked); scanErr != nil {
		t.Fatalf("Failed to unlock out of band: %v", scanErr)
	}

	if !unlocked {
		t.Fatal("out-of-band unlock reported the lock was not held")
	}

	if releaseErr := pg.ReleaseLock(t.Context()); !errors.Is(releaseErr, db.ErrLockNotHeld) {
		t.Errorf("Expected errors.Is(err, ErrLockNotHeld), got: %v", releaseErr)
	}

	// State must still be reset, so a later acquire works.
	if acquireErr := pg.AcquireLock(t.Context()); acquireErr != nil {
		t.Errorf("AcquireLock after a not-held release failed: %v", acquireErr)
	} else {
		_ = pg.ReleaseLock(t.Context())
	}
}
