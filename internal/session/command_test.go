package session

import (
	"reflect"
	"testing"
)

func TestGuestCommandDoesNotResolveHostCwd(t *testing.T) {
	s := &Session{Project: "/owned/project"}
	cwd, args, e := s.GuestCommand("/guest-only", []string{"printf", "two words", "$HOME;nope"})
	if e != nil || cwd != "/guest-only" || !reflect.DeepEqual(args[len(args)-3:], []string{"printf", "two words", "$HOME;nope"}) {
		t.Fatalf("guest command changed: %s %v %v", cwd, args, e)
	}
	if _, _, e = s.GuestCommand("relative", []string{"true"}); e == nil {
		t.Fatal("relative cwd accepted")
	}
}
