//go:build !windows

package main

import (
	"context"
	"syscall"
	"testing"
	"time"
)

func TestNewSignalContextCancelsOnSignal(t *testing.T) {
	t.Parallel()

	ctx, stop := newSignalContext()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to self: %v", err)
	}

	select {
	case <-ctx.Done():
		if ctx.Err() != context.Canceled {
			t.Errorf("ctx.Err() = %v, want context.Canceled", ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("context was not cancelled after SIGINT")
	}
}

func TestNewSignalContextStopCancels(t *testing.T) {
	t.Parallel()

	ctx, stop := newSignalContext()
	stop()

	select {
	case <-ctx.Done():
	default:
		t.Fatal("stop() did not cancel the context")
	}
}
