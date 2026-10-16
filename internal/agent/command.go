package agent

import "github.com/sarcasticbird/coop/internal/config"

// GuestArgv shares configured environment routing without preparation or PTY entry.
func (s *Session) GuestArgv(args []string) []string {
	argv := append([]string{"/usr/local/libexec/coop-env"}, args...)
	for _, name := range s.Spec.Config.IncludeCredentials {
		for _, expose := range config.Exposures(s.Spec.Config.Credentials[name]) {
			if expose.Type == "file" && expose.PathEnv == "COOP_GITHUB_APP_BUNDLE_FILE" {
				argv = append([]string{"/usr/local/libexec/coop-env", "/usr/local/libexec/coop-github", "env", "--"}, args...)
				return argv
			}
		}
	}
	return argv
}
