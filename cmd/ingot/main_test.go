package main

import (
	"os"
	"testing"
)

func TestProcessSignalsIncludeInterrupt(t *testing.T) {
	for _, candidate := range processSignals() {
		if candidate == os.Interrupt {
			return
		}
	}
	t.Fatal("process signals do not include os.Interrupt")
}
