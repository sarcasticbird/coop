package main

import (
	"io"
	"testing"
)

func TestNativeCommandRequiresExplicitSavedJobAndPrivateInput(t *testing.T) {
	for _, args := range [][]string{nil, {"action"}, {"action", "--config", "/private/config.json"}, {"review"}, {"action", "--config", "/private/config.json", "--job", "job", "--input", "/missing/action.json"}} {
		if err := nativeCommand(args, io.Discard); err == nil {
			t.Fatalf("invalid native command accepted %v", args)
		}
	}
}
