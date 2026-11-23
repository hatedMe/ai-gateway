package translate

import "fmt"

// Anthropic context edits are not Responses compaction. Preserve input rather
// than deleting tool results or opaque reasoning under different semantics.
func prepareContextManagement(root object, warn func(string)) error {
	value, exists := root["context_management"]
	if !exists {
		return nil
	}
	delete(root, "context_management")
	if value == nil {
		return nil
	}
	config, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("context_management must be an object")
	}
	for key := range config {
		if key != "edits" {
			return fmt.Errorf("unsupported context_management field %q", key)
		}
	}
	value, exists = config["edits"]
	if !exists {
		return nil
	}
	edits, ok := value.([]any)
	if !ok {
		return fmt.Errorf("context_management.edits must be an array")
	}
	for _, value := range edits {
		edit, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("context_management edits must be objects")
		}
		typ, ok := edit["type"].(string)
		if !ok || typ == "" {
			return fmt.Errorf("context_management edit type must be a non-empty string")
		}
		if typ == "clear_thinking_20251015" && edit["keep"] == "all" && len(edit) == 2 {
			continue
		}
		warn("context_management edit " + typ + " has no equivalent on this upstream; history was preserved without applying the edit")
	}
	return nil
}
