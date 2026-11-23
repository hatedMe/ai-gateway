package translate

import (
	"encoding/json"
	"fmt"
	"time"
)

func responseCreationTime(root object, stage string) (int64, error) {
	if value, exists := root["created"]; exists {
		number, ok := value.(json.Number)
		if ok {
			if created, err := number.Int64(); err == nil && created >= 0 {
				return created, nil
			}
		}
		return 0, &ConversionError{Code: "unsupported_capability", Stage: stage, Field: "created", Description: "invalid upstream creation timestamp"}
	}
	return time.Now().Unix(), nil
}

// Complete the public Responses wire fields independently of the internal
// Messages representation, which does not require item IDs or annotations.
func responseItemFields(item object, responseID string, index int, status string) {
	if id, _ := item["id"].(string); id == "" {
		prefix := "msg"
		if item["type"] == "reasoning" {
			prefix = "rs"
		}
		if item["type"] == "function_call" {
			item["id"] = "fc_" + item["call_id"].(string)
		} else {
			item["id"] = fmt.Sprintf("%s_%s_%d", prefix, responseID, index)
		}
	}
	item["status"] = status
	if parts, ok := item["content"].([]object); ok {
		for _, part := range parts {
			if part["type"] == "output_text" && part["annotations"] == nil {
				part["annotations"] = []object{}
			}
		}
	}
}
