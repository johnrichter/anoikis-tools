// Package config loads anoikis-tools' settings through koanf, layered
// flag > env > file > default. It is load-only: nothing here writes a config
// file back.
//
// Every path the engine needs is a declared setting, not a literal somewhere
// in the code — including the harness policy, which is injected precisely so
// the engine carries no harness knowledge of its own.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/johnrichter/anoikis-tools/internal/effort"
)

// EnvPrefix namespaces every environment variable this CLI reads.
const EnvPrefix = "ANOIKIS_"

// PolicyFileName is the harness policy's default name inside an effort
// directory. The path itself is still a declared setting; this is only the
// value that setting takes when nothing overrides it.
const PolicyFileName = "harness-policy.json"

// DefaultTimeout bounds a subprocess the engine runs on the driver's behalf.
const DefaultTimeout = 10 * time.Minute

// Settings is one invocation's resolved configuration.
type Settings struct {
	// Repo is the repository root the effort directory hangs off.
	Repo string `koanf:"repo"`
	// Effort is the effort slug under .anoikis/.
	Effort string `koanf:"effort"`
	// Policy is the harness policy file. Empty resolves to the default name
	// inside the effort directory.
	Policy string `koanf:"policy"`
	// BaseRef overrides the commit a newly launched layer branches from,
	// which is otherwise the build branch's current head.
	BaseRef string `koanf:"base_ref"`
	// Timeout bounds each subprocess the engine runs.
	Timeout time.Duration `koanf:"timeout"`
}

var defaults = map[string]any{
	"repo":     "",
	"effort":   "",
	"policy":   "",
	"base_ref": "",
	"timeout":  DefaultTimeout,
}

// Load resolves settings from, in ascending priority: the built-in defaults,
// configFile when non-empty, the ANOIKIS_* environment, and finally flags.
//
// Repo defaults to the repository the working directory sits in, and Policy
// to the default file inside the resolved effort directory — both resolved
// here so no command re-derives them.
func Load(flags *pflag.FlagSet, configFile string) (*Settings, error) {
	k := koanf.New(".")
	if err := load(k, confmap.Provider(defaults, "."), nil, "defaults"); err != nil {
		return nil, err
	}
	if configFile != "" {
		if err := load(k, file.Provider(configFile), yaml.Parser(), configFile); err != nil {
			return nil, err
		}
	}
	if err := load(k, env.Provider(EnvPrefix, ".", envKey), nil, "environment"); err != nil {
		return nil, err
	}
	if err := load(k, flagProvider(flags, k), nil, "flags"); err != nil {
		return nil, err
	}
	var s Settings
	if err := k.Unmarshal("", &s); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}
	if s.Repo == "" {
		root, err := RepoRoot("")
		if err != nil {
			return nil, err
		}
		s.Repo = root
	}
	if s.Policy == "" && s.Effort != "" {
		s.Policy = filepath.Join(s.Repo, effort.DirName, s.Effort, PolicyFileName)
	}
	if s.Timeout <= 0 {
		s.Timeout = DefaultTimeout
	}
	return &s, nil
}

// RepoRoot walks up from start (the working directory when empty) to the
// nearest directory holding a .git entry.
func RepoRoot(start string) (string, error) {
	dir := start
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("config: resolve working directory: %w", err)
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("config: resolve %s: %w", dir, err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("config: no repository found at or above %s; pass --repo", dir)
		}
		dir = parent
	}
}

func load(k *koanf.Koanf, p koanf.Provider, pa koanf.Parser, source string) error {
	if err := k.Load(p, pa); err != nil {
		return fmt.Errorf("config: load %s: %w", source, err)
	}
	return nil
}

// envKey maps ANOIKIS_BASE_REF to the koanf key base_ref, and drops any
// variable outside the prefix.
func envKey(s string) string {
	if !strings.HasPrefix(s, EnvPrefix) {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(s, EnvPrefix))
}

// flagProvider reads flags into koanf keys using each flag's own typed value,
// translating hyphenated CLI names to the underscored keys the env and file
// layers share.
func flagProvider(flags *pflag.FlagSet, k *koanf.Koanf) *posflag.Posflag {
	return posflag.ProviderWithFlag(flags, ".", k, func(f *pflag.Flag) (string, any) {
		return strings.ReplaceAll(f.Name, "-", "_"), posflag.FlagVal(flags, f)
	})
}
