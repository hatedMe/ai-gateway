package translate

import (
	"encoding/json"
	"fmt"
	"io"
)

// UpstreamError preserves the error details reported by the Responses provider.
type UpstreamError struct {
	Type      string `json:"type,omitempty"`
	Stage     string `json:"stage,omitempty"`
	Field     string `json:"field,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Param     string `json:"param,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream %s (%s): %s", e.Type, e.Code, e.Message)
}

func streamError(raw []byte) error {
	var e struct {
		Error    *UpstreamError `json:"error"`
		Response struct {
			Error     *UpstreamError `json:"error"`
			RequestID string         `json:"request_id"`
		} `json:"response"`
		UpstreamError
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	if e.Response.Error != nil {
		if e.Response.Error.RequestID == "" {
			e.Response.Error.RequestID = e.Response.RequestID
			if e.Response.Error.RequestID == "" {
				e.Response.Error.RequestID = e.RequestID
			}
		}
		return e.Response.Error
	}
	if e.Error != nil {
		if e.Error.RequestID == "" {
			e.Error.RequestID = e.RequestID
		}
		return e.Error
	}
	if e.Message == "" {
		e.Message = "upstream stream failed"
	}
	return &e.UpstreamError
}

// AggregateStream returns the provider's terminal response body, without converting it.
func AggregateStream(r io.Reader) ([]byte, error) {
	var result []byte
	err := readSSE(r, func(raw []byte) (bool, error) {
		var e struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return false, err
		}
		switch e.Type {
		case "response.failed", "error":
			return false, streamError(raw)
		case "response.completed", "response.incomplete":
			var response upstreamResponse
			if err := json.Unmarshal(e.Response, &response); err != nil {
				return false, err
			}
			if (e.Type == "response.completed" && response.Status != "completed") || (e.Type == "response.incomplete" && response.Status != "incomplete") {
				return false, fmt.Errorf("terminal event status mismatch")
			}
			if response.Error != nil {
				if response.Error.RequestID == "" {
					response.Error.RequestID = response.RequestID
				}
				return false, response.Error
			}
			if response.ID == "" {
				return false, fmt.Errorf("missing upstream response id")
			}
			result = append([]byte(nil), e.Response...)
			return true, nil
		}
		return false, nil
	})
	return result, err
}

func (p upstreamPart) citations() []object {
	var out []object
	for _, a := range p.Annotations {
		typ, _ := a["type"].(string)
		switch typ {
		case "url_citation":
			if url, ok := a["url"].(string); ok && url != "" {
				cited := p.Text
				start, sok := a["start_index"].(float64)
				end, eok := a["end_index"].(float64)
				runes := []rune(p.Text)
				if sok && eok && start >= 0 && end >= start && int(end) <= len(runes) {
					cited = string(runes[int(start):int(end)])
				}
				out = append(out, object{"type": "web_search_result_location", "url": url, "title": a["title"], "cited_text": cited, "encrypted_index": ""})
			}
		case "file_citation":
			if _, ok := a["document_index"]; ok {
				if _, has := a["start_page_number"]; has {
					c := object{"type": "page_location", "document_index": a["document_index"], "document_title": a["filename"], "file_id": a["file_id"], "start_page_number": a["start_page_number"], "end_page_number": a["end_page_number"], "cited_text": a["cited_text"]}
					if c["end_page_number"] != nil && c["cited_text"] != nil {
						out = append(out, c)
					}
				}
			}
		case "char_location", "page_location", "content_block_location", "web_search_result_location":
			out = append(out, a)
		}
	}
	return out
}

func (p upstreamPart) annotationText() string {
	var suffix string
	for _, a := range p.Annotations {
		candidate := upstreamPart{Annotations: []object{a}}
		if len(candidate.citations()) > 0 {
			continue
		}
		raw, _ := json.Marshal(a)
		suffix += "\n[Source annotation: " + string(raw) + "]"
	}
	return suffix
}

func (i upstreamItem) serverBlocks() []object {
	name, resultType := "web_search", "web_search_tool_result"
	input := i.Action
	content := any(i.Results)
	if i.Type == "code_interpreter_call" {
		name = "code_execution"
		resultType = "code_execution_tool_result"
		input = object{"code": i.Code}
		raw, _ := json.Marshal(i.Outputs)
		if i.ReturnCode == nil {
			return []object{{"type": "server_tool_use", "id": i.ID, "name": name, "input": input}, {"type": "text", "text": "[Upstream code execution output; exit status unavailable] " + string(raw)}}
		}
		content = object{"type": "code_execution_result", "stdout": i.Stdout, "stderr": i.Stderr, "return_code": *i.ReturnCode, "content": []object{}}
	}
	if i.Type == "file_search_call" {
		name = "file_search"
		resultType = "web_search_tool_result"
	}
	if input == nil {
		input = object{}
	}
	if resultType == "web_search_tool_result" {
		results := []object{}
		unmapped := []object{}
		for _, r := range i.Results {
			url, _ := r["url"].(string)
			if url != "" {
				results = append(results, object{"type": "web_search_result", "url": url, "title": r["title"], "encrypted_content": ""})
				if len(r) > 2 {
					unmapped = append(unmapped, r)
				}
			} else {
				unmapped = append(unmapped, r)
			}
		}
		content = results
		if len(unmapped) > 0 {
			raw, _ := json.Marshal(unmapped)
			blocks := []object{{"type": "server_tool_use", "id": i.ID, "name": name, "input": input}}
			if len(results) > 0 {
				blocks = append(blocks, object{"type": resultType, "tool_use_id": i.ID, "content": content})
			}
			return append(blocks, object{"type": "text", "text": "[Upstream search results] " + string(raw)})
		}
	}
	return []object{{"type": "server_tool_use", "id": i.ID, "name": name, "input": input}, {"type": resultType, "tool_use_id": i.ID, "content": content}}
}
