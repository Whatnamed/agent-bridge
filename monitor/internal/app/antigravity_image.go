package app

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode"

	"github.com/whatnamed/agent-bridge/shared/antigravity/cloudcode"
)

const (
	maxAntigravityRequestJSONBytes = 20 << 20
	maxAntigravityImageBytes       = 15 << 20
)

var supportedAntigravityImageMIMEs = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/webp": {},
	"image/heic": {},
	"image/heif": {},
}

func antigravityMessageParts(value any) ([]cloudcode.ContentPart, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		return []cloudcode.ContentPart{{Text: text}}, nil
	}
	rawParts, ok := value.([]any)
	if !ok {
		return nil, errorsForAntigravityContent("message content must be a string or an array of content parts")
	}
	parts := make([]cloudcode.ContentPart, 0, len(rawParts))
	for _, raw := range rawParts {
		part := mapAny(raw)
		if part == nil {
			return nil, errorsForAntigravityContent("message content contains an invalid content part")
		}
		typ := strings.ToLower(strings.TrimSpace(stringValue(part["type"])))
		switch typ {
		case "input_text", "text", "output_text":
			if _, ok := part["text"]; !ok {
				return nil, fmt.Errorf("Antigravity %s input must include text", typ)
			}
			parts = append(parts, cloudcode.ContentPart{Text: stringValue(part["text"])})
		case "input_image", "image_url":
			image, err := antigravityImageContentPart(part)
			if err != nil {
				return nil, err
			}
			parts = append(parts, image)
		default:
			return nil, fmt.Errorf("Antigravity does not support input content type %q", typ)
		}
	}
	return parts, nil
}

func antigravityImageContentPart(part map[string]any) (cloudcode.ContentPart, error) {
	if part == nil {
		return cloudcode.ContentPart{}, errorsForAntigravityContent("image content part must be an object")
	}
	value := part["image_url"]
	if value == nil && strings.EqualFold(strings.TrimSpace(stringValue(part["type"])), "image_url") {
		value = part["url"]
	}
	urlValue := strings.TrimSpace(stringValue(value))
	if imageMap := mapAny(value); imageMap != nil {
		urlValue = strings.TrimSpace(stringValue(imageMap["url"]))
	}
	if urlValue == "" {
		return cloudcode.ContentPart{}, errorsForAntigravityContent("Antigravity input_image must include image_url")
	}
	if !strings.HasPrefix(strings.ToLower(urlValue), "data:") {
		return cloudcode.ContentPart{}, errorsForAntigravityContent("Antigravity remote image URLs are not supported; use a data URL")
	}
	mimeType, encoded, err := parseAntigravityDataURL(urlValue)
	if err != nil {
		return cloudcode.ContentPart{}, err
	}
	return cloudcode.ContentPart{InlineData: &cloudcode.InlineData{MimeType: mimeType, Data: encoded}}, nil
}

func parseAntigravityDataURL(value string) (string, string, error) {
	if !strings.HasPrefix(strings.ToLower(value), "data:") {
		return "", "", errorsForAntigravityContent("Antigravity image URL must be a data URL")
	}
	comma := strings.IndexByte(value, ',')
	if comma <= len("data:") || comma == len(value)-1 {
		return "", "", errorsForAntigravityContent("Antigravity image data URL is malformed")
	}
	metadata := value[len("data:"):comma]
	payload := value[comma+1:]
	parts := strings.Split(metadata, ";")
	if len(parts) != 2 || !strings.EqualFold(parts[1], "base64") {
		return "", "", errorsForAntigravityContent("Antigravity image data URL must use base64 encoding")
	}
	mimeType := strings.ToLower(strings.TrimSpace(parts[0]))
	if _, ok := supportedAntigravityImageMIMEs[mimeType]; !ok {
		return "", "", fmt.Errorf("Antigravity does not support image MIME type %q", mimeType)
	}
	if strings.IndexFunc(payload, unicode.IsSpace) >= 0 {
		return "", "", errorsForAntigravityContent("Antigravity image base64 must not contain whitespace")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil || len(decoded) == 0 || base64.StdEncoding.EncodeToString(decoded) != payload {
		return "", "", errorsForAntigravityContent("Antigravity image base64 is malformed")
	}
	if len(decoded) > maxAntigravityImageBytes {
		return "", "", errorsForAntigravityContent("Antigravity image exceeds the supported size limit")
	}
	return mimeType, payload, nil
}

func errorsForAntigravityContent(message string) error {
	return fmt.Errorf("Antigravity %s", message)
}
