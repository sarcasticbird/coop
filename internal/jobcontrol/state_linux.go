//go:build linux

package jobcontrol

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func processStopped(pid int) (bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 || end+2 >= len(raw) {
		return false, errors.New("malformed process stat")
	}
	state := raw[end+2]
	return state == 'T' || state == 't', nil
}
