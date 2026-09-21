package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestProcessSignalsIncludeInterrupt(t *testing.T) {
	for _, candidate := range processSignals() {
		if candidate == os.Interrupt {
			return
		}
	}
	t.Fatal("process signals do not include os.Interrupt")
}

func TestProcessSignalsRestoreWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	restoreProcessSignalsOnCancel(ctx, func() { close(stopped) })
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("signal handling was not restored after cancellation")
	}
}
