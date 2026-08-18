package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationURLUsesStateAndPKCEOfflineFlow(t *testing.T) {
	config := Config{ClientID: "client-id", RedirectURI: DefaultRedirectURI, Scopes: []string{"scope-a", "scope-b"}}
	state := "state-value"
	verifier, challenge, err := GeneratePKCEVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if verifier == "" || challenge == "" {
		t.Fatal("PKCE values must not be empty")
	}
	authURL, err := BuildAuthorizationURL(config, state, challenge)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	for key, expected := range map[string]string{
		"client_id":              "client-id",
		"redirect_uri":           DefaultRedirectURI,
		"response_type":          "code",
		"access_type":            "offline",
		"prompt":                 "consent",
		"include_granted_scopes": "true",
		"state":                  state,
		"code_challenge":         challenge,
		"code_challenge_method":  "S256",
	} {
		if got := query.Get(key); got != expected {
			t.Fatalf("query %s = %q, want %q", key, got, expected)
		}
	}
}

func TestCallbackServerAcceptsLoopbackCallback(t *testing.T) {
	port := freePort(t)
	callback, err := ListenCallback(fmt.Sprintf("http://localhost:%d/oauth-callback", port))
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()

	go func() {
		_, _ = http.Get(fmt.Sprintf("http://127.0.0.1:%d/oauth-callback?code=one-time-code&state=expected", port))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := callback.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Code != "one-time-code" || result.State != "expected" {
		t.Fatalf("unexpected callback result: %+v", result)
	}
	if err := ValidateState("expected", result.State); err != nil {
		t.Fatal(err)
	}
	if err := ValidateState("expected", "wrong"); err == nil {
		t.Fatal("state mismatch must fail")
	}
}

func TestExchangeErrorDoesNotEchoTokenLikeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"secret-token-should-not-escape"}`))
	}))
	defer server.Close()
	_, err := ExchangeCode(context.Background(), Config{
		ClientID:      "client-id",
		TokenEndpoint: server.URL,
		HTTPClient:    server.Client(),
	}, "code", "verifier")
	if err == nil {
		t.Fatal("expected token exchange error")
	}
	if strings.Contains(err.Error(), "secret-token-should-not-escape") {
		t.Fatalf("error leaked response body: %v", err)
	}
}

func TestCredentialStoreRoundTripAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth_creds.json")
	want := Credentials{
		AccessToken:  "access-token-for-test",
		RefreshToken: "refresh-token-for-test",
		ExpiryDate:   time.Now().Add(time.Hour).UnixMilli(),
		TokenType:    "Bearer",
		Scope:        "scope",
		ClientID:     "client-id",
	}
	if err := SaveCredentials(path, want); err != nil {
		t.Fatal(err)
	}
	want.AccessToken = "replacement-access-token"
	if err := SaveCredentials(path, want); err != nil {
		t.Fatalf("second atomic save failed: %v", err)
	}
	got, err := LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || got.ClientID != want.ClientID {
		t.Fatalf("credential round trip mismatch: got %+v want %+v", got, want)
	}
	if err := DeleteCredentials(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("credential file still exists: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
