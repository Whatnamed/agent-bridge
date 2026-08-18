package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/whatnamed/agent-bridge/agy/internal/auth"
	"github.com/whatnamed/agent-bridge/agy/internal/cloudcode"
	"github.com/whatnamed/agent-bridge/agy/internal/probe"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "auth":
		err = runAuth(os.Args[2:])
	case "auth-status":
		err = runAuthStatus(os.Args[2:])
	case "models":
		err = runModels(os.Args[2:])
	case "probe":
		var result probe.Result
		result, err = runProbe(os.Args[2:])
		if result.Status != "" {
			printJSON(result)
		}
	case "logout":
		err = runLogout(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agy-oauth-poc:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("agy-oauth-poc - isolated Google OAuth and CloudCode compatibility probe")
	fmt.Println()
	fmt.Println("commands:")
	fmt.Println("  auth          start a new browser OAuth flow and save only this POC credential")
	fmt.Println("  auth-status   show redacted local credential status")
	fmt.Println("  models        list CloudCode models without a generation request")
	fmt.Println("  probe         run the explicit compat|minimal streaming probe")
	fmt.Println("  logout        delete only this POC credential file")
	fmt.Println()
	fmt.Println("OAuth profiles for auth/models/probe:")
	fmt.Println("  --oauth-profile antigravity  current direct-OAuth desktop-client profile (default)")
	fmt.Println("  --oauth-profile custom       read AGY_POC_CLIENT_ID/SECRET from the environment")
}

func runAuth(args []string) error {
	flags := flag.NewFlagSet("auth", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	noBrowser := flags.Bool("no-browser", false, "print the URL and read a redirect URL from stdin")
	verify := flags.Bool("verify", false, "call loadCodeAssist after saving; no generation request")
	timeout := flags.Duration("timeout", 2*time.Minute, "OAuth callback timeout")
	redirectURI := flags.String("redirect-uri", auth.DefaultRedirectURI, "loopback redirect URI registered for this OAuth client")
	credentialPath := flags.String("credential-path", auth.DefaultCredentialPath(), "isolated POC credential path")
	oauthProfile := flags.String("oauth-profile", string(auth.DefaultProfile), "OAuth profile: antigravity or custom")
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, err := auth.ConfigForProfile(*oauthProfile)
	if err != nil {
		return err
	}
	config.RedirectURI = *redirectURI
	callback, err := auth.ListenCallback(config.RedirectURI)
	if err != nil {
		return err
	}
	defer callback.Close()
	state, err := auth.GenerateState()
	if err != nil {
		return err
	}
	verifier, challenge, err := auth.GeneratePKCEVerifier()
	if err != nil {
		return err
	}
	authorizationURL, err := auth.BuildAuthorizationURL(config, state, challenge)
	if err != nil {
		return err
	}
	fmt.Println("请在浏览器中完成新的、独立的 Google OAuth 授权。")
	fmt.Println("授权 URL:")
	fmt.Println(authorizationURL)

	var callbackResult auth.CallbackResult
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *noBrowser {
		fmt.Println("完成授权后，将地址栏中的完整回调 URL 粘贴到这里：")
		line, readErr := readLine()
		if readErr != nil {
			return readErr
		}
		callbackResult, err = parseCallbackInput(line)
	} else {
		if err := openBrowser(authorizationURL); err != nil {
			fmt.Fprintln(os.Stderr, "无法自动打开浏览器，请手动打开上面的 URL：", err)
		}
		callbackResult, err = callback.Wait(ctx)
	}
	if err != nil {
		return err
	}
	if err := auth.ValidateState(state, callbackResult.State); err != nil {
		return err
	}
	token, err := auth.ExchangeCode(ctx, config, callbackResult.Code, verifier)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		return errors.New("OAuth did not return a refresh token; re-run auth with consent")
	}
	credentials := auth.CredentialsFromToken(token, config.ClientID)
	if err := auth.SaveCredentials(*credentialPath, credentials); err != nil {
		return err
	}
	fmt.Println("OAuth 完成。")
	fmt.Println("仅保存到独立 POC 凭据路径：", *credentialPath)
	fmt.Println("access token: present; refresh token: present; token contents were not printed")

	if *verify {
		manager := &auth.TokenManager{Path: *credentialPath, Config: config}
		client := cloudcode.NewClient(cloudcode.DefaultEndpoint, manager)
		if _, err := client.LoadCodeAssist(ctx); err != nil {
			return fmt.Errorf("OAuth saved, but loadCodeAssist verification failed: %w", err)
		}
		fmt.Println("loadCodeAssist: OK")
	}
	return nil
}

func runAuthStatus(args []string) error {
	flags := flag.NewFlagSet("auth-status", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	credentialPath := flags.String("credential-path", auth.DefaultCredentialPath(), "isolated POC credential path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	credentials, err := auth.LoadCredentials(*credentialPath)
	if err != nil {
		return err
	}
	status := map[string]any{
		"path":                   *credentialPath,
		"has_access_token":       credentials.AccessToken != "",
		"has_refresh_token":      credentials.RefreshToken != "",
		"expiry_utc":             credentials.Expiry().UTC().Format(time.RFC3339),
		"expired_or_near_expiry": !credentials.AccessTokenUsable(time.Now()),
		"client_id_present":      credentials.ClientID != "",
	}
	printJSON(status)
	return nil
}

func runModels(args []string) error {
	flags := flag.NewFlagSet("models", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	endpoint := flags.String("endpoint", cloudcode.DefaultEndpoint, "CloudCode endpoint")
	credentialPath := flags.String("credential-path", auth.DefaultCredentialPath(), "isolated POC credential path")
	oauthProfile := flags.String("oauth-profile", string(auth.DefaultProfile), "OAuth profile: antigravity or custom")
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, err := auth.ConfigForProfile(*oauthProfile)
	if err != nil {
		return err
	}
	manager := &auth.TokenManager{Path: *credentialPath, Config: config}
	client := cloudcode.NewClient(*endpoint, manager)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := client.FetchAvailableModels(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(models.Models))
	for id := range models.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	type modelInfo struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name,omitempty"`
	}
	out := struct {
		Endpoint            string      `json:"endpoint"`
		DefaultAgentModelID string      `json:"default_agent_model_id,omitempty"`
		Models              []modelInfo `json:"models"`
	}{Endpoint: client.Endpoint, DefaultAgentModelID: models.DefaultAgentModelID}
	for _, id := range ids {
		out.Models = append(out.Models, modelInfo{ID: id, DisplayName: models.Models[id].DisplayName})
	}
	printJSON(out)
	return nil
}

type probeCommandOptions struct {
	Mode            cloudcode.Mode
	Model           string
	Project         string
	Prompt          string
	PromptSpecified bool
	Endpoint        string
	ToolTest        bool
	Timeout         time.Duration
	CredentialPath  string
	OAuthProfile    string
}

func parseProbeArgs(args []string) (probeCommandOptions, error) {
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	mode := flags.String("mode", string(cloudcode.ModeCompat), "compat or minimal")
	model := flags.String("model", probe.DefaultModel, "requested CloudCode model id")
	project := flags.String("project", "", "explicit verified Cloud AI Companion project id")
	prompt := flags.String("prompt", "", "probe prompt; it is not written to a log")
	endpoint := flags.String("endpoint", cloudcode.DefaultEndpoint, "CloudCode endpoint")
	toolTest := flags.Bool("tool-test", false, "enable only the safe get_test_value function-call round trip")
	timeout := flags.Duration("timeout", 90*time.Second, "probe timeout")
	credentialPath := flags.String("credential-path", auth.DefaultCredentialPath(), "isolated POC credential path")
	oauthProfile := flags.String("oauth-profile", string(auth.DefaultProfile), "OAuth profile: antigravity or custom")
	if err := flags.Parse(args); err != nil {
		return probeCommandOptions{}, err
	}
	promptSpecified := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == "prompt" {
			promptSpecified = true
		}
	})
	effectivePrompt := *prompt
	if !promptSpecified {
		if *toolTest {
			effectivePrompt = probe.DefaultToolTestPrompt
		} else {
			effectivePrompt = probe.DefaultPrompt
		}
	}
	return probeCommandOptions{
		Mode:            cloudcode.Mode(*mode),
		Model:           *model,
		Project:         *project,
		Prompt:          effectivePrompt,
		PromptSpecified: promptSpecified,
		Endpoint:        *endpoint,
		ToolTest:        *toolTest,
		Timeout:         *timeout,
		CredentialPath:  *credentialPath,
		OAuthProfile:    *oauthProfile,
	}, nil
}

func runProbe(args []string) (probe.Result, error) {
	options, err := parseProbeArgs(args)
	if err != nil {
		return probe.Result{}, err
	}
	config, err := auth.ConfigForProfile(options.OAuthProfile)
	if err != nil {
		return probe.Result{}, err
	}
	manager := &auth.TokenManager{Path: options.CredentialPath, Config: config}
	client := cloudcode.NewClient(options.Endpoint, manager)
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	result, err := probe.Run(ctx, client, probe.Config{
		Mode:            options.Mode,
		RequestedModel:  options.Model,
		Project:         options.Project,
		Prompt:          options.Prompt,
		PromptSpecified: options.PromptSpecified,
		ToolTest:        options.ToolTest,
	})
	return result, err
}

func runLogout(args []string) error {
	flags := flag.NewFlagSet("logout", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	credentialPath := flags.String("credential-path", auth.DefaultCredentialPath(), "isolated POC credential path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := auth.DeleteCredentials(*credentialPath); err != nil {
		return err
	}
	fmt.Println("已删除本 POC 自己的凭据文件：", *credentialPath)
	return nil
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		command = exec.Command("open", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func readLine() (string, error) {
	var line string
	if _, err := fmt.Scanln(&line); err != nil {
		return "", fmt.Errorf("read OAuth callback URL: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func parseCallbackInput(input string) (auth.CallbackResult, error) {
	parsed, err := url.Parse(strings.TrimSpace(input))
	if err != nil {
		return auth.CallbackResult{}, errors.New("invalid OAuth callback URL")
	}
	if parsed.Query().Get("code") == "" || parsed.Query().Get("state") == "" {
		return auth.CallbackResult{}, errors.New("OAuth callback URL must contain code and state")
	}
	return auth.CallbackResult{Code: parsed.Query().Get("code"), State: parsed.Query().Get("state")}, nil
}

func printJSON(value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode result:", err)
		return
	}
	fmt.Println(string(data))
}
