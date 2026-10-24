package worker

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func diagnosticTitle(title string) string {
	// Collapse untrusted source whitespace and remove terminal/control characters.
	title = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	for len(title) > c.MaxDiagnosticTitleBytes {
		_, size := utf8.DecodeLastRuneInString(title)
		title = title[:len(title)-size]
	}
	return title
}

// recordNativeProgress is called only for an accepted submission or a changed
// owned native observation. The journal persists the fingerprint and time.
func recordNativeProgress(j Job, fingerprint string, now time.Time) Job {
	if !hexSHA.MatchString(fingerprint) || fingerprint == j.ProgressFingerprint || now.IsZero() {
		return j
	}
	at := now.UTC()
	if j.ProgressAt != nil && !at.After(*j.ProgressAt) {
		// Retain the new evidence even when the clock has not advanced; the
		// same observation on a later poll must not acquire a fresh timestamp.
		j.ProgressFingerprint = fingerprint
		return j
	}
	j.ProgressFingerprint, j.ProgressAt = fingerprint, &at
	return j
}

func observedNativeProgress(j Job, turn Turn, now time.Time) Job {
	if !validSession(j.SessionID) || !validMessage(j.MessageID) || turn.SessionID != j.SessionID || turn.MessageID != j.MessageID || !turn.Accepted {
		return j
	}
	if j.ProgressAt == nil && j.ProgressFingerprint == "" && hexSHA.MatchString(turn.ProgressFingerprint) {
		// Old journals have no prior observation to compare. Establish a
		// durable baseline without assigning restart time to historical work.
		j.ProgressFingerprint = turn.ProgressFingerprint
		return j
	}
	return recordNativeProgress(j, turn.ProgressFingerprint, now)
}
