package credential

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type awsProcessDocument struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

// ParseAWSProcess validates an AWS process credential document without ever
// including secret field values in errors.
func ParseAWSProcess(raw []byte, now time.Time, requireExpiration bool) (AWSCredentials, Metadata, error) {
	var document awsProcessDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return AWSCredentials{}, Metadata{}, fmt.Errorf("parse AWS process credentials: %w", err)
	}
	if document.Version != 1 {
		return AWSCredentials{}, Metadata{}, fmt.Errorf("AWS process credentials use unsupported version %d", document.Version)
	}
	if document.AccessKeyID == "" || document.SecretAccessKey == "" {
		return AWSCredentials{}, Metadata{}, errors.New("AWS process credentials are missing required key fields")
	}
	for _, value := range []string{document.AccessKeyID, document.SecretAccessKey, document.SessionToken} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return AWSCredentials{}, Metadata{}, errors.New("AWS process credentials contain invalid control characters")
		}
	}

	metadata := Metadata{Provider: "aws-profile"}
	if document.Expiration == "" {
		if requireExpiration {
			return AWSCredentials{}, Metadata{}, errors.New("AWS process credentials do not include required expiration")
		}
	} else {
		expiresAt, err := time.Parse(time.RFC3339, document.Expiration)
		if err != nil {
			return AWSCredentials{}, Metadata{}, errors.New("AWS process credentials contain an invalid expiration")
		}
		if !expiresAt.After(now) {
			return AWSCredentials{}, Metadata{}, errors.New("AWS process credentials are expired")
		}
		metadata.ExpiresAt = expiresAt
	}

	return AWSCredentials{
		accessKeyID:     document.AccessKeyID,
		secretAccessKey: document.SecretAccessKey,
		sessionToken:    document.SessionToken,
	}, metadata, nil
}

func (m *Manager) acquireAWS(ctx context.Context, home string, selected Selected) (Acquired, error) {
	profile := selected.Spec.Source.Profile
	argv := []string{"aws", "configure", "export-credentials", "--profile", profile, "--format", "process"}
	raw, err := m.runAcquisitionCommand(ctx, home, argv)
	if err != nil {
		return Acquired{}, err
	}
	defer zeroBytes(raw)

	awsCredentials, metadata, err := ParseAWSProcess(raw, m.Now(), selected.Spec.RequireExpiration)
	if err != nil {
		return Acquired{}, err
	}
	metadata.Profile = profile
	return Acquired{
		Selected: selected,
		aws:      &awsCredentials,
		metadata: metadata,
	}, nil
}

func zeroBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
