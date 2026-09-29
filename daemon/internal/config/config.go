// Package config loads and validates cliamp-rpcd configuration.
package config

// This file is the configuration surface: it names the settings the daemon runs
// on, reads them from flags, environment variables, and Cliamp's config.toml,
// and records which of those a transport value came from.
//
// Two settings are joint with the plugin, because the Lua plugin reads the same
// config.toml keys and has no command line or environment of its own. The
// transport may still be overridden from this side, which is why its source is
// recorded and why such an override is reported through TransportWarning rather
// than obeyed quietly. The state path deliberately has no flag or environment
// variable: an override here could only be made where the plugin cannot see it,
// leaving the daemon watching a path nothing writes.

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

// DefaultApplicationID is the community-maintained Cliamp Discord application,
// used for presence unless a custom ID is supplied through --app-id,
// CLIAMP_DISCORD_APP_ID, or the plugin section of Cliamp's config file.
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
	// TransportSource records where Transport came from: SourceFlag,
	// SourceEnvironment, SourceFile, or SourceDefault.
	TransportSource string
	// TransportFromFile is the raw value config.toml carried, whatever won.
	TransportFromFile string
	// StatePath is the state document the file transport reads. The default is
	// the path v1.4.0 wrote, which an existing legacy install already has.
	StatePath string
	// StateMaxAge is how long a document stays live after its last heartbeat.
	StateMaxAge time.Duration
}

// Where a transport value came from.
const (
	SourceFlag        = "flag"
	SourceEnvironment = "environment"
	SourceFile        = "config file"
	SourceDefault     = "default"
)

// PluginTransport is the transport the Lua plugin will read, which is the value
// config.toml names or, with no such key, the plugin's own default. That default
// is part of the plugin's source, so it is repeated here as TransportIPC — the
// transport guards beside the plugin, in statewatch's plugin contract test, hold
// the two to each other.
func (c Config) PluginTransport() string {
	if c.TransportFromFile == "" {
		return TransportIPC
	}
	return c.TransportFromFile
}

// TransportDisagreesWithPlugin reports whether this daemon was pointed at a
// transport other than the one the plugin will use.
//
// The Lua plugin has no command line and no environment of its own: p:config()
// reading config.toml is its only source, so an override here is the one way
// the two halves can be made to disagree, and it is silent without this check —
// the daemon would simply watch a file nothing writes, or subscribe to a stream
// nothing publishes to.
func (c Config) TransportDisagreesWithPlugin() bool {
	if c.TransportSource != SourceFlag && c.TransportSource != SourceEnvironment {
		return false
	}
	return c.PluginTransport() != c.Transport
}

// TransportWarning explains a transport override the plugin cannot see, or
// returns "" when there is nothing to warn about. It names both halves, because
// either of them could be the one to change.
func (c Config) TransportWarning() string {
	if !c.TransportDisagreesWithPlugin() {
		return ""
	}
	return fmt.Sprintf(
		"playback transport %s comes from the %s, but the plugin reads config.toml and will use %s: one of the two has to be changed to match",
		c.Transport, c.TransportSource, c.PluginTransport(),
	)
}

// Load parses command-line arguments, then fills credentials from environment
// variables and the dedicated [plugins.discord-rpc] Cliamp config section.
//
// The transport flag defaults to empty so that an unset one falls through to
// config.toml, which is the one source the Lua plugin can also read. A transport
// naming neither of the two known values is rejected rather than fallen back
// from, because a misspelled value would otherwise start the daemon on the wrong
// source and look exactly like a Cliamp that sends nothing. A non-positive
// maximum age is rejected for the same kind of reason: a zero window would clear
// every document the moment it arrived, so the file transport could never show
// anything.
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
	flags.StringVar(&cfg.Transport, "transport", envOr("CLIAMP_DISCORD_TRANSPORT", ""), "playback event `transport`: ipc or file (or CLIAMP_DISCORD_TRANSPORT)")
	flags.DurationVar(&cfg.StateMaxAge, "max-age", DefaultStateMaxAge, "clear presence after the file transport's heartbeat exceeds `duration`")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	given := make(map[string]bool)
	flags.Visit(func(option *flag.Flag) { given[option.Name] = true })

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
	cfg.TransportFromFile, err = readTOMLValue(cfg.CliampConfig, "plugins.discord-rpc", "transport")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read playback transport: %w", err)
	}
	switch {
	case cfg.Transport != "" && given["transport"]:
		cfg.TransportSource = SourceFlag
	case cfg.Transport != "":
		cfg.TransportSource = SourceEnvironment
	case cfg.TransportFromFile != "":
		cfg.Transport = cfg.TransportFromFile
		cfg.TransportSource = SourceFile
	default:
		cfg.Transport = TransportIPC
		cfg.TransportSource = SourceDefault
	}
	if cfg.Transport != TransportIPC && cfg.Transport != TransportFile {
		return Config{}, fmt.Errorf(
			"playback transport %q from the %s is not one of %q or %q",
			cfg.Transport, cfg.TransportSource, TransportIPC, TransportFile,
		)
	}
	cfg.StatePath, err = readTOMLValue(cfg.CliampConfig, "plugins.discord-rpc", "state_path")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read state file path: %w", err)
	}
	if cfg.StatePath == "" {
		cfg.StatePath = filepath.Join(home, ".local", "share", "cliamp", "rpc-state.json")
	}
	if cfg.StateMaxAge <= 0 {
		return Config{}, errors.New("max age must be positive")
	}
	if cfg.CliampSocket == "" {
		return Config{}, errors.New("Cliamp socket path must not be empty")
	}
	return cfg, nil
}

// writeUsage prints the flag list, with each flag's default beside it. A boolean
// flag's zero value is left out rather than printed as a default, because false
// is what the flag already means when it is not given.
func writeUsage(flags *flag.FlagSet) {
	fmt.Fprintf(flags.Output(), "Usage: %s [options]\n", flags.Name())
	writer := tabwriter.NewWriter(flags.Output(), 0, 4, 2, ' ', 0)
	flags.VisitAll(func(option *flag.Flag) {
		valueName, usage := flag.UnquoteUsage(option)
		if valueName != "" {
			valueName = " " + valueName
		}
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

// readTOMLValue reads one key from one section of a TOML file, returning "" when
// the file or the key is absent. A quoted value is unwrapped: a basic string is
// unescaped with strconv.Unquote, and a literal string is taken verbatim, since
// Go reads '1234' as a rune literal, which is not what TOML means by it.
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
//
// A backslash escapes the next character inside a basic string, so the scan
// steps over a pair of them. A literal string has no escapes, so a backslash
// there closes nothing and must not be allowed to skip the closing quote. An
// unterminated value is returned as written, so the ordinary unquoted path
// handles it rather than this function inventing a reason of its own.
func trimTOMLComment(value string) string {
	if value == "" || (value[0] != '"' && value[0] != '\'') {
		if index := strings.IndexByte(value, '#'); index >= 0 {
			return strings.TrimSpace(value[:index])
		}
		return value
	}

	quote := value[0]
	for index := 1; index < len(value); index++ {
		if quote == '"' && value[index] == '\\' {
			index++
			continue
		}
		if value[index] == quote {
			return value[:index+1]
		}
	}
	return value
}
