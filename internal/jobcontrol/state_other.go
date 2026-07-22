//go:build !darwin && !linux

package jobcontrol

func processStopped(int) (bool, error) { return false, nil }
