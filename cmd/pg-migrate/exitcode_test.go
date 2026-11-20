package main

import (
	"errors"
	"fmt"
	"testing"

	migrate "github.com/eidon-go/pg-migrate"
)

func TestExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		name string
		want int
	}{
		{name: "success", err: nil, want: 0},
		{name: "generic failure", err: errors.New("connection refused"), want: exitFailure},
		{name: "failed migrations", err: migrate.ErrFailedMigrations, want: exitNeedsHuman},
		{name: "unrecorded", err: migrate.ErrUnrecorded, want: exitNeedsHuman},
		{name: "lock timeout", err: migrate.ErrLockTimeout, want: exitLockTimeout},
		{name: "divergence", err: migrate.ErrDivergence, want: exitRefused},
		{name: "interleaved", err: migrate.ErrInterleaved, want: exitRefused},
		{name: "empty source", err: migrate.ErrEmptySource, want: exitRefused},
		{name: "irreversible", err: migrate.ErrIrreversible, want: exitRefused},
		{name: "already recorded", err: migrate.ErrAlreadyRecorded, want: exitRefused},
		{name: "baseline not found", err: migrate.ErrBaselineNotFound, want: exitRefused},
		{
			// run wraps what the command returns, and the hint wraps it again.
			name: "wrapped twice",
			err:  fmt.Errorf("execute command: %w", fmt.Errorf("%w (hint)", migrate.ErrFailedMigrations)),
			want: exitNeedsHuman,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := exitCode(tt.err); got != tt.want {
				t.Errorf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}
