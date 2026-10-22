package worker

import (
	"errors"
	"io"
)

// QASourceDigest computes the source-bundle-v1 digest of a gzip tar archive.
// Compressed input is bounded separately from ordinary GitHub JSON pages.
func QASourceDigest(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxSourceArchive+1))
	if err != nil {
		return "", err
	}
	if len(raw) > MaxSourceArchive {
		return "", errors.New("QA source archive exceeds compressed size limit")
	}
	return sourceBundleSHA(raw)
}
