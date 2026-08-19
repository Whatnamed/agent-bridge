package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAntigravityCompatibilityFixturesTranslateOffline(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "compat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 4 {
		t.Fatalf("fixture count = %d, want at least four entry shapes", len(entries))
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "compat", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Entrypoint string         `json:"entrypoint"`
				Source     string         `json:"source"`
				Request    map[string]any `json:"request"`
			}
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			if fixture.Source == "" || fixture.Request == nil {
				t.Fatalf("invalid fixture metadata: %#v", fixture)
			}
			switch fixture.Entrypoint {
			case "responses":
				prepared := prepareResponse(fixture.Request, stableAntigravityModel)
				if err := validateAntigravityPayload(prepared); err != nil {
					t.Fatalf("Responses fixture rejected: %v", err)
				}
			case "chat":
				converted, err := chatToResponse(fixture.Request, stableAntigravityModel)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateAntigravityPayload(converted); err != nil {
					t.Fatalf("Chat fixture rejected: %v", err)
				}
			default:
				t.Fatalf("unknown fixture entrypoint %q", fixture.Entrypoint)
			}
		})
	}
}
