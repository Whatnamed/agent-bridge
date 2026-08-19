package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultAuthorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultTokenEndpoint         = "https://oauth2.googleapis.com/token"
	DefaultRedirectURI           = "http://localhost:51121/oauth-callback"
)

type Profile string

const (
	ProfileAntigravity Profile = "antigravity"
	ProfileCustom      Profile = "custom"
	DefaultProfile     Profile = ProfileAntigravity
)

// These are the public desktop-client values used by the current Antigravity
// direct-OAuth implementations. They are configuration, not user credentials:
// the POC still creates and stores its own token set after a new PKCE flow.
const (
	antigravityClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityClientSecret = "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"
)

var DefaultScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// Config is the independent OAuth client configuration for the POC.
// ClientSecret is intentionally supplied at runtime and never persisted.
type Config struct {
	Profile               Profile
	ClientID              string
	ClientSecret          string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RedirectURI           string
	Scopes                []string
	HTTPClient            *http.Client
}

// ConfigForProfile returns an explicit OAuth profile. The antigravity profile
// intentionally does not inspect the installed AGY CLI or its credential store.
func ConfigForProfile(profile string) (Config, error) {
	switch Profile(strings.ToLower(strings.TrimSpace(profile))) {
	case ProfileAntigravity:
		return profileConfig(ProfileAntigravity, antigravityClientID, antigravityClientSecret), nil
	case ProfileCustom:
		clientID := strings.TrimSpace(os.Getenv("AGY_POC_CLIENT_ID"))
		if clientID == "" {
			return Config{}, errors.New("AGY_POC_CLIENT_ID is not set for the custom OAuth profile")
		}
		return profileConfig(ProfileCustom, clientID, os.Getenv("AGY_POC_CLIENT_SECRET")), nil
	default:
		return Config{}, fmt.Errorf("unknown OAuth profile %q; choose antigravity or custom", profile)
	}
}

func profileConfig(profile Profile, clientID, clientSecret string) Config {
	return Config{
		Profile:               profile,
		ClientID:              clientID,
		ClientSecret:          clientSecret,
		AuthorizationEndpoint: DefaultAuthorizationEndpoint,
		TokenEndpoint:         DefaultTokenEndpoint,
		RedirectURI:           DefaultRedirectURI,
		Scopes:                append([]string(nil), DefaultScopes...),
		HTTPClient:            http.DefaultClient,
	}
}

// ConfigFromEnvironment is retained for package callers that explicitly want
// the custom environment-backed profile. The CLI defaults to antigravity and
// calls ConfigForProfile directly.
func ConfigFromEnvironment() (Config, error) {
	return ConfigForProfile(string(ProfileCustom))
}

func (c Config) withDefaults() Config {
	if c.AuthorizationEndpoint == "" {
		c.AuthorizationEndpoint = DefaultAuthorizationEndpoint
	}
	if c.TokenEndpoint == "" {
		c.TokenEndpoint = DefaultTokenEndpoint
	}
	if c.RedirectURI == "" {
		c.RedirectURI = DefaultRedirectURI
	}
	if len(c.Scopes) == 0 {
		c.Scopes = append([]string(nil), DefaultScopes...)
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	return c
}

func BuildAuthorizationURL(c Config, state, codeChallenge string) (string, error) {
	c = c.withDefaults()
	if strings.TrimSpace(c.ClientID) == "" {
		return "", errors.New("missing OAuth client id")
	}
	if strings.TrimSpace(state) == "" || strings.TrimSpace(codeChallenge) == "" {
		return "", errors.New("OAuth state and PKCE challenge are required")
	}
	if len(c.Scopes) == 0 {
		return "", errors.New("no OAuth scopes configured")
	}

	u, err := url.Parse(c.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("invalid authorization endpoint: %w", err)
	}
	query := u.Query()
	query.Set("client_id", c.ClientID)
	query.Set("redirect_uri", c.RedirectURI)
	query.Set("response_type", "code")
	query.Set("scope", strings.Join(c.Scopes, " "))
	query.Set("access_type", "offline")
	query.Set("prompt", "consent")
	query.Set("include_granted_scopes", "true")
	query.Set("state", state)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func GenerateState() (string, error) {
	return randomURLString(24)
}

func GeneratePKCEVerifier() (verifier, challenge string, err error) {
	verifier, err = randomURLString(48)
	if err != nil {
		return "", "", err
	}
	hash := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(hash[:]), nil
}

func randomURLString(size int) (string, error) {
	if size <= 0 {
		return "", errors.New("random string size must be positive")
	}
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

type CallbackResult struct {
	Code  string
	State string
}

// CallbackServer binds only to loopback and accepts one OAuth callback.
type CallbackServer struct {
	server   *http.Server
	listener net.Listener
	result   chan CallbackResult
	errCh    chan error
	closeMu  sync.Once
}

func ListenCallback(redirectURI string) (*CallbackServer, error) {
	host, port, path, err := parseRedirectURI(redirectURI)
	if err != nil {
		return nil, err
	}
	// The redirect URI uses localhost for Google client registration, but the
	// listener is deliberately loopback-only and does not bind a wildcard host.
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return nil, fmt.Errorf("listen on OAuth callback %s:%s: %w", host, port, err)
	}

	callback := &CallbackServer{
		listener: listener,
		result:   make(chan CallbackResult, 1),
		errCh:    make(chan error, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if oauthErr := strings.TrimSpace(query.Get("error")); oauthErr != "" {
			writeCallbackHTML(w, http.StatusBadRequest, "Authentication failed", "Google did not grant access.")
			sendCallbackError(callback.errCh, fmt.Errorf("Google OAuth returned %s", oauthErr))
			return
		}
		code := strings.TrimSpace(query.Get("code"))
		if code == "" {
			writeCallbackHTML(w, http.StatusBadRequest, "Authentication failed", "No authorization code was received.")
			sendCallbackError(callback.errCh, errors.New("OAuth callback did not contain an authorization code"))
			return
		}
		writeCallbackHTML(w, http.StatusOK, "Authentication successful", "You can close this browser tab and return to the terminal.")
		select {
		case callback.result <- CallbackResult{Code: code, State: query.Get("state")}:
		default:
		}
	})
	callback.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if serveErr := callback.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			sendCallbackError(callback.errCh, serveErr)
		}
	}()
	return callback, nil
}

func (s *CallbackServer) Wait(ctx context.Context) (CallbackResult, error) {
	if s == nil {
		return CallbackResult{}, errors.New("OAuth callback server is nil")
	}
	select {
	case result := <-s.result:
		return result, nil
	case err := <-s.errCh:
		return CallbackResult{}, err
	case <-ctx.Done():
		return CallbackResult{}, ctx.Err()
	}
}

func (s *CallbackServer) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeMu.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if s.server != nil {
			closeErr = s.server.Shutdown(ctx)
		}
		if s.listener != nil {
			if err := s.listener.Close(); closeErr == nil && err != nil && !errors.Is(err, net.ErrClosed) {
				closeErr = err
			}
		}
	})
	return closeErr
}

func ValidateState(expected, received string) error {
	if expected == "" || received == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(received)) != 1 {
		return errors.New("OAuth state validation failed")
	}
	return nil
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

func ExchangeCode(ctx context.Context, c Config, code, verifier string) (TokenResponse, error) {
	c = c.withDefaults()
	if strings.TrimSpace(code) == "" || strings.TrimSpace(verifier) == "" {
		return TokenResponse{}, errors.New("OAuth authorization code and PKCE verifier are required")
	}
	form := url.Values{
		"client_id":     {c.ClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {c.RedirectURI},
	}
	if c.ClientSecret != "" {
		form.Set("client_secret", c.ClientSecret)
	}
	return exchangeToken(ctx, c, form)
}

func RefreshAccessToken(ctx context.Context, c Config, refreshToken string) (TokenResponse, error) {
	c = c.withDefaults()
	if strings.TrimSpace(refreshToken) == "" {
		return TokenResponse{}, errors.New("refresh token is empty")
	}
	form := url.Values{
		"client_id":     {c.ClientID},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	if c.ClientSecret != "" {
		form.Set("client_secret", c.ClientSecret)
	}
	return exchangeToken(ctx, c, form)
}

func exchangeToken(ctx context.Context, c Config, form url.Values) (TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, fmt.Errorf("create OAuth token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("OAuth token request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return TokenResponse{}, fmt.Errorf("OAuth token endpoint returned HTTP %d", resp.StatusCode)
	}
	var token TokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&token); err != nil {
		return TokenResponse{}, fmt.Errorf("decode OAuth token response: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return TokenResponse{}, errors.New("OAuth token response did not contain an access token")
	}
	return token, nil
}

type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiryDate   int64  `json:"expiry_date"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
}

func CredentialsFromToken(token TokenResponse, clientID string) Credentials {
	return Credentials{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiryDate:   time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UnixMilli(),
		TokenType:    token.TokenType,
		Scope:        token.Scope,
		ClientID:     clientID,
	}
}

func (c Credentials) Expiry() time.Time {
	if c.ExpiryDate <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(c.ExpiryDate)
}

func (c Credentials) AccessTokenUsable(now time.Time) bool {
	return strings.TrimSpace(c.AccessToken) != "" && c.Expiry().After(now.Add(60*time.Second))
}

func DefaultCredentialPath() string {
	return filepath.Join(agentBridgeLocalDataDir(), "antigravity", "oauth_creds.json")
}

// DefaultPOCCredentialPath preserves the original isolated credential location
// used by the OAuth POC. It is intentionally separate from the production
// provider path so a production migration never overwrites the live POC file.
func DefaultPOCCredentialPath() string {
	return filepath.Join(agentBridgeLocalDataDir(), "agy-poc", "oauth_creds.json")
}

func agentBridgeLocalDataDir() string {
	base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if base == "" {
		if configDir, err := os.UserConfigDir(); err == nil {
			base = configDir
		}
	}
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(base, "AgentBridge")
}

func LoadCredentials(path string) (Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("read OAuth credentials: %w", err)
	}
	var credentials Credentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return Credentials{}, fmt.Errorf("decode OAuth credentials: %w", err)
	}
	if credentials.AccessToken == "" && credentials.RefreshToken == "" {
		return Credentials{}, errors.New("OAuth credentials do not contain usable OAuth material")
	}
	return credentials, nil
}

func SaveCredentials(path string, credentials Credentials) error {
	if strings.TrimSpace(credentials.AccessToken) == "" || strings.TrimSpace(credentials.RefreshToken) == "" {
		return errors.New("refusing to persist incomplete OAuth credentials")
	}
	if path == "" {
		return errors.New("credential path is empty")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create OAuth credential directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to replace a symlinked OAuth credential file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect OAuth credential file: %w", err)
	}

	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return fmt.Errorf("encode OAuth credentials: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".oauth_creds-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary OAuth credential file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0600); err != nil {
		return fmt.Errorf("restrict temporary OAuth credential file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write OAuth credentials: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("flush OAuth credentials: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close OAuth credentials: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit OAuth credentials: %w", err)
	}
	_ = os.Chmod(path, 0600)
	return nil
}

func DeleteCredentials(path string) error {
	if path == "" {
		return errors.New("credential path is empty")
	}
	if info, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect OAuth credential file: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to delete a symlinked OAuth credential file")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete OAuth credentials: %w", err)
	}
	return nil
}

// TokenSource is intentionally small so cloudcode can be tested with a fake token.
type TokenSource interface {
	AccessToken(context.Context) (string, error)
	Refresh(context.Context) error
}

type TokenManager struct {
	Path       string
	Config     Config
	HTTPClient *http.Client
	mu         sync.Mutex
}

// CredentialStatus contains only non-secret OAuth metadata suitable for
// diagnostics and telemetry. It deliberately omits both token values.
type CredentialStatus struct {
	HasAccessToken  bool
	HasRefreshToken bool
	Expiry          time.Time
	ClientID        string
}

func (m *TokenManager) CredentialStatus() (CredentialStatus, error) {
	if m == nil {
		return CredentialStatus{}, errors.New("OAuth token manager is nil")
	}
	credentials, err := LoadCredentials(m.Path)
	if err != nil {
		return CredentialStatus{}, err
	}
	return CredentialStatus{
		HasAccessToken:  strings.TrimSpace(credentials.AccessToken) != "",
		HasRefreshToken: strings.TrimSpace(credentials.RefreshToken) != "",
		Expiry:          credentials.Expiry(),
		ClientID:        credentials.ClientID,
	}, nil
}

func (m *TokenManager) AccessToken(ctx context.Context) (string, error) {
	if m == nil {
		return "", errors.New("OAuth token manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	credentials, err := LoadCredentials(m.Path)
	if err != nil {
		return "", err
	}
	if credentials.AccessTokenUsable(time.Now()) {
		return credentials.AccessToken, nil
	}
	if err := m.refreshLocked(ctx, credentials); err != nil {
		return "", err
	}
	updated, err := LoadCredentials(m.Path)
	if err != nil {
		return "", err
	}
	return updated.AccessToken, nil
}

func (m *TokenManager) Refresh(ctx context.Context) error {
	if m == nil {
		return errors.New("OAuth token manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	credentials, err := LoadCredentials(m.Path)
	if err != nil {
		return err
	}
	return m.refreshLocked(ctx, credentials)
}

func (m *TokenManager) refreshLocked(ctx context.Context, credentials Credentials) error {
	clientConfig := m.Config
	if clientConfig.ClientID == "" {
		clientConfig.ClientID = credentials.ClientID
	}
	if m.HTTPClient != nil {
		clientConfig.HTTPClient = m.HTTPClient
	}
	token, err := RefreshAccessToken(ctx, clientConfig, credentials.RefreshToken)
	if err != nil {
		return fmt.Errorf("refresh OAuth token: %w", err)
	}
	if token.RefreshToken == "" {
		token.RefreshToken = credentials.RefreshToken
	}
	updated := CredentialsFromToken(token, clientConfig.ClientID)
	if updated.ClientID == "" {
		updated.ClientID = credentials.ClientID
	}
	if err := SaveCredentials(m.Path, updated); err != nil {
		return err
	}
	return nil
}

type StaticToken struct {
	Value string
}

func (s StaticToken) AccessToken(context.Context) (string, error) {
	if strings.TrimSpace(s.Value) == "" {
		return "", errors.New("static OAuth token is empty")
	}
	return s.Value, nil
}

func (s StaticToken) Refresh(context.Context) error { return nil }

func parseRedirectURI(redirectURI string) (host, port, path string, err error) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return "", "", "", fmt.Errorf("parse OAuth redirect URI: %w", err)
	}
	if u.Scheme != "http" {
		return "", "", "", errors.New("OAuth redirect URI must use http:// for loopback")
	}
	host = strings.ToLower(u.Hostname())
	if host != "localhost" && host != "127.0.0.1" {
		return "", "", "", errors.New("OAuth redirect URI must target localhost or 127.0.0.1")
	}
	port = u.Port()
	if port == "" {
		return "", "", "", errors.New("OAuth redirect URI must include an explicit port")
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", "", "", fmt.Errorf("invalid OAuth redirect URI port: %w", err)
	}
	path = u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return host, port, path, nil
}

func writeCallbackHTML(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>"+htmlEscape(title)+"</title></head><body><h1>"+htmlEscape(title)+"</h1><p>"+htmlEscape(body)+"</p></body></html>")
}

func htmlEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;").Replace(value)
}

func sendCallbackError(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}
