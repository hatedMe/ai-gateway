package translate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type thinkingSummary struct {
	text string
	done bool
}

func (st *streamItem) thinkingBlock() *streamBlock {
	b := st.blocks[0]
	if b == nil {
		b = &streamBlock{index: -1, kind: "thinking", start: object{"type": "thinking", "thinking": ""}}
		st.blocks[0] = b
		st.queue = append(st.queue, queuedEvent{name: "content_block_start", block: b})
	}
	return b
}

func (st *streamItem) addSummary(index int) error {
	if index != len(st.summaries) || (index > 0 && !st.summaries[index-1].done) {
		return fmt.Errorf("nonsequential reasoning summary")
	}
	if st.done {
		return fmt.Errorf("summary after completed reasoning")
	}
	b := st.thinkingBlock()
	if index > 0 {
		if err := st.appendValue(b, "\n\n"); err != nil {
			return err
		}
	}
	st.summaries = append(st.summaries, thinkingSummary{})
	return nil
}

func (st *streamItem) updateSummary(index int, value string, delta, done bool) error {
	if index == len(st.summaries) {
		if err := st.addSummary(index); err != nil {
			return err
		}
	}
	if index < 0 || index >= len(st.summaries) {
		return fmt.Errorf("missing reasoning summary")
	}
	part := &st.summaries[index]
	suffix := value
	if !delta {
		if !strings.HasPrefix(value, part.text) {
			return fmt.Errorf("final reasoning summary conflicts with deltas")
		}
		suffix = strings.TrimPrefix(value, part.text)
	}
	if suffix != "" && part.done {
		return fmt.Errorf("delta after reasoning summary completed")
	}
	if err := st.appendValue(st.thinkingBlock(), suffix); err != nil {
		return err
	}
	part.text += suffix
	if done {
		part.done = true
	}
	return nil
}

func (s *streamState) thinking(e streamEvent) error {
	i, err := eventIndex(e.OutputIndex)
	if err != nil {
		return err
	}
	st := s.items[i]
	if st == nil || st.done || st.item.Type != "reasoning" {
		return fmt.Errorf("reasoning event references missing or completed reasoning item")
	}
	index, err := eventIndex(e.SummaryIndex)
	if err != nil {
		return err
	}
	switch e.Type {
	case "response.reasoning_summary_part.added":
		if e.Part.Type != "summary_text" {
			return fmt.Errorf("unsupported reasoning summary type")
		}
		if err = st.addSummary(index); err != nil {
			return err
		}
		return st.updateSummary(index, e.Part.Text, false, false)
	case "response.reasoning_summary_part.done":
		if e.Part.Type != "summary_text" {
			return fmt.Errorf("unsupported reasoning summary type")
		}
		return st.updateSummary(index, e.Part.Text, false, true)
	case "response.reasoning_summary_text.delta":
		return st.updateSummary(index, e.Delta, true, false)
	case "response.reasoning_summary_text.done":
		return st.updateSummary(index, e.Text, false, true)
	}
	return nil
}

func (st *streamItem) finishThinking(item upstreamItem) error {
	if len(item.Summary) == 0 && len(item.Content) == 0 && st.direct != "" {
		item.Content = []upstreamPart{{Type: "reasoning_text", Text: st.direct}}
	}
	final, err := item.thinkingBlock()
	if err != nil {
		return err
	}
	if final == nil {
		if len(st.blocks) > 0 {
			return fmt.Errorf("reasoning disappeared from final item")
		}
		return nil
	}
	for i, part := range item.Summary {
		if err = st.updateSummary(i, part.Text, false, true); err != nil {
			return err
		}
	}
	if len(item.Summary) == 0 {
		if err = st.reconcile(st.thinkingBlock(), final["thinking"].(string)); err != nil {
			return err
		}
	}
	if len(st.summaries) != len(item.Summary) {
		return fmt.Errorf("final reasoning summary count differs")
	}
	b := st.thinkingBlock()
	signature := final["signature"].(string)
	if b.signature != "" {
		if b.signature != signature && !sameReasoningState(b.signature, signature) {
			return fmt.Errorf("final reasoning signature changed")
		}
		return nil
	}
	if st.done {
		return fmt.Errorf("reasoning changed after completion")
	}
	b.signature = signature
	st.queue = append(st.queue, queuedEvent{name: "signature_delta", block: b, delta: signature})
	st.closeBlock(b)
	return nil
}

// Providers may re-encrypt an unchanged reasoning item for the terminal event.
// Keep the already emitted item.done carrier; opaque ciphertext is not an ID.
func sameReasoningState(a, b string) bool {
	decode := func(signature string) (reasoningCarrier, error) {
		var c reasoningCarrier
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(signature, reasoningSignaturePrefix))
		if err != nil {
			return c, err
		}
		err = json.Unmarshal(raw, &c)
		return c, err
	}
	left, err := decode(a)
	if err != nil {
		return false
	}
	right, err := decode(b)
	if err != nil {
		return false
	}
	if left.EncryptedContent == "" || right.EncryptedContent == "" {
		return false
	}
	left.EncryptedContent = ""
	right.EncryptedContent = ""
	return reflect.DeepEqual(left, right)
}
