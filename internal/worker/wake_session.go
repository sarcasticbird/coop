package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func WakeBindingKey(c Config, r WakeRequest) string {
	return BodyHash(c.Name + ":wake:" + r.Source.DiscussionID + ":" + r.Source.RootID)
}
func PrepareWakeJob(c Config, r WakeRequest, b WakeBinding) (Job, error) {
	if r.Target != c.Name || r.Source.DiscussionID == "" || r.Source.RootID == "" {
		return Job{}, errors.New("invalid wake binding")
	}
	key := WakeBindingKey(c, r)
	root := filepath.Join(c.WorkRoot, "wake-"+key[:32])
	dir := filepath.Join(root, "conversation")
	if b.Directory != "" && (b.Directory != dir || b.DiscussionID != "" && b.DiscussionID != r.Source.DiscussionID || b.ThreadID != "" && b.ThreadID != r.Source.RootID) {
		return Job{}, errors.New("foreign wake binding")
	}
	if err := ensurePrivateDir(c.WorkRoot, true); err != nil {
		return Job{}, err
	}
	if err := safePath(root); err != nil {
		return Job{}, err
	}
	owner := WakeBinding{DiscussionID: r.Source.DiscussionID, ThreadID: r.Source.RootID, Directory: dir}
	marker := filepath.Join(root, "owner.json")
	if _, err := os.Lstat(root); err == nil {
		raw, err := readPrivate(marker, MaxConfig)
		if err != nil {
			return Job{}, errors.New("missing wake directory ownership")
		}
		var saved WakeBinding
		if strictJSON(raw, &saved) != nil || saved != owner {
			return Job{}, errors.New("foreign wake directory ownership")
		}
	} else if os.IsNotExist(err) {
		if err := ensurePrivateDir(root, true); err != nil {
			return Job{}, err
		}
		raw, _ := json.Marshal(owner)
		if err := atomicPrivate(marker, raw); err != nil {
			return Job{}, err
		}
	} else {
		return Job{}, err
	}
	if err := ensurePrivateDir(dir, true); err != nil {
		return Job{}, err
	}
	return Job{ID: r.ID, Kind: "wake", GoalURL: r.Source.DiscussionURL, TaskURL: r.Source.RootID, Checkout: dir, SessionID: b.SessionID}, nil
}
