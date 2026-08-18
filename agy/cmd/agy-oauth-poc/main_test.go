package main

import (
	"testing"

	"github.com/whatnamed/agent-bridge/agy/internal/auth"
	"github.com/whatnamed/agent-bridge/agy/internal/probe"
)

func TestParseProbeArgsToolTestUsesDeterministicPrompt(t *testing.T) {
	options, err := parseProbeArgs([]string{"--mode", "minimal", "--tool-test"})
	if err != nil {
		t.Fatal(err)
	}
	if options.Prompt != probe.DefaultToolTestPrompt || options.PromptSpecified {
		t.Fatalf("tool-test options = %+v", options)
	}
}

func TestParseProbeArgsExplicitPromptWinsOverToolDefault(t *testing.T) {
	options, err := parseProbeArgs([]string{"--tool-test", "--prompt", "用户指定的工具测试提示"})
	if err != nil {
		t.Fatal(err)
	}
	if options.Prompt != "用户指定的工具测试提示" || !options.PromptSpecified {
		t.Fatalf("explicit prompt options = %+v", options)
	}
}

func TestParseProbeArgsDefaultsToAntigravityProfile(t *testing.T) {
	options, err := parseProbeArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.OAuthProfile != string(auth.DefaultProfile) {
		t.Fatalf("default OAuth profile = %q, want %q", options.OAuthProfile, auth.DefaultProfile)
	}
}
