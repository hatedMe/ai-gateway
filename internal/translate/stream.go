package translate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type streamEvent struct {
	Type         string           `json:"type"`
	OutputIndex  *int             `json:"output_index"`
	ContentIndex *int             `json:"content_index"`
	SummaryIndex *int             `json:"summary_index"`
	Text         string           `json:"text"`
	Delta        string           `json:"delta"`
	Arguments    string           `json:"arguments"`
	Item         upstreamItem     `json:"item"`
	Part         upstreamPart     `json:"part"`
	Annotation   object           `json:"annotation"`
	Response     upstreamResponse `json:"response"`
}

type streamBlock struct {
	buffer        strings.Builder
	index         int
	kind          string
	value         string
	closed        bool
	start         object
	signature     string
	citationsDone bool
}

type queuedEvent struct {
	name     string
	block    *streamBlock
	delta    string
	citation object
}

type streamItem struct {
	directBuffer strings.Builder
	budget       *streamBudget
	item         upstreamItem
	blocks       map[int]*streamBlock
	queue        []queuedEvent
	done         bool
	summaries    []thinkingSummary
	direct       string
}

type streamState struct {
	budget       streamBudget
	chat         bool
	model        string
	id           string
	started      bool
	items        map[int]*streamItem
	nextItem     int
	nextBlock    int
	emit         func(string, any) error
	allowPartial bool
}

// ConvertStream emits Anthropic SSE payloads from an OpenAI Responses event stream.
// The caller writes an Anthropic error event if this function returns an error.
func ConvertStream(r io.Reader, model string, emit func(string, any) error) error {
	return convertStreamFor(r, model, emit, false)
}

// ConvertStreamForChat preserves refusal metadata for the Chat adapter.
func ConvertStreamForChat(r io.Reader, model string, emit func(string, any) error) error {
	return convertStreamFor(r, model, emit, true)
}

func convertStreamFor(r io.Reader, model string, emit func(string, any) error, chat bool) error {
	s := streamState{model: model, items: map[int]*streamItem{}, emit: emit, chat: chat}
	return readSSE(r, func(raw []byte) (bool, error) {
		var e streamEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return false, fmt.Errorf("invalid upstream SSE JSON: %w", err)
		}
		switch e.Type {
		case "response.created":
			if s.started {
				return false, fmt.Errorf("duplicate response.created")
			}
			return false, s.start(e.Response)
		case "response.completed", "response.incomplete":
			s.allowPartial = e.Response.Status == "incomplete" && e.Response.IncompleteDetails.Reason == "max_output_tokens"
			if (e.Type == "response.completed" && e.Response.Status != "completed") || (e.Type == "response.incomplete" && e.Response.Status != "incomplete") {
				return false, fmt.Errorf("terminal event status mismatch")
			}
			message, err := e.Response.messageFor(model, chat)
			if err != nil {
				return false, err
			}
			if !s.started {
				if err = s.start(e.Response); err != nil {
					return false, err
				}
			}
			if e.Response.ID != s.id {
				return false, fmt.Errorf("upstream response id changed")
			}
			for i, item := range e.Response.Output {
				if err = s.finishItem(i, item); err != nil {
					return false, err
				}
			}
			if len(s.items) != len(e.Response.Output) {
				return false, fmt.Errorf("terminal output differs from streamed items")
			}
			if err = s.flush(); err != nil {
				return false, err
			}
			if s.nextItem != len(s.items) {
				return false, fmt.Errorf("missing upstream output item")
			}
			if err = s.emit("message_delta", object{"type": "message_delta", "delta": object{"stop_reason": message["stop_reason"], "stop_sequence": nil}, "usage": message["usage"]}); err != nil {
				return false, err
			}
			if err = s.emit("message_stop", object{"type": "message_stop"}); err != nil {
				return false, err
			}
			return true, nil
		case "response.failed", "error":
			return false, streamError(raw)
		case "response.output_item.added":
			if !s.started {
				return false, fmt.Errorf("output before response.created")
			}
			i, err := eventIndex(e.OutputIndex)
			if err != nil {
				return false, err
			}
			if _, ok := s.items[i]; ok {
				return false, fmt.Errorf("duplicate output item")
			}
			if err = s.addItem(i, e.Item); err != nil {
				return false, err
			}
		case "response.output_item.done":
			if e.Item.Status == "incomplete" {
				break
			}
			// An item may end with partial arguments before the response declares
			// max_output_tokens. Only the response terminal can decide validity.
			if e.Item.Type == "function_call" {
				if _, err := rawObject([]byte(e.Item.Arguments)); err != nil {
					i, indexErr := eventIndex(e.OutputIndex)
					if indexErr != nil {
						return false, indexErr
					}
					st := s.items[i]
					if st == nil || st.item.Type != "function_call" {
						return false, fmt.Errorf("arguments reference missing function item")
					}
					if st.item.CallID != e.Item.CallID || st.item.Name != e.Item.Name {
						return false, fmt.Errorf("tool identity changed")
					}
					if err = st.reconcile(st.blocks[0], e.Item.Arguments); err != nil {
						return false, err
					}
					break
				}
			}
			i, err := eventIndex(e.OutputIndex)
			if err != nil {
				return false, err
			}
			if !s.started {
				return false, fmt.Errorf("output before response.created")
			}
			if err = s.finishItem(i, e.Item); err != nil {
				return false, err
			}
		case "response.output_text.annotation.added":
			i, err := eventIndex(e.OutputIndex)
			if err != nil {
				return false, err
			}
			ci, err := eventIndex(e.ContentIndex)
			if err != nil {
				return false, err
			}
			st := s.items[i]
			if st == nil || st.blocks[ci] == nil {
				return false, fmt.Errorf("annotation references missing content part")
			}
			b := st.blocks[ci]
			p := upstreamPart{Text: b.value, Annotations: []object{e.Annotation}}
			for _, c := range p.citations() {
				st.queueEvent(queuedEvent{name: "citations_delta", block: b, citation: c})
			}
			if len(p.citations()) > 0 {
				b.citationsDone = true
			}
		case "response.content_part.added", "response.content_part.done", "response.output_text.delta", "response.refusal.delta", "response.function_call_arguments.delta", "response.function_call_arguments.done":
			if !s.started {
				return false, fmt.Errorf("delta before response.created")
			}
			if err := s.content(e); err != nil {
				return false, err
			}
		case "response.reasoning_text.delta", "response.reasoning_text.done":
			i, err := eventIndex(e.OutputIndex)
			if err != nil {
				return false, err
			}
			st := s.items[i]
			if st == nil || st.item.Type != "reasoning" {
				return false, fmt.Errorf("missing reasoning item")
			}
			if strings.HasSuffix(e.Type, ".delta") {
				if !st.budget.reserve(len(e.Delta)) {
					return false, st.budget.err
				}
				st.directBuffer.WriteString(e.Delta)
				st.direct = st.directBuffer.String()
			} else {
				if !strings.HasPrefix(e.Text, st.direct) {
					return false, fmt.Errorf("reasoning text conflict")
				}
				if !st.budget.reserve(len(e.Text) - len(st.direct)) {
					return false, st.budget.err
				}
				st.directBuffer.WriteString(strings.TrimPrefix(e.Text, st.direct))
				st.direct = st.directBuffer.String()
			}
		case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
			if !s.started {
				return false, fmt.Errorf("reasoning before response.created")
			}
			if err := s.thinking(e); err != nil {
				return false, err
			}
		default:
			// Lifecycle, annotation and unknown future events carry no supported content.
		}
		return false, s.flush()
	})
}

func eventIndex(p *int) (int, error) {
	if p == nil || *p < 0 {
		return 0, fmt.Errorf("missing or negative event index")
	}
	return *p, nil
}

func (s *streamState) start(r upstreamResponse) error {
	if r.ID == "" {
		return fmt.Errorf("missing response id")
	}
	s.id = r.ID
	s.started = true
	model := s.model
	if model == "" {
		model = r.Model
	}
	usage, err := r.Usage.anthropic()
	if err != nil {
		return err
	}
	return s.emit("message_start", object{"type": "message_start", "message": object{"id": messageID(r.ID), "type": "message", "role": "assistant", "model": model, "content": []object{}, "stop_reason": nil, "stop_sequence": nil, "usage": usage}})
}

func (s *streamState) addItem(index int, item upstreamItem) error {
	if item.Type != "message" && item.Type != "function_call" && item.Type != "reasoning" && item.Type != "web_search_call" && item.Type != "code_interpreter_call" && item.Type != "file_search_call" {
		return fmt.Errorf("unsupported streamed output item %q", item.Type)
	}
	if len(s.items) >= 32768 {
		return fmt.Errorf("upstream stream output item limit exceeded")
	}
	raw, _ := json.Marshal(item)
	if !s.budget.reserve(len(raw)) {
		return s.budget.err
	}
	st := &streamItem{budget: &s.budget, item: item, blocks: map[int]*streamBlock{}}
	s.items[index] = st
	if item.Type == "function_call" {
		if item.CallID == "" || item.Name == "" {
			return fmt.Errorf("invalid streamed tool call")
		}
		if !st.reserveBlock() {
			return st.budget.err
		}
		b := &streamBlock{index: -1, kind: "tool_use", start: object{"type": "tool_use", "id": item.CallID, "name": item.Name, "input": object{}}}
		st.blocks[0] = b
		st.queueEvent(queuedEvent{name: "content_block_start", block: b})
		if item.Arguments != "" {
			return st.appendValue(b, item.Arguments)
		}
	}
	return nil
}

func (st *streamItem) addPart(index int, part upstreamPart) (*streamBlock, error) {
	if st.item.Type != "message" {
		return nil, fmt.Errorf("text part in function call")
	}
	if _, ok := st.blocks[index]; ok {
		return nil, fmt.Errorf("duplicate content part")
	}
	if index != len(st.blocks) {
		return nil, fmt.Errorf("nonsequential content part")
	}
	if index > 0 && !st.blocks[index-1].closed {
		return nil, fmt.Errorf("overlapping text parts")
	}
	value, err := part.value()
	if err != nil {
		return nil, err
	}
	if !st.reserveBlock() {
		return nil, st.budget.err
	}
	b := &streamBlock{index: -1, kind: part.Type, start: object{"type": "text", "text": ""}}
	st.blocks[index] = b
	st.queueEvent(queuedEvent{name: "content_block_start", block: b})
	return b, st.appendValue(b, value)
}

func (st *streamItem) appendValue(b *streamBlock, value string) error {
	if value == "" {
		return nil
	}
	if b.closed || st.done {
		return fmt.Errorf("delta after block closed")
	}
	if !st.budget.reserve(len(value)) {
		return st.budget.err
	}
	b.buffer.WriteString(value)
	b.value = b.buffer.String()
	st.queueEvent(queuedEvent{name: "content_block_delta", block: b, delta: value})
	return nil
}

func (st *streamItem) reconcile(b *streamBlock, value string) error {
	if !strings.HasPrefix(value, b.value) {
		return fmt.Errorf("upstream final content conflicts with deltas")
	}
	return st.appendValue(b, strings.TrimPrefix(value, b.value))
}

func (st *streamItem) closeBlock(b *streamBlock) {
	if !b.closed {
		b.closed = true
		st.queueEvent(queuedEvent{name: "content_block_stop", block: b})
	}
}

func (s *streamState) content(e streamEvent) error {
	i, err := eventIndex(e.OutputIndex)
	if err != nil {
		return err
	}
	st := s.items[i]
	if st == nil || st.done {
		return fmt.Errorf("event references missing or completed output item")
	}
	if strings.HasPrefix(e.Type, "response.function_call_arguments.") {
		if st.item.Type != "function_call" {
			return fmt.Errorf("arguments for non-function item")
		}
		b := st.blocks[0]
		if e.Type == "response.function_call_arguments.delta" {
			return st.appendValue(b, e.Delta)
		}
		return st.reconcile(b, e.Arguments)
	}
	ci, err := eventIndex(e.ContentIndex)
	if err != nil {
		return err
	}
	if e.Type == "response.content_part.added" {
		_, err = st.addPart(ci, e.Part)
		return err
	}
	b := st.blocks[ci]
	if b == nil {
		return fmt.Errorf("event references missing content part")
	}
	if e.Type == "response.content_part.done" {
		if b.kind != e.Part.Type {
			return fmt.Errorf("content type changed")
		}
		v, err := e.Part.value()
		if err != nil {
			return err
		}
		if err = st.reconcile(b, v); err != nil {
			return err
		}
		st.addCitations(b, e.Part)
		st.closeBlock(b)
		return nil
	}
	if (e.Type == "response.output_text.delta" && b.kind != "output_text") || (e.Type == "response.refusal.delta" && b.kind != "refusal") {
		return fmt.Errorf("delta type mismatch")
	}
	return st.appendValue(b, e.Delta)
}

func (s *streamState) finishItem(index int, item upstreamItem) error {
	st := s.items[index]
	if st == nil {
		if err := s.addItem(index, item); err != nil {
			return err
		}
		st = s.items[index]
	}
	if st.item.Type != item.Type || (st.item.ID != "" && item.ID != st.item.ID) {
		return fmt.Errorf("output item identity changed")
	}
	if item.Type == "reasoning" {
		if err := st.finishThinking(item); err != nil {
			return err
		}
	} else if item.Type == "function_call" {
		if st.item.CallID != item.CallID || st.item.Name != item.Name {
			return fmt.Errorf("tool identity changed")
		}
		if _, err := rawObject([]byte(item.Arguments)); err != nil && !s.allowPartial {
			return fmt.Errorf("invalid upstream function arguments: %w", err)
		}
		b := st.blocks[0]
		if err := st.reconcile(b, item.Arguments); err != nil {
			return err
		}
		st.closeBlock(b)
	} else if item.Type == "web_search_call" || item.Type == "code_interpreter_call" || item.Type == "file_search_call" {
		if !st.done {
			for ci, block := range item.serverBlocks() {
				if !st.reserveBlock() {
					return st.budget.err
				}
				encoded, _ := json.Marshal(block)
				if !st.budget.reserve(len(encoded)) {
					return st.budget.err
				}
				b := &streamBlock{index: -1, kind: "server", start: block}
				st.blocks[ci] = b
				st.queueEvent(queuedEvent{name: "content_block_start", block: b})
				st.closeBlock(b)
			}
		}
	} else {
		for ci, part := range item.Content {
			b := st.blocks[ci]
			if b == nil {
				var err error
				b, err = st.addPart(ci, part)
				if err != nil {
					return err
				}
			}
			if b.kind != part.Type {
				return fmt.Errorf("content type changed")
			}
			v, err := part.value()
			if err != nil {
				return err
			}
			if err = st.reconcile(b, v); err != nil {
				return err
			}
			st.addCitations(b, part)
			st.closeBlock(b)
		}
		if len(st.blocks) != len(item.Content) {
			return fmt.Errorf("final content parts differ")
		}
	}
	st.done = true
	return nil
}

func (s *streamState) flush() error {
	if s.budget.err != nil {
		return s.budget.err
	}
	for {
		st := s.items[s.nextItem]
		if st == nil {
			return nil
		}
		for _, event := range st.queue {
			b := event.block
			payload := object{"type": event.name}
			name := event.name
			switch event.name {
			case "content_block_start":
				b.index = s.nextBlock
				s.nextBlock++
				payload["content_block"] = b.start
				if s.chat && b.kind == "refusal" {
					b.start["refusal"] = true
				}
			case "content_block_delta":
				if b.kind == "tool_use" {
					payload["delta"] = object{"type": "input_json_delta", "partial_json": event.delta}
				} else if b.kind == "thinking" {
					payload["delta"] = object{"type": "thinking_delta", "thinking": event.delta}
				} else {
					payload["delta"] = object{"type": "text_delta", "text": event.delta}
				}
			case "signature_delta":
				name = "content_block_delta"
				payload["type"] = name
				payload["delta"] = object{"type": "signature_delta", "signature": event.delta}
			case "citations_delta":
				name = "content_block_delta"
				payload["type"] = name
				payload["delta"] = object{"type": "citations_delta", "citation": event.citation}
			}
			payload["index"] = b.index
			if err := s.emit(name, payload); err != nil {
				return err
			}
		}
		for _, event := range st.queue {
			s.budget.queued--
			s.budget.bytes -= queuedBytes(event)
		}
		st.queue = nil
		if !st.done {
			return nil
		}
		s.nextItem++
	}
}

func (st *streamItem) addCitations(b *streamBlock, p upstreamPart) {
	if b.citationsDone {
		return
	}
	cs := p.citations()
	if len(cs) == 0 {
		return
	}
	b.citationsDone = true
	for _, c := range cs {
		st.queueEvent(queuedEvent{name: "citations_delta", block: b, citation: c})
	}
}
