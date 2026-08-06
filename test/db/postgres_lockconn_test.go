//go:build integration

package db_test

import (
	"testing"

	"github.com/eidon-go/pg-migrate/internal/dbconn"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

func TestPostgresLocker_InternalState(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	pg, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("failed to create postgres instance: %v", err)
	}
	defer pg.Close()

	if pg.ExportLockConn() != nil {
		t.Fatal("expected lockConn to be nil initially")
	}

	// Acquire lock
	err = pg.AcquireLock(t.Context())
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	if pg.ExportLockConn() == nil {
		t.Fatal("expected lockConn to be initialized after AcquireLock")
	}

	// Release lock
	err = pg.ReleaseLock(t.Context())
	if err != nil {
		t.Fatalf("failed to release lock: %v", err)
	}

	if pg.ExportLockConn() != nil {
		t.Fatal("expected lockConn to be nil after ReleaseLock")
	}
}
