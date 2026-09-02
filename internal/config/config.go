// Package config loads settings for qi-index's subcommands. Each command
// gets its own struct with built-in defaults; those defaults are layered
// over by an optional YAML file, then by environment variables, then by CLI
// flags — each layer overriding only the values it actually sets, so a
// value defined nowhere always falls back to its default.
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath is used when no -config flag or QI_CONFIG env var is given and
// a file exists there. Its absence is not an error: defaults, env vars and
// flags are enough to run.
const DefaultPath = "configs/config.yaml"

// Duration wraps time.Duration so it can be written in YAML as "15s" rather
// than a raw nanosecond count.
type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).String() }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// Follow holds every setting the `qi-index follow` command needs.
type Follow struct {
	RPCURL       string   `yaml:"rpc_url"`
	WSURL        string   `yaml:"ws_url"`
	DatabaseURL  string   `yaml:"database_url"`
	PollInterval Duration `yaml:"poll_interval"`
	ReorgDepth   int      `yaml:"reorg_depth"`
	MetricsAddr  string   `yaml:"metrics_addr"`
	Verbose      bool     `yaml:"verbose"`
}

func defaultFollow() Follow {
	return Follow{
		RPCURL:       "https://rpc.quai.network/cyprus1",
		WSURL:        "wss://rpc.quai.network/cyprus1",
		DatabaseURL:  "",
		PollInterval: Duration(15 * time.Second),
		ReorgDepth:   1024,
		MetricsAddr:  "",
		Verbose:      false,
	}
}

// file is the on-disk shape: one top-level key per subcommand, so the same
// file can grow to hold other commands' settings later.
type file struct {
	Follow Follow `yaml:"follow"`
}

// LoadFollow builds the Follow config for args (typically os.Args[2:]),
// layering a YAML file, environment variables and flags over the defaults,
// in that order.
func LoadFollow(args []string) (*Follow, error) {
	fs := flag.NewFlagSet("follow", flag.ExitOnError)
	configPath := fs.String("config", "", "path to YAML config file (default: "+DefaultPath+" if present)")
	rpcURL := fs.String("rpc", "", "zone RPC URL")
	wsURL := fs.String("ws", "", "zone WS URL (empty = poll only)")
	dbURL := fs.String("db", "", "Postgres URL (empty = in-memory store)")
	poll := fs.Duration("poll", 0, "safety-net head poll interval")
	depth := fs.Int("depth", 0, "reorg window depth")
	metricsAddr := fs.String("metrics", "", "metrics listen address, e.g. :2112 (empty = off)")
	verbose := fs.Bool("v", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfg := defaultFollow()

	path := resolvePath(*configPath)
	if path != "" {
		if err := mergeFollowFile(&cfg, path); err != nil {
			return nil, fmt.Errorf("load config file %s: %w", path, err)
		}
	}

	mergeFollowEnv(&cfg)

	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "rpc":
			cfg.RPCURL = *rpcURL
		case "ws":
			cfg.WSURL = *wsURL
		case "db":
			cfg.DatabaseURL = *dbURL
		case "poll":
			cfg.PollInterval = Duration(*poll)
		case "depth":
			cfg.ReorgDepth = *depth
		case "metrics":
			cfg.MetricsAddr = *metricsAddr
		case "v":
			cfg.Verbose = *verbose
		}
	})

	if cfg.RPCURL == "" {
		return nil, fmt.Errorf("rpc_url must not be empty")
	}
	if cfg.ReorgDepth <= 0 {
		return nil, fmt.Errorf("reorg_depth must be positive")
	}
	if time.Duration(cfg.PollInterval) <= 0 {
		return nil, fmt.Errorf("poll_interval must be positive")
	}
	return &cfg, nil
}

// resolvePath decides which config file (if any) to load: an explicit flag
// wins, then QI_CONFIG, then DefaultPath if it exists on disk.
func resolvePath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if env := os.Getenv("QI_CONFIG"); env != "" {
		return env
	}
	if _, err := os.Stat(DefaultPath); err == nil {
		return DefaultPath
	}
	return ""
}

func mergeFollowFile(cfg *Follow, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	f := file{Follow: *cfg}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return err
	}
	*cfg = f.Follow
	return nil
}

func mergeFollowEnv(cfg *Follow) {
	if v := os.Getenv("QI_RPC_URL"); v != "" {
		cfg.RPCURL = v
	}
	if v := os.Getenv("QI_WS_URL"); v != "" {
		cfg.WSURL = v
	}
	if v := os.Getenv("QI_DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	if v := os.Getenv("QI_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.PollInterval = Duration(d)
		}
	}
	if v := os.Getenv("QI_REORG_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ReorgDepth = n
		}
	}
	if v := os.Getenv("QI_METRICS_ADDR"); v != "" {
		cfg.MetricsAddr = v
	}
	if v := os.Getenv("QI_VERBOSE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Verbose = b
		}
	}
}
