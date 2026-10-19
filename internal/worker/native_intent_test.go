package worker

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNativeRetainedIntentIsPrivateAndBoundToOriginalJournal(t *testing.T) {
	cfg, _, _, _, x := nativeTerminalWaitFixture(t)
	in := NativeAction{Operation: "create-task", RequestID: "original", Repository: "code/repo", Title: "task", Body: "private scope", BaseBranch: "main"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	record := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: in.RequestID, InputSHA: BodyHash(string(raw)), Operation: in.Operation, State: "prepared"}
	if err = saveNativeAction(cfg, record); err != nil {
		t.Fatal(err)
	}
	if err = RetainNativeAction(cfg, x.Job.ID, in); err != nil {
		t.Fatal("original intent could not be retained", err)
	}
	saved, err := loadNativeActionIntent(cfg, x, record)
	if err != nil || saved.Body != in.Body || saved.RequestID != in.RequestID {
		t.Fatal("original intent lost", err)
	}
	info, err := os.Stat(nativeActionIntentPath(cfg, record.JobID, record.RequestID))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("intent is not private", err)
	}
	in.Body = "replacement scope"
	if err = RetainNativeAction(cfg, x.Job.ID, in); err == nil {
		t.Fatal("changed input replaced the original")
	}
	saved, err = loadNativeActionIntent(cfg, x, record)
	if err != nil || saved.Body == in.Body {
		t.Fatal("rejected input changed private intent", err)
	}
}
