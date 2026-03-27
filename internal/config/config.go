package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Dev       string
	HostsRaw  []string
	Frequency time.Duration
	StatePath string
	DryRun    bool
	LogLevel  string
	Show      bool
}

const defaultFrequency = 30 * time.Second

// Default for root-run local agent.
const defaultStatePath = "~/.config/vpn-routes/state.json"

func Parse(args []string) (Config, error) {
	cfg := Config{
		Frequency: defaultFrequency,
		StatePath: defaultStatePath,
		LogLevel:  "info",
	}

	// Load JSON config first (if provided).
	configPath := findFlagValue(args, "--config")
	if configPath != "" {
		configPath = expandUser(configPath)
		fileCfg, err := loadJSONConfig(configPath)
		if err != nil {
			return Config{}, err
		}
		cfg = mergeFile(cfg, fileCfg)
	}

	// Parse CLI flags, tracking which ones were explicitly set so they can override file config.
	var (
		devFlag       = newStringFlag()
		hostsFlag     = newStringFlag()
		freqFlag      = newDurationFlag()
		statePathFlag = newStringFlag()
		dryRunFlag    = newBoolFlag()
		logLevelFlag  = newStringFlag()
		showFlag      = newBoolFlag()
	)

	fs := flag.NewFlagSet("vpn-routes", flag.ContinueOnError)
	_ = fs.String("config", configPath, "Path to JSON config file (loaded first; CLI flags override)")
	fs.Var(devFlag, "dev", "VPN device interface name (e.g. utun4)")
	fs.Var(hostsFlag, "hosts", "Comma/space-separated list of hosts, IPv4s, and/or IPv4 CIDRs")
	fs.Var(freqFlag, "freq", "How often to reconcile (e.g. 30s, 1m)")
	fs.Var(statePathFlag, "state-path", "Path to owned-routes state file")
	fs.Var(dryRunFlag, "dry-run", "Log route changes that would be made, without making them")
	fs.Var(logLevelFlag, "log-level", "Log level: debug, info, warn, error")
	fs.Var(showFlag, "show", "Print routes using --dev and mark managed routes, then exit")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if devFlag.set {
		cfg.Dev = devFlag.value
	}
	if hostsFlag.set {
		cfg.HostsRaw = splitHosts(hostsFlag.value)
	}
	if freqFlag.set {
		cfg.Frequency = freqFlag.value
	}
	if statePathFlag.set {
		cfg.StatePath = statePathFlag.value
	}
	if dryRunFlag.set {
		cfg.DryRun = dryRunFlag.value
	}
	if logLevelFlag.set {
		cfg.LogLevel = logLevelFlag.value
	}
	if showFlag.set {
		cfg.Show = showFlag.value
	}

	cfg.Dev = strings.TrimSpace(cfg.Dev)
	if cfg.Dev == "" {
		return Config{}, errors.New("--dev is required (via --dev or config file)")
	}
	if cfg.Frequency <= 0 {
		return Config{}, fmt.Errorf("--freq must be > 0 (got %s)", cfg.Frequency)
	}

	cfg.StatePath = strings.TrimSpace(cfg.StatePath)
	if cfg.StatePath == "" {
		return Config{}, errors.New("--state-path must be non-empty")
	}
	cfg.StatePath = expandUser(cfg.StatePath)

	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	switch cfg.LogLevel {
	case "debug", "info", "warn", "warning", "error":
		// ok (normalize warning -> warn later if desired)
	default:
		return Config{}, fmt.Errorf("invalid --log-level %q (expected debug|info|warn|error)", cfg.LogLevel)
	}

	if len(cfg.HostsRaw) == 0 {
		// If still empty, it means neither file nor CLI provided hosts.
		return Config{}, errors.New("--hosts is required (via --hosts or config file)")
	}
	if len(cfg.HostsRaw) == 0 {
		return Config{}, errors.New("--hosts is required (provide at least one host/ip/cidr)")
	}

	return cfg, nil
}

func splitHosts(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	// Support comma-separated and whitespace-separated forms.
	s = strings.ReplaceAll(s, ",", " ")
	parts := strings.Fields(s)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

type fileConfig struct {
	Dev       string   `json:"dev"`
	Hosts     []string `json:"hosts"`
	Freq      string   `json:"freq"`
	StatePath string   `json:"statePath"`
	DryRun    *bool    `json:"dryRun"`
	LogLevel  string   `json:"logLevel"`
}

func loadJSONConfig(path string) (fileConfig, error) {
	path = expandUser(path)
	b, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, fmt.Errorf("read config file %q: %w", path, err)
	}
	var fc fileConfig
	if err := json.Unmarshal(b, &fc); err != nil {
		return fileConfig{}, fmt.Errorf("parse config file %q: %w", path, err)
	}
	return fc, nil
}

func mergeFile(base Config, fc fileConfig) Config {
	if strings.TrimSpace(fc.Dev) != "" {
		base.Dev = strings.TrimSpace(fc.Dev)
	}
	if len(fc.Hosts) > 0 {
		base.HostsRaw = splitHosts(strings.Join(fc.Hosts, " "))
	}
	if strings.TrimSpace(fc.Freq) != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(fc.Freq)); err == nil {
			base.Frequency = d
		} else {
			// Keep base.Frequency; validation will catch invalid values if CLI overrides to bad.
		}
	}
	if strings.TrimSpace(fc.StatePath) != "" {
		base.StatePath = strings.TrimSpace(fc.StatePath)
	}
	if fc.DryRun != nil {
		base.DryRun = *fc.DryRun
	}
	if strings.TrimSpace(fc.LogLevel) != "" {
		base.LogLevel = strings.TrimSpace(fc.LogLevel)
	}
	return base
}

func findFlagValue(args []string, name string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"=")
		}
	}
	return ""
}

func expandUser(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "~" {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}

type stringFlag struct {
	set   bool
	value string
}

func newStringFlag() *stringFlag { return &stringFlag{} }

func (f *stringFlag) String() string { return f.value }
func (f *stringFlag) Set(v string) error {
	f.set = true
	f.value = v
	return nil
}

type boolFlag struct {
	set   bool
	value bool
}

func newBoolFlag() *boolFlag { return &boolFlag{} }

func (f *boolFlag) String() string {
	if f.value {
		return "true"
	}
	return "false"
}
func (f *boolFlag) Set(v string) error {
	f.set = true
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "true", "1", "t", "yes", "y":
		f.value = true
		return nil
	case "false", "0", "f", "no", "n":
		f.value = false
		return nil
	default:
		return fmt.Errorf("invalid boolean %q", v)
	}
}
func (f *boolFlag) IsBoolFlag() bool { return true }

type durationFlag struct {
	set   bool
	value time.Duration
}

func newDurationFlag() *durationFlag { return &durationFlag{} }

func (f *durationFlag) String() string { return f.value.String() }
func (f *durationFlag) Set(v string) error {
	d, err := time.ParseDuration(v)
	if err != nil {
		return err
	}
	f.set = true
	f.value = d
	return nil
}
