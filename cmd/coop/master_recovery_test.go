package main

import (
	"path/filepath"
	"testing"
)

func TestMasterClientDefaultPath(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := defaultMasterClientPath(); got != "/home/operator/.config/coop/master-client.toml" {
		t.Fatal(got)
	}
	t.Setenv("XDG_CONFIG_HOME", "/private/config")
	if got := defaultMasterClientPath(); got != filepath.Join("/private/config", "coop", "master-client.toml") {
		t.Fatal(got)
	}
}
