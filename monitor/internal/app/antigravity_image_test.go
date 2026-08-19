package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestAntigravityMessagePartsPreserveInlineImageOrder(t *testing.T) {
	parts, err := antigravityMessageParts([]any{
		map[string]any{"type": "input_text", "text": "before"},
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AA=="},
		map[string]any{"type": "input_text", "text": "after"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0].Text != "before" || parts[1].InlineData == nil || parts[2].Text != "after" {
		t.Fatalf("parts = %#v", parts)
	}
	if parts[1].InlineData.MimeType != "image/png" || parts[1].InlineData.Data != "AA==" {
		t.Fatalf("inline image = %#v", parts[1].InlineData)
	}

	for _, mimeType := range []string{"image/png", "image/jpeg", "image/webp", "image/heic", "image/heif"} {
		part, err := antigravityImageContentPart(map[string]any{
			"type": "input_image", "image_url": "data:" + mimeType + ";base64,AA==",
		})
		if err != nil || part.InlineData == nil || part.InlineData.MimeType != mimeType {
			t.Fatalf("mime %s: part=%#v err=%v", mimeType, part, err)
		}
	}
}

func TestAntigravityBuildRequestSerializesInlineDataAndChatImage(t *testing.T) {
	request, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "describe"},
			map[string]any{"type": "input_image", "image_url": "data:image/jpeg;base64,AA=="},
		}}},
	}, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	parts := request.Request.Contents[0].Parts
	if len(parts) != 2 || parts[0].Text != "describe" || parts[1].InlineData == nil || parts[1].InlineData.MimeType != "image/jpeg" {
		t.Fatalf("request parts = %#v", parts)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"inlineData":{"mimeType":"image/jpeg","data":"AA=="}`)) || bytes.Contains(encoded, []byte("image_url")) {
		t.Fatalf("serialized request contains unexpected image representation: %s", encoded)
	}

	chat, err := chatToResponse(map[string]any{
		"model": stableAntigravityModel,
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "describe"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/webp;base64,AA=="}},
		}}},
	}, stableAntigravityModel)
	if err != nil {
		t.Fatal(err)
	}
	chatRequest, err := buildAntigravityRequest(chat, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	chatParts := chatRequest.Request.Contents[0].Parts
	if len(chatParts) != 2 || chatParts[1].InlineData == nil || chatParts[1].InlineData.MimeType != "image/webp" {
		t.Fatalf("chat image parts = %#v", chatParts)
	}
}

func TestAntigravityImageDataURLFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"remote", "https://example.invalid/image.png"},
		{"unsupported mime", "data:image/gif;base64,AA=="},
		{"missing base64", "data:image/png,AA=="},
		{"bad base64", "data:image/png;base64,not-base64"},
		{"whitespace", "data:image/png;base64,A A=="},
		{"noncanonical", "data:image/png;base64,AA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := antigravityImageContentPart(map[string]any{"type": "input_image", "image_url": tc.url})
			if err == nil {
				t.Fatal("image input was accepted")
			}
		})
	}
}

func TestAntigravityImageAndUnknownContentAreRejectedByPayloadValidation(t *testing.T) {
	valid := map[string]any{
		"model": stableAntigravityModel,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AA=="},
		}}},
	}
	if err := validateAntigravityPayload(valid); err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}
	unknown := map[string]any{
		"model": stableAntigravityModel,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_file", "file_id": "file_1"},
		}}},
	}
	if err := validateAntigravityPayload(unknown); err == nil || !strings.Contains(err.Error(), "input content type") {
		t.Fatalf("unknown content was accepted: %v", err)
	}
}

func TestAntigravityImageRequestSizeGuard(t *testing.T) {
	data := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, maxAntigravityImageBytes))
	_, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + data},
		}}},
	}, stableAntigravityModel, "projects/test-project")
	if err == nil || !strings.Contains(err.Error(), "JSON limit") {
		t.Fatalf("oversized request error = %v", err)
	}
}
