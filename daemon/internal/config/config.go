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
)

const DefaultApplicationID = "1537329890829926400"

// Config contains all runtime settings needed by the daemon.
type Config struct {
	// ShowVersion asks the caller to print the version and exit instead of
	// running the daemon.
	ShowVersion bool
	// ShowCheck asks the caller to probe the runtime environment, report, and
	// exit instead of running the daemon.
	ShowCheck bool

	ApplicationID string
	CliampSocket  string
	CliampConfig  string
	LargeImage    string
	LargeText     string
	LastFMAPIKey  string
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
	flags.BoolVar(&cfg.ShowVersion, "version", false, "print the version and exit")
	flags.BoolVar(&cfg.ShowCheck, "check", false, "probe the runtime environment, report, and exit")
	flags.StringVar(&cfg.ApplicationID, "app-id", os.Getenv("CLIAMP_DISCORD_APP_ID"), "Discord application `ID` (or CLIAMP_DISCORD_APP_ID)")
	flags.StringVar(&cfg.CliampSocket, "socket", filepath.Join(home, ".config", "cliamp", "cliamp.sock"), "Cliamp IPC socket `path`")
	flags.StringVar(&cfg.CliampConfig, "config", filepath.Join(home, ".config", "cliamp", "config.toml"), "Cliamp config file `path` containing Discord RPC credentials")
	flags.StringVar(&cfg.LargeImage, "large-image", envOr("CLIAMP_DISCORD_LARGE_IMAGE", "cliamp"), "Discord application asset `key`")
	flags.StringVar(&cfg.LargeText, "large-text", envOr("CLIAMP_DISCORD_LARGE_TEXT", "Cliamp"), "large image hover `text`")
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
		cfg.ApplicationID = DefaultApplicationID
	}
	if cfg.CliampSocket == "" {
		return Config{}, errors.New("Cliamp socket path must not be empty")
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
		// A boolean flag's zero value is not worth printing as a default.
		boolean, isBoolean := option.Value.(interface{ IsBoolFlag() bool })
		defaultValue := ""
		if option.Name != "app-id" && option.DefValue != "" && !(isBoolean && boolean.IsBoolFlag()) {
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
		value = trimTOMLComment(strings.TrimSpace(value))
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			// A literal string takes its content verbatim, so it is unwrapped
			// rather than unescaped. Handing it to strconv.Unquote would fail:
			// Go reads '1234' as a rune literal, which TOML does not mean.
			if value[0] == '\'' {
				return value[1 : len(value)-1], nil
			}
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return "", fmt.Errorf("invalid %s value", wantedKey)
			}
			return unquoted, nil
		}
		return value, nil
	}
	return "", nil
}

// trimTOMLComment removes a trailing comment from a TOML value. TOML permits a #
// inside both basic and literal strings, so a quoted value is scanned to its
// closing quote and only an unquoted value is split at the first #.
//
// Stripping the comment before testing for quotes is what the caller depends on:
// requiring a quote as the final character meant `app_id = "1" # note` fell
// through as an unquoted value, kept its quotes, and failed the Discord
// handshake with a configuration error that named neither the file nor the line.
func trimTOMLComment(value string) string {
	if value == "" || (value[0] != '"' && value[0] != '\'') {
		if index := strings.IndexByte(value, '#'); index >= 0 {
			return strings.TrimSpace(value[:index])
		}
		return value
	}

	quote := value[0]
	for index := 1; index < len(value); index++ {
		// A backslash escapes the next character inside a basic string. A
		// literal string has no escapes, so a backslash closes nothing there
		// and must not be allowed to skip over the closing quote.
		if quote == '"' && value[index] == '\\' {
			index++
			continue
		}
		if value[index] == quote {
			return value[:index+1]
		}
	}
	// Unterminated. Returned as written so the ordinary unquoted path handles
	// it rather than this function inventing a reason of its own.
	return value
}
