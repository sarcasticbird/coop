package projectinit

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sarcasticbird/coop/internal/config"
)

func Run(root string, in io.Reader, out io.Writer) error {
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	candidates, err := Discover(root, cfg.Volumes)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	if _, err := fmt.Fprintf(out, "Project: %s\n", root); err != nil {
		return fmt.Errorf("write project init header: %w", err)
	}

	selectedVolumes := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		answer, readErr := prompt(reader, out, fmt.Sprintf("Add project volume %s? [y/N] ", candidate))
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return decline(out)
			}
			return fmt.Errorf("read project volume response: %w", readErr)
		}
		if strings.EqualFold(answer, "y") {
			selectedVolumes = append(selectedVolumes, candidate)
		}
	}

	usedHosts := make(map[int]int, len(cfg.Publishes))
	for _, publish := range cfg.Publishes {
		usedHosts[publish.HostPort] = publish.GuestPort
	}
	selectedPublishes := make([]config.Publish, 0)
	for len(cfg.Publishes)+len(selectedPublishes) < config.MaxPublishedPorts {
		guestText, readErr := prompt(reader, out, "Guest TCP port (blank to finish): ")
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return decline(out)
			}
			return fmt.Errorf("read guest port: %w", readErr)
		}
		if guestText == "" {
			break
		}
		guestPort, err := parsePort(guestText)
		if err != nil {
			if _, writeErr := fmt.Fprintf(out, "Invalid guest port: %v\n", err); writeErr != nil {
				return fmt.Errorf("write guest port error: %w", writeErr)
			}
			continue
		}

		for {
			hostText, readErr := prompt(reader, out, fmt.Sprintf("Host TCP port [%d]: ", guestPort))
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return decline(out)
				}
				return fmt.Errorf("read host port: %w", readErr)
			}
			hostPort := guestPort
			if hostText != "" {
				hostPort, err = parsePort(hostText)
				if err != nil {
					if _, writeErr := fmt.Fprintf(out, "Invalid host port: %v\n", err); writeErr != nil {
						return fmt.Errorf("write host port error: %w", writeErr)
					}
					continue
				}
			}
			if existingGuest, exists := usedHosts[hostPort]; exists {
				if existingGuest == guestPort {
					if _, err := fmt.Fprintf(out, "Publication 127.0.0.1:%d:%d/tcp is already declared; skipping.\n", hostPort, guestPort); err != nil {
						return fmt.Errorf("write duplicate publication notice: %w", err)
					}
					break
				}
				if _, err := fmt.Fprintf(out, "Host port %d already maps to guest port %d; choose another.\n", hostPort, existingGuest); err != nil {
					return fmt.Errorf("write conflicting publication notice: %w", err)
				}
				continue
			}
			usedHosts[hostPort] = guestPort
			selectedPublishes = append(selectedPublishes, config.Publish{HostPort: hostPort, GuestPort: guestPort})
			break
		}
	}
	validation := config.Config{
		Volumes:   append([]config.Volume(nil), cfg.Volumes...),
		Publishes: append([]config.Publish(nil), cfg.Publishes...),
	}
	for _, path := range selectedVolumes {
		validation.Volumes = append(validation.Volumes, config.Volume{Path: path})
	}
	validation.Publishes = append(validation.Publishes, selectedPublishes...)
	if err := config.ValidateProjectRuntime(&validation, root); err != nil {
		return fmt.Errorf("validate project init selections: %w", err)
	}

	block, err := Render(selectedVolumes, selectedPublishes)
	if err != nil {
		return fmt.Errorf("render project init selections: %w", err)
	}
	if len(block) == 0 {
		_, err := fmt.Fprintln(out, "No changes selected.")
		return err
	}
	if _, err := fmt.Fprintf(out, "\nProposed .coop.toml append:\n\n%s\n", block); err != nil {
		return fmt.Errorf("write project init preview: %w", err)
	}
	answer, readErr := prompt(reader, out, "Apply these changes? [y/N] ")
	if readErr != nil {
		if errors.Is(readErr, io.EOF) {
			return decline(out)
		}
		return fmt.Errorf("read final project init confirmation: %w", readErr)
	}
	if !strings.EqualFold(answer, "y") {
		return decline(out)
	}
	if err := EnsureLocalExclude(root); err != nil {
		return err
	}
	if err := AppendConfig(root, block); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Updated %s.\nRun `coop up` or enter the Coop to apply the new container configuration.\n", filepath.Join(root, ".coop.toml")); err != nil {
		return fmt.Errorf("write project init completion: %w", err)
	}
	return nil
}

func prompt(reader *bufio.Reader, out io.Writer, question string) (string, error) {
	if _, err := fmt.Fprint(out, question); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return strings.TrimSpace(line), err
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("must be an integer from 1 through 65535")
	}
	return port, nil
}

func decline(out io.Writer) error {
	_, err := fmt.Fprintln(out, "No changes written.")
	return err
}
