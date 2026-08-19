package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	agyauth "github.com/whatnamed/agent-bridge/shared/antigravity/auth"
)

const (
	defaultHost                   = "127.0.0.1"
	defaultPort                   = 18080
	defaultModel                  = "gpt-5.6-luna"
	defaultBackendURL             = "https://chatgpt.com/backend-api/codex"
	defaultAntigravityEndpoint    = "https://daily-cloudcode-pa.googleapis.com"
	defaultAntigravityProfile     = "antigravity"
	defaultAntigravityCatalogTTL  = 45 * time.Minute
	defaultAntigravityProjectTTL  = 30 * time.Minute
	defaultClient                 = "1.0.0"
	defaultMaxStored              = 1000
	defaultConcurrency            = 10
	defaultTelemetryRetentionDays = 30
	defaultTelemetryEventMemory   = 200
	defaultTelemetryQueueSize     = 256
	defaultCodexImportDays        = 30
	defaultCodexScanInterval      = 60 * time.Second
)

type config struct {
	Host                      string
	Port                      int
	Model                     string
	BackendURL                string
	ClientVersion             string
	AuthJSON                  string
	APIKey                    string
	Timeout                   time.Duration
	MaxStored                 int
	Concurrency               int
	Verbose                   bool
	DropParams                []string
	StateDir                  string
	PIDFile                   string
	LogFile                   string
	StopTimeout               time.Duration
	TelemetryEnabled          bool
	TelemetryRetentionDays    int
	TelemetryEventMemoryLimit int
	TelemetryQueueSize        int
	DashboardEnabled          bool
	ReasoningSummaryDefault   string
	CodexCollectorEnabled     bool
	CodexSessionsDir          string
	CodexArchivedSessionsDir  string
	CodexImportDays           int
	CodexScanInterval         time.Duration
	AntigravityEnabled        bool
	AntigravityOAuthProfile   string
	AntigravityCredentialPath string
	AntigravityEndpoint       string
	AntigravityProject        string
	AntigravityCatalogTTL     time.Duration
	AntigravityProjectTTL     time.Duration
}

func defaultConfig() config {
	home, _ := os.UserHomeDir()
	auth := filepath.Join(home, ".codex", "auth.json")
	return config{
		Host: defaultHost, Port: defaultPort, Model: defaultModel,
		BackendURL: defaultBackendURL, ClientVersion: defaultClient, AuthJSON: auth,
		Timeout: 300 * time.Second, MaxStored: defaultMaxStored, Concurrency: defaultConcurrency,
		StateDir: defaultStateDir(), StopTimeout: 10 * time.Second,
		TelemetryEnabled: true, TelemetryRetentionDays: defaultTelemetryRetentionDays,
		TelemetryEventMemoryLimit: defaultTelemetryEventMemory, TelemetryQueueSize: defaultTelemetryQueueSize,
		DashboardEnabled: true, ReasoningSummaryDefault: "none",
		CodexCollectorEnabled: true, CodexImportDays: defaultCodexImportDays, CodexScanInterval: defaultCodexScanInterval,
		AntigravityOAuthProfile:   defaultAntigravityProfile,
		AntigravityCredentialPath: agyauth.DefaultCredentialPath(),
		AntigravityEndpoint:       defaultAntigravityEndpoint,
		AntigravityCatalogTTL:     defaultAntigravityCatalogTTL,
		AntigravityProjectTTL:     defaultAntigravityProjectTTL,
	}
}

func (c *config) applyEnvironment() {
	c.Host = envString("OPENAI_VIA_CODEX_HOST", c.Host)
	c.Port = envInt("OPENAI_VIA_CODEX_PORT", c.Port)
	c.Model = envString("OPENAI_VIA_CODEX_DEFAULT_MODEL", c.Model)
	c.BackendURL = strings.TrimRight(envString("OPENAI_VIA_CODEX_BACKEND_BASE_URL", c.BackendURL), "/")
	c.ClientVersion = envString("OPENAI_VIA_CODEX_CLIENT_VERSION", c.ClientVersion)
	c.AuthJSON = envString("OPENAI_VIA_CODEX_AUTH_JSON", c.AuthJSON)
	c.APIKey = strings.TrimSpace(envString("OPENAI_VIA_CODEX_API_KEY", c.APIKey))
	c.Timeout = time.Duration(envFloat("OPENAI_VIA_CODEX_TIMEOUT", c.Timeout.Seconds()) * float64(time.Second))
	c.MaxStored = envInt("OPENAI_VIA_CODEX_MAX_STORED_ITEMS", c.MaxStored)
	c.Concurrency = envInt("OPENAI_VIA_CODEX_MAX_CONCURRENT_REQUESTS", c.Concurrency)
	c.Verbose = envBool("OPENAI_VIA_CODEX_VERBOSE", c.Verbose)
	c.StateDir = envString("OPENAI_VIA_CODEX_STATE_DIR", c.StateDir)
	c.PIDFile = envString("OPENAI_VIA_CODEX_PID_FILE", c.PIDFile)
	c.LogFile = envString("OPENAI_VIA_CODEX_LOG_FILE", c.LogFile)
	c.StopTimeout = time.Duration(envFloat("OPENAI_VIA_CODEX_STOP_TIMEOUT", c.StopTimeout.Seconds()) * float64(time.Second))
	c.TelemetryEnabled = envBool("OPENAI_VIA_CODEX_TELEMETRY_ENABLED", c.TelemetryEnabled)
	c.TelemetryRetentionDays = envInt("OPENAI_VIA_CODEX_TELEMETRY_RETENTION_DAYS", c.TelemetryRetentionDays)
	c.TelemetryEventMemoryLimit = envInt("OPENAI_VIA_CODEX_TELEMETRY_EVENT_MEMORY_LIMIT", c.TelemetryEventMemoryLimit)
	c.TelemetryQueueSize = envInt("OPENAI_VIA_CODEX_TELEMETRY_QUEUE_SIZE", c.TelemetryQueueSize)
	c.DashboardEnabled = envBool("OPENAI_VIA_CODEX_DASHBOARD_ENABLED", c.DashboardEnabled)
	c.ReasoningSummaryDefault = strings.ToLower(envString("OPENAI_VIA_CODEX_REASONING_SUMMARY_DEFAULT", c.ReasoningSummaryDefault))
	c.CodexCollectorEnabled = envBool("OPENAI_VIA_CODEX_CODEX_COLLECTOR_ENABLED", c.CodexCollectorEnabled)
	c.CodexSessionsDir = envString("OPENAI_VIA_CODEX_CODEX_SESSIONS_DIR", c.CodexSessionsDir)
	c.CodexArchivedSessionsDir = envString("OPENAI_VIA_CODEX_CODEX_ARCHIVED_SESSIONS_DIR", c.CodexArchivedSessionsDir)
	c.CodexImportDays = envInt("OPENAI_VIA_CODEX_CODEX_IMPORT_DAYS", c.CodexImportDays)
	c.CodexScanInterval = time.Duration(envFloat("OPENAI_VIA_CODEX_CODEX_SCAN_INTERVAL", c.CodexScanInterval.Seconds()) * float64(time.Second))
	c.AntigravityEnabled = envBool("OPENAI_VIA_CODEX_ANTIGRAVITY_ENABLED", c.AntigravityEnabled)
	c.AntigravityOAuthProfile = strings.ToLower(strings.TrimSpace(envString("OPENAI_VIA_CODEX_ANTIGRAVITY_OAUTH_PROFILE", c.AntigravityOAuthProfile)))
	c.AntigravityCredentialPath = envString("OPENAI_VIA_CODEX_ANTIGRAVITY_CREDENTIAL_PATH", c.AntigravityCredentialPath)
	c.AntigravityEndpoint = strings.TrimRight(envString("OPENAI_VIA_CODEX_ANTIGRAVITY_ENDPOINT", c.AntigravityEndpoint), "/")
	c.AntigravityProject = strings.TrimSpace(envString("OPENAI_VIA_CODEX_ANTIGRAVITY_PROJECT", c.AntigravityProject))
	c.AntigravityCatalogTTL = time.Duration(envFloat("OPENAI_VIA_CODEX_ANTIGRAVITY_CATALOG_TTL", c.AntigravityCatalogTTL.Seconds()) * float64(time.Second))
	c.AntigravityProjectTTL = time.Duration(envFloat("OPENAI_VIA_CODEX_ANTIGRAVITY_PROJECT_TTL", c.AntigravityProjectTTL.Seconds()) * float64(time.Second))
}

func Run(args []string, version string) error {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(version)
		return nil
	}
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	if command == "config-generate" {
		return runConfigGenerate(args)
	}
	if command == "start" || command == "stop" || command == "status" {
		return runDaemonCommand(command, args)
	}
	if command != "serve" && command != "daemon-run" {
		return fmt.Errorf("unsupported command %q (supported: serve, start, stop, status, config-generate)", command)
	}
	cfg, configPath, err := loadResolvedConfig(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("openai-api-server-via-codex", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", configPath, "configuration file path")
	fs.StringVar(&cfg.Host, "host", cfg.Host, "server bind host")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "server bind port")
	fs.StringVar(&cfg.Model, "default-model", cfg.Model, "default model")
	fs.StringVar(&cfg.BackendURL, "backend-base-url", cfg.BackendURL, "Codex backend base URL")
	fs.StringVar(&cfg.ClientVersion, "client-version", cfg.ClientVersion, "client version header")
	fs.StringVar(&cfg.AuthJSON, "auth-json", cfg.AuthJSON, "Codex auth.json path")
	fs.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "incoming API key")
	timeout := cfg.Timeout.Seconds()
	fs.Float64Var(&timeout, "timeout", timeout, "backend timeout seconds")
	fs.IntVar(&cfg.MaxStored, "max-stored-items", cfg.MaxStored, "maximum in-memory stored items")
	fs.IntVar(&cfg.Concurrency, "max-concurrent-requests", cfg.Concurrency, "maximum Codex requests")
	fs.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "verbose logging")
	fs.BoolVar(&cfg.TelemetryEnabled, "telemetry-enabled", cfg.TelemetryEnabled, "enable request telemetry")
	fs.IntVar(&cfg.TelemetryRetentionDays, "telemetry-retention-days", cfg.TelemetryRetentionDays, "telemetry retention days")
	fs.IntVar(&cfg.TelemetryEventMemoryLimit, "telemetry-event-memory-limit", cfg.TelemetryEventMemoryLimit, "in-memory event timeline limit")
	fs.IntVar(&cfg.TelemetryQueueSize, "telemetry-queue-size", cfg.TelemetryQueueSize, "bounded telemetry writer queue size")
	fs.BoolVar(&cfg.DashboardEnabled, "dashboard-enabled", cfg.DashboardEnabled, "enable the local dashboard")
	fs.StringVar(&cfg.ReasoningSummaryDefault, "reasoning-summary-default", cfg.ReasoningSummaryDefault, "default reasoning summary: none or auto")
	fs.BoolVar(&cfg.CodexCollectorEnabled, "codex-collector-enabled", cfg.CodexCollectorEnabled, "enable read-only Codex rollout import")
	fs.StringVar(&cfg.CodexSessionsDir, "codex-sessions-dir", cfg.CodexSessionsDir, "Codex sessions directory")
	fs.StringVar(&cfg.CodexArchivedSessionsDir, "codex-archived-sessions-dir", cfg.CodexArchivedSessionsDir, "Codex archived sessions directory")
	fs.IntVar(&cfg.CodexImportDays, "codex-import-days", cfg.CodexImportDays, "days of Codex rollout history to import")
	codexScanInterval := cfg.CodexScanInterval.Seconds()
	fs.Float64Var(&codexScanInterval, "codex-scan-interval", codexScanInterval, "Codex rollout scan interval seconds")
	fs.BoolVar(&cfg.AntigravityEnabled, "antigravity-enabled", cfg.AntigravityEnabled, "enable the Direct OAuth Antigravity provider")
	fs.StringVar(&cfg.AntigravityOAuthProfile, "antigravity-oauth-profile", cfg.AntigravityOAuthProfile, "Antigravity OAuth profile: antigravity or custom")
	fs.StringVar(&cfg.AntigravityCredentialPath, "antigravity-credential-path", cfg.AntigravityCredentialPath, "Antigravity OAuth credential path")
	fs.StringVar(&cfg.AntigravityEndpoint, "antigravity-endpoint", cfg.AntigravityEndpoint, "Antigravity CloudCode endpoint")
	fs.StringVar(&cfg.AntigravityProject, "antigravity-project", cfg.AntigravityProject, "explicit verified Cloud AI Companion project")
	antigravityCatalogTTL := cfg.AntigravityCatalogTTL.Seconds()
	antigravityProjectTTL := cfg.AntigravityProjectTTL.Seconds()
	fs.Float64Var(&antigravityCatalogTTL, "antigravity-catalog-ttl", antigravityCatalogTTL, "Antigravity catalog cache TTL seconds")
	fs.Float64Var(&antigravityProjectTTL, "antigravity-project-ttl", antigravityProjectTTL, "Antigravity project cache TTL seconds")
	stopTimeout := cfg.StopTimeout.Seconds()
	if command == "daemon-run" {
		fs.Float64Var(&stopTimeout, "stop-timeout", stopTimeout, "seconds to wait before force kill")
	}
	var dropParams string
	fs.StringVar(&dropParams, "drop-params", "", "comma-separated downstream parameters to drop")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	minimumPort := 0
	if command == "daemon-run" {
		minimumPort = 1
	}
	cfg.ReasoningSummaryDefault = strings.ToLower(strings.TrimSpace(cfg.ReasoningSummaryDefault))
	if cfg.ReasoningSummaryDefault == "" {
		cfg.ReasoningSummaryDefault = "none"
	}
	if cfg.MaxStored < 0 || cfg.Concurrency < 0 || cfg.Port < minimumPort || cfg.Port > 65535 || timeout <= 0 || stopTimeout <= 0 ||
		cfg.TelemetryRetentionDays < 1 || cfg.TelemetryEventMemoryLimit < 1 || cfg.TelemetryQueueSize < 1 ||
		cfg.CodexImportDays < 1 || codexScanInterval < 5 ||
		(cfg.ReasoningSummaryDefault != "none" && cfg.ReasoningSummaryDefault != "auto") {
		return errors.New("port, timeout, stop-timeout, max-stored-items, max-concurrent-requests, telemetry settings, or reasoning summary default is invalid")
	}
	cfg.Timeout = time.Duration(timeout * float64(time.Second))
	cfg.StopTimeout = time.Duration(stopTimeout * float64(time.Second))
	cfg.CodexScanInterval = time.Duration(codexScanInterval * float64(time.Second))
	applyCodexDefaults(&cfg)
	if dropParams != "" {
		for _, value := range strings.Split(dropParams, ",") {
			if value = strings.TrimSpace(value); value != "" {
				cfg.DropParams = append(cfg.DropParams, value)
			}
		}
	}
	cfg.AntigravityOAuthProfile = strings.ToLower(strings.TrimSpace(cfg.AntigravityOAuthProfile))
	cfg.AntigravityCredentialPath = expandHome(cfg.AntigravityCredentialPath)
	cfg.AntigravityEndpoint = strings.TrimRight(strings.TrimSpace(cfg.AntigravityEndpoint), "/")
	cfg.AntigravityProject = strings.TrimSpace(cfg.AntigravityProject)
	cfg.AntigravityCatalogTTL = time.Duration(antigravityCatalogTTL * float64(time.Second))
	cfg.AntigravityProjectTTL = time.Duration(antigravityProjectTTL * float64(time.Second))
	if cfg.AntigravityOAuthProfile != "" && cfg.AntigravityOAuthProfile != "antigravity" && cfg.AntigravityOAuthProfile != "custom" {
		return errors.New("antigravity OAuth profile must be antigravity or custom")
	}
	if cfg.CodexCollectorEnabled && (cfg.CodexSessionsDir == "" || cfg.CodexArchivedSessionsDir == "" || cfg.CodexImportDays < 1 || cfg.CodexScanInterval < 5*time.Second) {
		return errors.New("Codex collector settings are invalid")
	}
	if cfg.AntigravityEnabled && (cfg.AntigravityCredentialPath == "" || cfg.AntigravityEndpoint == "" || antigravityCatalogTTL <= 0 || antigravityProjectTTL <= 0) {
		return errors.New("Antigravity provider settings are invalid")
	}
	if command == "daemon-run" {
		return runSupervised(cfg, version)
	}
	return serve(cfg, version)
}

func loadResolvedConfig(args []string) (config, string, error) {
	cfg := defaultConfig()
	configPath := configPathFromArgs(args)
	if configPath == "" {
		configPath = os.Getenv("OPENAI_VIA_CODEX_CONFIG")
	}
	if configPath == "" {
		home, _ := os.UserHomeDir()
		configPath = filepath.Join(home, ".config", "openai-api-server-via-codex", "config.toml")
	}
	if err := cfg.applyConfigFile(configPath); err != nil {
		return config{}, "", err
	}
	cfg.applyEnvironment()
	applyCodexDefaults(&cfg)
	return cfg, configPath, nil
}

func runConfigGenerate(args []string) error {
	fs := flag.NewFlagSet("config-generate", flag.ContinueOnError)
	stdout := fs.Bool("stdout", false, "print configuration")
	path := fs.String("config", "", "configuration path")
	force := fs.Bool("force", false, "overwrite")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	text := defaultConfigTOML()
	if *stdout {
		fmt.Print(text)
		return nil
	}
	if *path == "" {
		home, _ := os.UserHomeDir()
		*path = filepath.Join(home, ".config", "openai-api-server-via-codex", "config.toml")
	}
	if !*force {
		if _, err := os.Stat(*path); err == nil {
			return fmt.Errorf("configuration already exists at %s", *path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(*path), 0700); err != nil {
		return err
	}
	return os.WriteFile(*path, []byte(text), 0600)
}

func defaultConfigTOML() string {
	return fmt.Sprintf(`[server]
host = %q
port = %d
default_model = %q
timeout = 300.0
verbose = false
max_stored_items = %d
max_concurrent_requests = %d

[telemetry]
enabled = true
retention_days = %d
event_memory_limit = %d
queue_size = %d

[dashboard]
enabled = true

[reasoning]
summary_default = "none"

[codex]
auth_json = "~/.codex/auth.json"
backend_base_url = %q
client_version = %q
collector_enabled = true
sessions_dir = %q
archived_sessions_dir = %q
import_days = %d
scan_interval = %.1f

[antigravity]
enabled = false
oauth_profile = %q
credential_path = %q
endpoint = %q
project = ""
catalog_ttl = %.1f
project_ttl = %.1f

[compat]
drop_params = []

[daemon]
state_dir = %q
stop_timeout = 10.0
	`, defaultHost, defaultPort, defaultModel, defaultMaxStored, defaultConcurrency, defaultTelemetryRetentionDays, defaultTelemetryEventMemory, defaultTelemetryQueueSize, defaultBackendURL, defaultClient, defaultCodexSessionsDir(), defaultCodexArchivedSessionsDir(), defaultCodexImportDays, defaultCodexScanInterval.Seconds(), defaultAntigravityProfile, agyauth.DefaultCredentialPath(), defaultAntigravityEndpoint, defaultAntigravityCatalogTTL.Seconds(), defaultAntigravityProjectTTL.Seconds(), defaultStateDir())
}

func defaultCodexSessionsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "sessions")
}

func defaultCodexArchivedSessionsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "archived_sessions")
}

func applyCodexDefaults(cfg *config) {
	if cfg == nil {
		return
	}
	if cfg.CodexSessionsDir == "" {
		cfg.CodexSessionsDir = defaultCodexSessionsDir()
	}
	if cfg.CodexArchivedSessionsDir == "" {
		cfg.CodexArchivedSessionsDir = defaultCodexArchivedSessionsDir()
	}
	if cfg.CodexImportDays < 1 {
		cfg.CodexImportDays = defaultCodexImportDays
	}
	if cfg.CodexScanInterval <= 0 {
		cfg.CodexScanInterval = defaultCodexScanInterval
	}
}

func defaultStateDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "openai-api-server-via-codex", "run")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "openai-api-server-via-codex", "run")
}

func configPathFromArgs(args []string) string {
	for i, arg := range args {
		if arg == "--config" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--config=") {
			return strings.TrimPrefix(arg, "--config=")
		}
	}
	return ""
}

func (c *config) applyConfigFile(path string) error {
	data, err := os.ReadFile(expandHome(path))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var file configFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	for _, name := range file.Compat.DropParams {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("parse config %s: compat.drop_params must contain non-empty strings", path)
		}
	}
	file.apply(c)
	return nil
}

type configFile struct {
	Server struct {
		Host                  *string  `toml:"host"`
		Port                  *int     `toml:"port"`
		DefaultModel          *string  `toml:"default_model"`
		Timeout               *float64 `toml:"timeout"`
		Verbose               *bool    `toml:"verbose"`
		MaxStoredItems        *int     `toml:"max_stored_items"`
		MaxConcurrentRequests *int     `toml:"max_concurrent_requests"`
		APIKey                *string  `toml:"api_key"`
	} `toml:"server"`
	Telemetry struct {
		Enabled          *bool `toml:"enabled"`
		RetentionDays    *int  `toml:"retention_days"`
		EventMemoryLimit *int  `toml:"event_memory_limit"`
		QueueSize        *int  `toml:"queue_size"`
	} `toml:"telemetry"`
	Dashboard struct {
		Enabled *bool `toml:"enabled"`
	} `toml:"dashboard"`
	Reasoning struct {
		SummaryDefault *string `toml:"summary_default"`
	} `toml:"reasoning"`
	Codex struct {
		AuthJSON            *string  `toml:"auth_json"`
		BackendBaseURL      *string  `toml:"backend_base_url"`
		ClientVersion       *string  `toml:"client_version"`
		CollectorEnabled    *bool    `toml:"collector_enabled"`
		SessionsDir         *string  `toml:"sessions_dir"`
		ArchivedSessionsDir *string  `toml:"archived_sessions_dir"`
		ImportDays          *int     `toml:"import_days"`
		ScanInterval        *float64 `toml:"scan_interval"`
	} `toml:"codex"`
	Antigravity struct {
		Enabled        *bool    `toml:"enabled"`
		OAuthProfile   *string  `toml:"oauth_profile"`
		CredentialPath *string  `toml:"credential_path"`
		Endpoint       *string  `toml:"endpoint"`
		Project        *string  `toml:"project"`
		CatalogTTL     *float64 `toml:"catalog_ttl"`
		ProjectTTL     *float64 `toml:"project_ttl"`
	} `toml:"antigravity"`
	Compat struct {
		DropParams []string `toml:"drop_params"`
	} `toml:"compat"`
	Daemon struct {
		StateDir    *string  `toml:"state_dir"`
		PIDFile     *string  `toml:"pid_file"`
		LogFile     *string  `toml:"log_file"`
		StopTimeout *float64 `toml:"stop_timeout"`
	} `toml:"daemon"`
}

func (file *configFile) apply(c *config) {
	if file.Server.Host != nil {
		c.Host = *file.Server.Host
	}
	if file.Server.Port != nil {
		c.Port = *file.Server.Port
	}
	if file.Server.DefaultModel != nil {
		c.Model = *file.Server.DefaultModel
	}
	if file.Server.Timeout != nil {
		c.Timeout = time.Duration(*file.Server.Timeout * float64(time.Second))
	}
	if file.Server.Verbose != nil {
		c.Verbose = *file.Server.Verbose
	}
	if file.Server.MaxStoredItems != nil {
		c.MaxStored = *file.Server.MaxStoredItems
	}
	if file.Server.MaxConcurrentRequests != nil {
		c.Concurrency = *file.Server.MaxConcurrentRequests
	}
	if file.Server.APIKey != nil {
		c.APIKey = *file.Server.APIKey
	}
	if file.Codex.AuthJSON != nil {
		c.AuthJSON = *file.Codex.AuthJSON
	}
	if file.Codex.BackendBaseURL != nil {
		c.BackendURL = strings.TrimRight(*file.Codex.BackendBaseURL, "/")
	}
	if file.Codex.ClientVersion != nil {
		c.ClientVersion = *file.Codex.ClientVersion
	}
	if file.Codex.CollectorEnabled != nil {
		c.CodexCollectorEnabled = *file.Codex.CollectorEnabled
	}
	if file.Codex.SessionsDir != nil {
		c.CodexSessionsDir = *file.Codex.SessionsDir
	}
	if file.Codex.ArchivedSessionsDir != nil {
		c.CodexArchivedSessionsDir = *file.Codex.ArchivedSessionsDir
	}
	if file.Codex.ImportDays != nil {
		c.CodexImportDays = *file.Codex.ImportDays
	}
	if file.Codex.ScanInterval != nil {
		c.CodexScanInterval = time.Duration(*file.Codex.ScanInterval * float64(time.Second))
	}
	if file.Antigravity.Enabled != nil {
		c.AntigravityEnabled = *file.Antigravity.Enabled
	}
	if file.Antigravity.OAuthProfile != nil {
		c.AntigravityOAuthProfile = strings.ToLower(strings.TrimSpace(*file.Antigravity.OAuthProfile))
	}
	if file.Antigravity.CredentialPath != nil {
		c.AntigravityCredentialPath = *file.Antigravity.CredentialPath
	}
	if file.Antigravity.Endpoint != nil {
		c.AntigravityEndpoint = strings.TrimRight(*file.Antigravity.Endpoint, "/")
	}
	if file.Antigravity.Project != nil {
		c.AntigravityProject = strings.TrimSpace(*file.Antigravity.Project)
	}
	if file.Antigravity.CatalogTTL != nil {
		c.AntigravityCatalogTTL = time.Duration(*file.Antigravity.CatalogTTL * float64(time.Second))
	}
	if file.Antigravity.ProjectTTL != nil {
		c.AntigravityProjectTTL = time.Duration(*file.Antigravity.ProjectTTL * float64(time.Second))
	}
	if file.Compat.DropParams != nil {
		c.DropParams = append([]string(nil), file.Compat.DropParams...)
	}
	if file.Daemon.StateDir != nil {
		c.StateDir = *file.Daemon.StateDir
	}
	if file.Daemon.PIDFile != nil {
		c.PIDFile = *file.Daemon.PIDFile
	}
	if file.Daemon.LogFile != nil {
		c.LogFile = *file.Daemon.LogFile
	}
	if file.Daemon.StopTimeout != nil {
		c.StopTimeout = time.Duration(*file.Daemon.StopTimeout * float64(time.Second))
	}
	if file.Telemetry.Enabled != nil {
		c.TelemetryEnabled = *file.Telemetry.Enabled
	}
	if file.Telemetry.RetentionDays != nil {
		c.TelemetryRetentionDays = *file.Telemetry.RetentionDays
	}
	if file.Telemetry.EventMemoryLimit != nil {
		c.TelemetryEventMemoryLimit = *file.Telemetry.EventMemoryLimit
	}
	if file.Telemetry.QueueSize != nil {
		c.TelemetryQueueSize = *file.Telemetry.QueueSize
	}
	if file.Dashboard.Enabled != nil {
		c.DashboardEnabled = *file.Dashboard.Enabled
	}
	if file.Reasoning.SummaryDefault != nil {
		c.ReasoningSummaryDefault = strings.ToLower(*file.Reasoning.SummaryDefault)
	}
}

func envString(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func envInt(name string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return fallback
}
func envFloat(name string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return v
	}
	return fallback
}
func envBool(name string, fallback bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(name)); err == nil {
		return v
	}
	return fallback
}
