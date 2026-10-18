package masterclient

import (
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/credential"
)

type FileConfig struct {
	URL               string `toml:"url"`
	MasterID          string `toml:"master_id"`
	TokenEnv          string `toml:"token_env"`
	TokenFile         string `toml:"token_file"`
	AllowLoopbackHTTP bool   `toml:"allow_loopback_http"`
}

func LoadConfig(path string) (*Client, error) {
	raw, err := credential.ReadPrivateFile(path)
	if err != nil {
		return nil, c.ErrForbidden
	}
	if len(raw) > c.MaxJSON {
		return nil, c.ErrSize
	}
	var cfg FileConfig
	md, err := toml.Decode(string(raw), &cfg)
	if err != nil || len(md.Undecoded()) > 0 || (cfg.TokenEnv == "") == (cfg.TokenFile == "") {
		return nil, c.ErrInvalid
	}
	token := os.Getenv(cfg.TokenEnv)
	if cfg.TokenFile != "" {
		raw, err := credential.ReadPrivateFile(cfg.TokenFile)
		if err != nil {
			return nil, c.ErrForbidden
		}
		if len(raw) > 4096 {
			return nil, c.ErrSize
		}
		token = strings.TrimSpace(string(raw))
	}
	return New(ClientConfig{URL: cfg.URL, MasterID: cfg.MasterID, Token: token, AllowHTTP: cfg.AllowLoopbackHTTP})
}
