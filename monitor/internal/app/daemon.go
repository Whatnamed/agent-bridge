package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	initialRestartDelay = time.Second
	maxRestartDelay     = 30 * time.Second
	healthyRunDuration  = 30 * time.Second
)

type daemonPaths struct{ StateDir, PIDFile, LogFile string }

func runDaemonCommand(command string, args []string) error {
	cfg, configPath, err := loadResolvedConfig(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("openai-api-server-via-codex "+command, flag.ContinueOnError)
	fs.StringVar(&configPath, "config", configPath, "configuration file path")
	fs.StringVar(&cfg.Host, "host", cfg.Host, "server host")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "server port")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "daemon state directory")
	fs.StringVar(&cfg.PIDFile, "pid-file", cfg.PIDFile, "explicit PID file")
	fs.StringVar(&cfg.LogFile, "log-file", cfg.LogFile, "explicit log file")
	fs.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "verbose logging")
	stopSeconds := cfg.StopTimeout.Seconds()
	fs.Float64Var(&stopSeconds, "stop-timeout", stopSeconds, "seconds to wait before force kill")
	var serverFlags *daemonServerFlags
	if command == "start" {
		serverFlags = addDaemonServerFlags(fs, &cfg)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if serverFlags != nil {
		cfg.Timeout = time.Duration(*serverFlags.timeout * float64(time.Second))
		cfg.CodexScanInterval = time.Duration(*serverFlags.codexScanInterval * float64(time.Second))
		cfg.AntigravityCatalogTTL = time.Duration(*serverFlags.antigravityCatalogTTL * float64(time.Second))
		cfg.AntigravityProjectTTL = time.Duration(*serverFlags.antigravityProjectTTL * float64(time.Second))
	}
	cfg.AntigravityOAuthProfile = strings.ToLower(strings.TrimSpace(cfg.AntigravityOAuthProfile))
	cfg.AntigravityCredentialPath = expandHome(cfg.AntigravityCredentialPath)
	cfg.AntigravityEndpoint = strings.TrimRight(strings.TrimSpace(cfg.AntigravityEndpoint), "/")
	cfg.AntigravityProject = strings.TrimSpace(cfg.AntigravityProject)
	if cfg.Port < 1 || cfg.Port > 65535 || stopSeconds <= 0 || cfg.Timeout <= 0 || cfg.MaxStored < 0 || cfg.Concurrency < 0 ||
		cfg.CodexImportDays < 1 || cfg.CodexScanInterval < 5*time.Second {
		return errors.New("port, timeout, storage, concurrency, or stop-timeout is invalid")
	}
	if cfg.CodexCollectorEnabled && (cfg.CodexSessionsDir == "" || cfg.CodexArchivedSessionsDir == "") {
		return errors.New("Codex collector settings are invalid")
	}
	if cfg.AntigravityOAuthProfile != "" && cfg.AntigravityOAuthProfile != "antigravity" && cfg.AntigravityOAuthProfile != "custom" {
		return errors.New("antigravity OAuth profile must be antigravity or custom")
	}
	if cfg.AntigravityEnabled && (cfg.AntigravityCredentialPath == "" || cfg.AntigravityEndpoint == "" || cfg.AntigravityCatalogTTL <= 0 || cfg.AntigravityProjectTTL <= 0) {
		return errors.New("Antigravity provider settings are invalid")
	}
	cfg.StopTimeout = time.Duration(stopSeconds * float64(time.Second))
	paths, err := resolveDaemonPaths(cfg, flagProvided(args, "host"), flagProvided(args, "pid-file"))
	if err != nil {
		return err
	}
	switch command {
	case "start":
		return startDaemon(cfg, paths)
	case "stop":
		return stopDaemon(paths, cfg.StopTimeout)
	case "status":
		return statusDaemon(paths)
	default:
		return fmt.Errorf("unsupported daemon command %q", command)
	}
}

type daemonServerFlags struct {
	timeout               *float64
	codexScanInterval     *float64
	antigravityCatalogTTL *float64
	antigravityProjectTTL *float64
}

func addDaemonServerFlags(fs *flag.FlagSet, cfg *config) *daemonServerFlags {
	fs.StringVar(&cfg.Model, "default-model", cfg.Model, "default model")
	fs.StringVar(&cfg.BackendURL, "backend-base-url", cfg.BackendURL, "Codex backend base URL")
	fs.StringVar(&cfg.ClientVersion, "client-version", cfg.ClientVersion, "client version header")
	fs.StringVar(&cfg.AuthJSON, "auth-json", cfg.AuthJSON, "Codex auth.json path")
	fs.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "incoming API key")
	timeout := cfg.Timeout.Seconds()
	fs.Float64Var(&timeout, "timeout", timeout, "backend timeout seconds")
	fs.IntVar(&cfg.MaxStored, "max-stored-items", cfg.MaxStored, "maximum in-memory stored items")
	fs.IntVar(&cfg.Concurrency, "max-concurrent-requests", cfg.Concurrency, "maximum Codex requests")
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
	return &daemonServerFlags{
		timeout: &timeout, codexScanInterval: &codexScanInterval,
		antigravityCatalogTTL: &antigravityCatalogTTL, antigravityProjectTTL: &antigravityProjectTTL,
	}
}

func flagProvided(args []string, name string) bool {
	prefix := "--" + name
	for _, arg := range args {
		if arg == prefix || strings.HasPrefix(arg, prefix+"=") {
			return true
		}
	}
	return false
}

func resolveDaemonPaths(cfg config, hostExplicit, pidExplicit bool) (daemonPaths, error) {
	state, err := filepath.Abs(expandHome(cfg.StateDir))
	if err != nil {
		return daemonPaths{}, err
	}
	stem := "server-" + sanitizePathPart(cfg.Host) + "-" + strconv.Itoa(cfg.Port)
	pid := cfg.PIDFile
	if pid == "" {
		pid = filepath.Join(state, stem+".pid")
	}
	pid, err = filepath.Abs(expandHome(pid))
	if err != nil {
		return daemonPaths{}, err
	}
	logFile := cfg.LogFile
	if logFile == "" {
		logFile = filepath.Join(state, stem+".log")
	}
	logFile, err = filepath.Abs(expandHome(logFile))
	if err != nil {
		return daemonPaths{}, err
	}
	if !hostExplicit && !pidExplicit {
		if _, statErr := os.Stat(pid); os.IsNotExist(statErr) {
			matches, _ := filepath.Glob(filepath.Join(state, "server-*-"+strconv.Itoa(cfg.Port)+".pid"))
			if len(matches) == 1 {
				pid, _ = filepath.Abs(matches[0])
				if cfg.LogFile == "" {
					logFile = strings.TrimSuffix(pid, ".pid") + ".log"
				}
			} else if len(matches) > 1 {
				return daemonPaths{}, fmt.Errorf("multiple PID files match port %d; specify --host or --pid-file: %s", cfg.Port, strings.Join(matches, ", "))
			}
		}
	}
	return daemonPaths{StateDir: state, PIDFile: pid, LogFile: logFile}, nil
}

func sanitizePathPart(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-", r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func startDaemon(cfg config, paths daemonPaths) error {
	if _, err := newBackend(cfg).auth.borrow(); err != nil {
		return preflightAuthError(err)
	}
	if cfg.AntigravityEnabled {
		if _, err := newAntigravityProvider(cfg); err != nil {
			return err
		}
	}
	if pid := readPID(paths.PIDFile); pid > 0 {
		if processAlive(pid) {
			return fmt.Errorf("already running with PID %d (%s)", pid, paths.PIDFile)
		}
		_ = os.Remove(paths.PIDFile)
	}
	for _, dir := range []string{paths.StateDir, filepath.Dir(paths.PIDFile), filepath.Dir(paths.LogFile)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	logFile, err := os.OpenFile(paths.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	childArgs := serverCommandArgs("daemon-run", cfg)
	child := exec.Command(executable, childArgs...)
	child.Stdin = nil
	child.Stdout = logFile
	child.Stderr = logFile
	child.Env = os.Environ()
	if cfg.APIKey != "" {
		child.Env = append(child.Env, "OPENAI_VIA_CODEX_API_KEY="+cfg.APIKey)
	}
	configureDaemonProcess(child)
	if err := child.Start(); err != nil {
		return err
	}
	if err := writePID(paths.PIDFile, child.Process.Pid); err != nil {
		_ = forceKillProcess(child.Process.Pid)
		return err
	}
	fmt.Printf("Started openai-api-server-via-codex on %s:%d\nPID: %d\nPID file: %s\nLog file: %s\n", cfg.Host, cfg.Port, child.Process.Pid, paths.PIDFile, paths.LogFile)
	return nil
}

func serverCommandArgs(command string, cfg config) []string {
	args := []string{
		command,
		"--host", cfg.Host,
		"--port", strconv.Itoa(cfg.Port),
		"--backend-base-url", cfg.BackendURL,
		"--client-version", cfg.ClientVersion,
		"--auth-json", cfg.AuthJSON,
		"--timeout", formatSeconds(cfg.Timeout),
		"--max-stored-items", strconv.Itoa(cfg.MaxStored),
		"--max-concurrent-requests", strconv.Itoa(cfg.Concurrency),
		"--default-model", cfg.Model,
		"--telemetry-enabled", strconv.FormatBool(cfg.TelemetryEnabled),
		"--telemetry-retention-days", strconv.Itoa(cfg.TelemetryRetentionDays),
		"--telemetry-event-memory-limit", strconv.Itoa(cfg.TelemetryEventMemoryLimit),
		"--telemetry-queue-size", strconv.Itoa(cfg.TelemetryQueueSize),
		"--dashboard-enabled", strconv.FormatBool(cfg.DashboardEnabled),
		"--reasoning-summary-default", cfg.ReasoningSummaryDefault,
		"--codex-collector-enabled", strconv.FormatBool(cfg.CodexCollectorEnabled),
		"--codex-sessions-dir", cfg.CodexSessionsDir,
		"--codex-archived-sessions-dir", cfg.CodexArchivedSessionsDir,
		"--codex-import-days", strconv.Itoa(cfg.CodexImportDays),
		"--codex-scan-interval", formatSeconds(cfg.CodexScanInterval),
		"--antigravity-enabled", strconv.FormatBool(cfg.AntigravityEnabled),
		"--antigravity-oauth-profile", cfg.AntigravityOAuthProfile,
		"--antigravity-credential-path", cfg.AntigravityCredentialPath,
		"--antigravity-endpoint", cfg.AntigravityEndpoint,
		"--antigravity-project", cfg.AntigravityProject,
		"--antigravity-catalog-ttl", formatSeconds(cfg.AntigravityCatalogTTL),
		"--antigravity-project-ttl", formatSeconds(cfg.AntigravityProjectTTL),
	}
	if command == "daemon-run" {
		args = append(args, "--stop-timeout", formatSeconds(cfg.StopTimeout))
	}
	if cfg.Verbose {
		args = append(args, "--verbose")
	}
	if len(cfg.DropParams) > 0 {
		args = append(args, "--drop-params", strings.Join(cfg.DropParams, ","))
	}
	return args
}

func newSupervisedServerCommand(executable string, cfg config) *exec.Cmd {
	child := exec.Command(executable, serverCommandArgs("serve", cfg)...)
	child.Stdin = nil
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Env = os.Environ()
	// The supervisor itself is detached on Windows, but its foreground
	// serve child must be detached as well. Otherwise Windows may create a
	// console host (and, with Windows Terminal configured as the default,
	// a visible Terminal tab) for the child even though the tray launcher
	// and supervisor were started without a window.
	configureDaemonProcess(child)
	return child
}

func runSupervised(cfg config, version string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	restartDelay := initialRestartDelay
	for {
		child := newSupervisedServerCommand(executable, cfg)
		fmt.Fprintf(os.Stderr, "daemon supervisor starting server %s\n", version)
		if err := child.Start(); err != nil {
			return err
		}
		started := time.Now()
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		select {
		case <-signals:
			_ = terminateProcess(child.Process.Pid)
			select {
			case <-done:
				return nil
			case <-time.After(cfg.StopTimeout):
				_ = forceKillProcess(child.Process.Pid)
				<-done
				return nil
			}
		case childErr := <-done:
			runDuration := time.Since(started)
			if runDuration >= healthyRunDuration {
				restartDelay = initialRestartDelay
			}
			delay := restartDelay
			restartDelay = nextRestartDelay(restartDelay, runDuration)
			fmt.Fprintf(os.Stderr, "daemon supervisor server exited (%v); restarting in %s\n", childErr, delay)
			timer := time.NewTimer(delay)
			select {
			case <-signals:
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
}

func nextRestartDelay(current, runtime time.Duration) time.Duration {
	if runtime >= healthyRunDuration {
		return initialRestartDelay
	}
	next := current * 2
	if next > maxRestartDelay {
		return maxRestartDelay
	}
	return next
}

func stopDaemon(paths daemonPaths, timeout time.Duration) error {
	pid := readPID(paths.PIDFile)
	if pid == 0 {
		fmt.Printf("Not running. PID file: %s\n", paths.PIDFile)
		return nil
	}
	if !processAlive(pid) {
		_ = removeMatchingPID(paths.PIDFile, pid)
		fmt.Printf("Removed stale PID %d. PID file: %s\n", pid, paths.PIDFile)
		return nil
	}
	if err := terminateProcess(pid); err != nil {
		return err
	}
	if waitProcessExit(pid, timeout) {
		_ = removeMatchingPID(paths.PIDFile, pid)
		fmt.Printf("Stopped PID %d (stopped).\n", pid)
		return nil
	}
	if err := forceKillProcess(pid); err != nil {
		return err
	}
	_ = waitProcessExit(pid, 5*time.Second)
	_ = removeMatchingPID(paths.PIDFile, pid)
	fmt.Printf("Stopped PID %d (killed).\n", pid)
	return nil
}

func statusDaemon(paths daemonPaths) error {
	pid := readPID(paths.PIDFile)
	if pid == 0 {
		fmt.Printf("stopped. PID file: %s\n", paths.PIDFile)
		return nil
	}
	state := "stale"
	if processAlive(pid) {
		state = "running"
	}
	fmt.Printf("%s: PID %d\nPID file: %s\nLog file: %s\n", state, pid, paths.PIDFile, paths.LogFile)
	return nil
}
func readPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid < 1 {
		return 0
	}
	return pid
}
func writePID(path string, pid int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func removeMatchingPID(path string, pid int) error {
	if readPID(path) == pid {
		return os.Remove(path)
	}
	return nil
}
func waitProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !processAlive(pid)
}
func formatSeconds(value time.Duration) string {
	return strconv.FormatFloat(value.Seconds(), 'f', -1, 64)
}
