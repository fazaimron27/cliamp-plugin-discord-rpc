// Package config loads and validates cliamp-rpcd configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const DefaultStateMaxAge = 45 * time.Second

// Config contains all runtime settings needed by the daemon.
type Config struct {
	ApplicationID string
	StatePath     string
	CliampConfig  string
	LargeImage    string
	LargeText     string
	LastFMAPIKey  string
	PollInterval  time.Duration
	StateMaxAge   time.Duration
}

// Load parses command-line arguments, then fills credentials from environment
// variables and the dedicated [plugins.discord-rpc] Cliamp config section.
func Load(args []string) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{}
	flags := flag.NewFlagSet("cliamp-rpcd", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		writeUsage(flags)
	}
	flags.StringVar(&cfg.ApplicationID, "app-id", os.Getenv("CLIAMP_DISCORD_APP_ID"), "Discord application `ID` (or CLIAMP_DISCORD_APP_ID)")
	flags.StringVar(&cfg.StatePath, "state", filepath.Join(home, ".local", "share", "cliamp", "rpc-state.json"), "Cliamp RPC state file `path`")
	flags.StringVar(&cfg.CliampConfig, "config", filepath.Join(home, ".config", "cliamp", "config.toml"), "Cliamp config file `path` containing Discord RPC credentials")
	flags.StringVar(&cfg.LargeImage, "large-image", envOr("CLIAMP_DISCORD_LARGE_IMAGE", "cliamp"), "Discord application asset `key`")
	flags.StringVar(&cfg.LargeText, "large-text", envOr("CLIAMP_DISCORD_LARGE_TEXT", "Cliamp"), "large image hover `text`")
	flags.DurationVar(&cfg.PollInterval, "poll", time.Second, "state polling `duration`")
	flags.DurationVar(&cfg.StateMaxAge, "max-age", DefaultStateMaxAge, "clear presence after state heartbeat exceeds `duration`")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}

	if cfg.ApplicationID == "" {
		cfg.ApplicationID, err = readTOMLValue(cfg.CliampConfig, "plugins.discord-rpc", "app_id")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read Discord RPC app ID: %w", err)
		}
	}
	cfg.LastFMAPIKey = os.Getenv("CLIAMP_DISCORD_LASTFM_API_KEY")
	if cfg.LastFMAPIKey == "" {
		cfg.LastFMAPIKey, err = readTOMLValue(cfg.CliampConfig, "plugins.discord-rpc", "lastfm_api_key")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read Discord Last.fm API key: %w", err)
		}
	}

	if cfg.ApplicationID == "" {
		return Config{}, errors.New("Discord application ID is required; set plugins.discord-rpc.app_id, CLIAMP_DISCORD_APP_ID, or pass --app-id")
	}
	if cfg.PollInterval <= 0 || cfg.StateMaxAge <= 0 {
		return Config{}, errors.New("poll interval and max age must be positive")
	}
	return cfg, nil
}

func writeUsage(flags *flag.FlagSet) {
	fmt.Fprintf(flags.Output(), "Usage: %s [options]\n", flags.Name())
	writer := tabwriter.NewWriter(flags.Output(), 0, 4, 2, ' ', 0)
	flags.VisitAll(func(option *flag.Flag) {
		valueName, usage := flag.UnquoteUsage(option)
		if valueName != "" {
			valueName = " " + valueName
		}
		defaultValue := ""
		if option.Name != "app-id" && option.DefValue != "" {
			defaultValue = fmt.Sprintf(" (default %q)", option.DefValue)
		}
		fmt.Fprintf(writer, "  --%s%s\t%s%s\n", option.Name, valueName, usage, defaultValue)
	})
	_ = writer.Flush()
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func readTOMLValue(path, wantedSection, wantedKey string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	section := ""
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != wantedSection || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) != wantedKey {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return "", fmt.Errorf("invalid %s value", wantedKey)
			}
			return unquoted, nil
		}
		return strings.TrimSpace(strings.SplitN(value, "#", 2)[0]), nil
	}
	return "", nil
}
