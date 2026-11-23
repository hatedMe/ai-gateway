package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

type upstreamPart struct {
	Type        string   `json:"type"`
	Text        string   `json:"text"`
	Refusal     string   `json:"refusal,omitempty"`
	Annotations []object `json:"annotations,omitempty"`
}

type upstreamItem struct {
	ID               string         `json:"id"`
	Type             string         `json:"type"`
	Role             string         `json:"role"`
	CallID           string         `json:"call_id"`
	Name             string         `json:"name"`
	Arguments        string         `json:"arguments"`
	Status           string         `json:"status,omitempty"`
	Action           object         `json:"action,omitempty"`
	Results          []object       `json:"results,omitempty"`
	Outputs          []object       `json:"outputs,omitempty"`
	Code             string         `json:"code,omitempty"`
	ReturnCode       *int           `json:"return_code,omitempty"`
	Stdout           string         `json:"stdout,omitempty"`
	Stderr           string         `json:"stderr,omitempty"`
	Content          []upstreamPart `json:"content"`
	Summary          []upstreamPart `json:"summary"`
	EncryptedContent string         `json:"encrypted_content"`
}

type upstreamUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	InputDetails struct {
		CachedTokens        int `json:"cached_tokens"`
		CacheWriteTokens    int `json:"cache_write_tokens"`
		CacheCreationTokens int `json:"cache_creation_tokens"`
	} `json:"input_tokens_details"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type upstreamResponse struct {
	ID                string         `json:"id"`
	Status            string         `json:"status"`
	StopReason        string         `json:"stop_reason,omitempty"`
	Model             string         `json:"model"`
	RequestID         string         `json:"request_id"`
	Output            []upstreamItem `json:"output"`
	Usage             upstreamUsage  `json:"usage"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *UpstreamError `json:"error"`
}

func (u upstreamUsage) anthropic() (object, error) {
	cached := u.InputDetails.CachedTokens
	if cached == 0 {
		cached = u.CacheReadInputTokens
	}
	created := u.InputDetails.CacheWriteTokens
	if created == 0 {
		created = u.InputDetails.CacheCreationTokens
	}
	if created == 0 {
		created = u.CacheCreationInputTokens
	}
	if u.InputTokens < 0 || u.OutputTokens < 0 || cached < 0 || created < 0 || cached+created > u.InputTokens {
		return nil, fmt.Errorf("invalid upstream token usage")
	}
	return object{"input_tokens": u.InputTokens - cached - created, "output_tokens": u.OutputTokens, "cache_read_input_tokens": cached, "cache_creation_input_tokens": created}, nil
}

func messageID(id string) string { return "msg_" + strings.TrimPrefix(id, "resp_") }

func (r upstreamResponse) message(model string) (object, error) {
	return r.messageFor(model, false)
}

func (r upstreamResponse) messageFor(model string, chat bool) (object, error) {
	if r.ID == "" {
		return nil, fmt.Errorf("missing upstream response id")
	}
	if r.Error != nil {
		if r.Error.RequestID == "" {
			r.Error.RequestID = r.RequestID
		}
		return nil, r.Error
	}
	stop := "end_turn"
	if r.StopReason == "refusal" {
		stop = "refusal"
	}
	switch r.Status {
	case "completed":
	case "incomplete":
		switch r.IncompleteDetails.Reason {
		case "max_output_tokens":
			stop = "max_tokens"
		case "content_filter":
			stop = "refusal"
			if chat {
				stop = "content_filter"
			}
		default:
			return nil, fmt.Errorf("unsupported incomplete reason %q", r.IncompleteDetails.Reason)
		}
	default:
		return nil, fmt.Errorf("upstream response is not complete: %s", r.Status)
	}
	blocks := make([]object, 0)
	toolIDs := map[string]bool{}
	for _, item := range r.Output {
		switch item.Type {
		case "reasoning":
			block, err := item.thinkingBlock()
			if err != nil {
				return nil, err
			}
			if block != nil {
				blocks = append(blocks, block)
			}
		case "message":
			if item.Role != "" && item.Role != "assistant" {
				return nil, fmt.Errorf("invalid upstream output role")
			}
			for _, part := range item.Content {
				value, e := part.value()
				if e != nil {
					return nil, e
				}
				block := object{"type": "text", "text": value}
				if chat && part.Type == "refusal" {
					block["refusal"] = true
				}
				if citations := part.citations(); len(citations) > 0 {
					block["citations"] = citations
				}
				blocks = append(blocks, block)
				if part.Type == "refusal" && stop == "end_turn" {
					stop = "refusal"
				}
			}
		case "function_call":
			if item.CallID == "" || item.Name == "" || toolIDs[item.CallID] {
				return nil, fmt.Errorf("invalid or duplicate upstream function call")
			}
			args, e := rawObject([]byte(item.Arguments))
			if e != nil {
				if stop == "max_tokens" {
					blocks = append(blocks, object{"type": "text", "text": fmt.Sprintf("[Incomplete tool call: name=%s call_id=%s arguments=%s]", item.Name, item.CallID, item.Arguments)})
					continue
				}
				return nil, fmt.Errorf("invalid upstream function arguments: %w", e)
			}
			blocks = append(blocks, object{"type": "tool_use", "id": item.CallID, "name": item.Name, "input": args})
			toolIDs[item.CallID] = true
			if stop == "end_turn" {
				stop = "tool_use"
			}
		case "web_search_call", "code_interpreter_call", "file_search_call":
			if item.ID == "" {
				return nil, fmt.Errorf("missing upstream server tool id")
			}
			blocks = append(blocks, item.serverBlocks()...)
		default:
			return nil, fmt.Errorf("unsupported upstream output item %q", item.Type)
		}
	}
	usage, e := r.Usage.anthropic()
	if e != nil {
		return nil, e
	}
	if model == "" {
		model = r.Model
	}
	return object{"id": messageID(r.ID), "type": "message", "role": "assistant", "model": model, "content": blocks, "stop_reason": stop, "stop_sequence": nil, "usage": usage}, nil
}

func (p upstreamPart) value() (string, error) {
	switch p.Type {
	case "output_text":
		return p.Text + p.annotationText(), nil
	case "refusal":
		return p.Refusal, nil
	default:
		return "", fmt.Errorf("unsupported upstream content part %q", p.Type)
	}
}

// ConvertResponse translates a completed OpenAI Responses body to an Anthropic Message.
func ConvertResponse(raw []byte, model string) ([]byte, error) {
	return convertResponseFor(raw, model, false)
}

// ConvertResponseForChat preserves refusal metadata for the Chat adapter.
func ConvertResponseForChat(raw []byte, model string) ([]byte, error) {
	return convertResponseFor(raw, model, true)
}

func convertResponseFor(raw []byte, model string, chat bool) ([]byte, error) {
	var r upstreamResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	message, err := r.messageFor(model, chat)
	if err != nil {
		return nil, err
	}
	return json.Marshal(message)
}
