package agent

import (
	"reflect"
	"testing"
)

func TestGuestArgvPreservesLiteralArguments(t *testing.T) {
	s := &Session{}
	args := []string{"printf", "two words", "$HOME;nope"}
	got := s.GuestArgv(args)
	want := []string{"/usr/local/libexec/coop-env", "printf", "two words", "$HOME;nope"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}

}
