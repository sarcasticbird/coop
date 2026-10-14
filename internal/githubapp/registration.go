// Package githubapp acquires host-only GitHub App credentials and routes guest
// operations to the current owner-specific installation token.
package githubapp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const MaxDocument = 1 << 20

var ownerName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
var slugName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

type Installation struct {
	ID           int64             `json:"id"`
	Repositories string            `json:"repositories"`
	Permissions  map[string]string `json:"permissions"`
}
type Registration struct {
	AppID          int64                   `json:"app_id"`
	Slug           string                  `json:"slug"`
	AppOwner       string                  `json:"app_owner"`
	PrivateKeyPath string                  `json:"private_key_path"`
	BotLogin       string                  `json:"bot_login"`
	BotID          int64                   `json:"bot_id"`
	Installations  map[string]Installation `json:"installations"`
}

func (r Registration) Validate() error {
	if r.AppID <= 0 || r.BotID <= 0 || !slugName.MatchString(r.Slug) || r.BotLogin != r.Slug+"[bot]" || !ownerName.MatchString(r.AppOwner) || !filepath.IsAbs(r.PrivateKeyPath) || len(r.Installations) == 0 || len(r.Installations) > 64 {
		return errors.New("invalid GitHub App registration")
	}
	seen := map[int64]bool{}
	owners := map[string]bool{}
	for owner, i := range r.Installations {
		lower := strings.ToLower(owner)
		if !ownerName.MatchString(owner) || owners[lower] || i.ID <= 0 || seen[i.ID] || i.Repositories != "all" || len(i.Permissions) == 0 {
			return errors.New("invalid GitHub App installation scope")
		}
		owners[lower] = true
		seen[i.ID] = true
		for key, access := range i.Permissions {
			if key == "" || (access != "read" && access != "write") {
				return errors.New("invalid GitHub App permission")
			}
		}
	}
	return nil
}

func LoadRegistration(path string) (Registration, error) {
	var r Registration
	f, err := os.Open(path)
	if err != nil {
		return r, errors.New("cannot open GitHub App registration")
	}
	defer func() { _ = f.Close() }()
	raw, err := readBounded(f)
	if err != nil {
		return r, err
	}
	if json.Unmarshal(raw, &r) != nil {
		return r, errors.New("invalid GitHub App registration document")
	}
	return r, r.Validate()
}
