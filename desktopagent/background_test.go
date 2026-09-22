// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"testing"
)

func TestBackgroundRejectsForeignProfileBeforeOSCommand(t *testing.T) {
	foreign := Paths{StateDirectory: t.TempDir()}
	if _, err := ReadBackgroundStatus(context.Background(), foreign); err == nil {
		t.Fatal("foreign-profile registration read accepted")
	}
	if _, err := EnableBackground(context.Background(), foreign); err == nil {
		t.Fatal("foreign-profile activation accepted")
	}
}

func TestBackgroundOutputIsBounded(t *testing.T) {
	output := &boundedBackgroundOutput{}
	if _, err := output.Write(make([]byte, 32<<10)); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("x")); err == nil {
		t.Fatal("unbounded command output accepted")
	}
}
