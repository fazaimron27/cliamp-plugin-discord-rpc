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

const DefaultApplicationID = "1537329890829926400"

// The two playback event transports. IPC is the retained pub/sub stream that
// current Cliamp builds expose; the file is the state document an older Cliamp
// can write with cliamp.fs, restored from v1.4.0.
const (
	TransportIPC  = "ipc"
	TransportFile = "file"
)

// DefaultStateMaxAge is how long a file transport document stays live after its
// last heartbeat. The plugin heartbeats every 15s, so this tolerates two missed
// beats before a crashed Cliamp stops being reported as playing.
const DefaultStateMaxAge = 45 * time.Second

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
	// Transport names the playback event source: TransportIPC or TransportFile.
	// The plugin reads the same config.toml key, so the two halves agree unless
	// one of them is overridden here.
	Transport string
	// StatePath is the state document the file transport reads. The default is
	// the path v1.4.0 wrote, which an existing legacy install already has.
	StatePath string
	// StateMaxAge is how long a document stays live after its last heartbeat.
	StateMaxAge time.Duration
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
	// The transport flags default to empty so an unset one can fall through to
	// config.toml, the one place the Lua plugin can read from too.
	flags.StringVar(&cfg.Transport, "transport", envOr("CLIAMP_DISCORD_TRANSPORT", ""), "playback event `transport`: ipc or file (or CLIAMP_DISCORD_TRANSPORT)")
	flags.StringVar(&cfg.StatePath, "state", envOr("CLIAMP_DISCORD_STATE", ""), "state file `path` for the file transport (or CLIAMP_DISCORD_STATE)")
	flags.DurationVar(&cfg.StateMaxAge, "max-age", DefaultStateMaxAge, "clear presence after the file transport's heartbeat exceeds `duration`")
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
	if cfg.Transport == "" {
		cfg.Transport, err = readTOMLValue(cfg.CliampConfig, "plugins.discord-rpc", "transport")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read playback transport: %w", err)
		}
	}
	if cfg.Transport == "" {
		cfg.Transport = TransportIPC
	}
	// Reject rather than fall back: a misspelled transport would otherwise start
	// the daemon on the wrong source and look like a Cliamp that sends nothing.
	if cfg.Transport != TransportIPC && cfg.Transport != TransportFile {
		return Config{}, fmt.Errorf(
			"playback transport %q is not one of %q or %q", cfg.Transport, TransportIPC, TransportFile,
		)
	}
	if cfg.StatePath == "" {
		cfg.StatePath, err = readTOMLValue(cfg.CliampConfig, "plugins.discord-rpc", "state_path")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read state file path: %w", err)
		}
	}
	if cfg.StatePath == "" {
		cfg.StatePath = filepath.Join(home, ".local", "share", "cliamp", "rpc-state.json")
	}
	// A zero window would clear every document the moment it arrived, so the
	// file transport could never show anything.
	if cfg.StateMaxAge <= 0 {
		return Config{}, errors.New("max age must be positive")
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
