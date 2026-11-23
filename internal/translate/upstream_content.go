package translate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ConvertInputContent converts one user content array without fetching images.
// Unsupported parts fail the entire conversion; callers retain message roles/history.
func ConvertInputContent(raw []byte, source, target Protocol, allowImage bool) ([]byte, error) {
	validProtocol := func(p Protocol) bool {
		return p == ChatProtocol || p == ResponsesProtocol || p == AnthropicProtocol
	}
	if !validProtocol(source) || !validProtocol(target) {
		return nil, contentError("unsupported_field", "protocol")
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return nil, contentError("unsupported_field", "content")
	}
	out := make([]object, 0, len(parts))
	for i, rawPart := range parts {
		field := fmt.Sprintf("content[%d]", i)
		part, err := rawObject(rawPart)
		if err != nil {
			return nil, contentError("unsupported_field", field)
		}
		typ, err := text(part["type"])
		if err != nil {
			return nil, contentError("unsupported_field", field+".type")
		}
		isText := typ == "text" && source != ResponsesProtocol || source == ResponsesProtocol && (typ == "input_text" || typ == "output_text")
		if isText {
			if only(part, "type", "text") != nil {
				return nil, contentError("unsupported_field", field)
			}
			s, err := text(part["text"])
			if err != nil {
				return nil, contentError("unsupported_field", field+".text")
			}
			textType := "text"
			if target == ResponsesProtocol {
				textType = "input_text"
			}
			out = append(out, object{"type": textType, "text": s})
			continue
		}
		imageType := map[Protocol]string{ChatProtocol: "image_url", ResponsesProtocol: "input_image", AnthropicProtocol: "image"}[source]
		if typ != imageType {
			return nil, contentError("unsupported_field", field+".type")
		}
		if !allowImage {
			return nil, contentError("unsupported_capability", field)
		}
		imageURL, detail, err := readInputImage(part, source)
		if err != nil {
			return nil, contentError("unsupported_field", field)
		}
		media, data, valid := inputImageURL(imageURL)
		if !valid {
			return nil, contentError("unsupported_field", field)
		}
		if detail != "" && detail != "auto" && detail != "low" && detail != "high" {
			return nil, contentError("unsupported_field", field+".detail")
		}
		switch target {
		case ChatProtocol:
			image := object{"url": imageURL}
			if detail != "" {
				image["detail"] = detail
			}
			out = append(out, object{"type": "image_url", "image_url": image})
		case ResponsesProtocol:
			image := object{"type": "input_image", "image_url": imageURL}
			if detail != "" {
				image["detail"] = detail
			}
			out = append(out, image)
		case AnthropicProtocol:
			if detail != "" && detail != "auto" {
				return nil, contentError("unsupported_capability", field+".detail")
			}
			image := object{"type": "url", "url": imageURL}
			if media != "" {
				image = object{"type": "base64", "media_type": media, "data": data}
			}
			out = append(out, object{"type": "image", "source": image})
		}
	}
	return json.Marshal(out)
}

func contentError(code, field string) error {
	return &ConversionError{Code: code, Stage: "request_content", Field: field, Description: "content cannot be converted without losing unsupported or invalid semantics"}
}

func readInputImage(part map[string]json.RawMessage, source Protocol) (string, string, error) {
	var image map[string]json.RawMessage
	switch source {
	case AnthropicProtocol:
		// The existing block converter validates the native URL/base64 source.
		if err := only(part, "type", "source"); err != nil {
			return "", "", err
		}
		converted, err := inputPart(part, "user")
		if err != nil {
			return "", "", err
		}
		return converted["image_url"].(string), "", nil
	case ChatProtocol:
		if err := only(part, "type", "image_url"); err != nil {
			return "", "", err
		}
		var err error
		image, err = rawObject(part["image_url"])
		if err != nil {
			return "", "", err
		}
		if err := only(image, "url", "detail"); err != nil {
			return "", "", err
		}
	case ResponsesProtocol:
		if err := only(part, "type", "image_url", "detail"); err != nil {
			return "", "", err
		}
		image = part
	}
	urlKey := "image_url"
	if source == ChatProtocol {
		urlKey = "url"
	}
	imageURL, err := text(image[urlKey])
	if err != nil {
		return "", "", err
	}
	var detail string
	if raw, exists := image["detail"]; exists {
		detail, err = text(raw)
		if err != nil || detail == "" {
			return "", "", fmt.Errorf("invalid image detail")
		}
	}
	return imageURL, detail, nil
}

func inputImageURL(value string) (media, data string, valid bool) {
	if strings.HasPrefix(value, "data:") {
		header, encoded, ok := strings.Cut(strings.TrimPrefix(value, "data:"), ";base64,")
		if !ok || encoded == "" {
			return "", "", false
		}
		switch header {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return "", "", false
		}
		if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
			return "", "", false
		}
		return header, encoded, true
	}
	u, err := url.Parse(value)
	return "", "", err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}
