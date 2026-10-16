package worker

import (
	"errors"
	"io"
)

// QASourceDigest computes the source-bundle-v1 digest of a gzip tar archive.
// Compressed input is bounded by the same limit used for GitHub API downloads.
func QASourceDigest(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxAPIPage+1))
	if err != nil {
		return "", err
	}
	if len(raw) > MaxAPIPage {
		return "", errors.New("QA source archive exceeds compressed size limit")
	}
	return sourceBundleSHA(raw)
}
