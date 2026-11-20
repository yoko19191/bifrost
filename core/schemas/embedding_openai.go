package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// EmbeddingMessage represents one chat-style turn inside a multimodal embedding request
// (such as Cloudflare Workers AI EmbeddingGemma 2 or vLLM pooling messages).
type EmbeddingMessage struct {
	Role    string           `json:"role"`
	Content EmbeddingContent `json:"content"`
}

// UnmarshalJSON accepts content as either a plain string or an array of content parts.
func (m *EmbeddingMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := Unmarshal(data, &raw); err != nil {
		return err
	}
	content, err := unmarshalEmbeddingMessageContent(raw.Content)
	if err != nil {
		return err
	}
	m.Role = raw.Role
	m.Content = content
	return nil
}

// EmbeddingMessagesInput holds one conversation ([]EmbeddingMessage) or a batch of
// conversations ([][]EmbeddingMessage), each producing one embedding vector.
type EmbeddingMessagesInput struct {
	Conversations [][]EmbeddingMessage
}

// UnmarshalJSON parses either a single conversation (array of messages) or a batch of
// conversations (array of arrays of messages).
func (e *EmbeddingMessagesInput) UnmarshalJSON(data []byte) error {
	*e = EmbeddingMessagesInput{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] != '[' {
		return fmt.Errorf("embedding messages must be an array of messages or conversations")
	}
	var elems []json.RawMessage
	if err := Unmarshal(trimmed, &elems); err != nil {
		return err
	}
	if len(elems) == 0 {
		return nil
	}
	first := bytes.TrimSpace(elems[0])
	if len(first) > 0 && first[0] == '[' {
		var batch [][]EmbeddingMessage
		if err := Unmarshal(trimmed, &batch); err != nil {
			return err
		}
		e.Conversations = batch
		return nil
	}
	var single []EmbeddingMessage
	if err := Unmarshal(trimmed, &single); err != nil {
		return err
	}
	e.Conversations = [][]EmbeddingMessage{single}
	return nil
}

// MarshalJSON emits a single conversation array when there is one conversation, or a
// nested array for multiple conversations.
func (e EmbeddingMessagesInput) MarshalJSON() ([]byte, error) {
	if len(e.Conversations) == 1 {
		return MarshalSorted(e.Conversations[0])
	}
	if len(e.Conversations) > 1 {
		return MarshalSorted(e.Conversations)
	}
	return Marshal(nil)
}

// IsEmpty reports whether no conversations are present.
func (e *EmbeddingMessagesInput) IsEmpty() bool {
	return e == nil || len(e.Conversations) == 0
}

// ToEmbeddingInput converts each conversation into an EmbeddingInputItem preserving the
// original Messages and populating Content with all flattened parts.
func (e *EmbeddingMessagesInput) ToEmbeddingInput() []EmbeddingInputItem {
	if e == nil || len(e.Conversations) == 0 {
		return nil
	}
	items := make([]EmbeddingInputItem, len(e.Conversations))
	for i, conv := range e.Conversations {
		items[i] = EmbeddingInputItem{
			Content:  flattenEmbeddingMessages(conv),
			Messages: conv,
		}
	}
	return items
}

type openAIEmbeddingPartProbe struct {
	Type       string          `json:"type"`
	ImageURL   json.RawMessage `json:"image_url,omitempty"`
	VideoURL   json.RawMessage `json:"video_url,omitempty"`
	AudioURL   json.RawMessage `json:"audio_url,omitempty"`
	InputAudio json.RawMessage `json:"input_audio,omitempty"`
}

// UnmarshalJSON decodes both canonical Bifrost EmbeddingContentPart objects and OpenAI-style
// multimodal content parts (image_url, video_url, audio_url, input_audio).
func (p *EmbeddingContentPart) UnmarshalJSON(data []byte) error {
	type alias EmbeddingContentPart
	var base alias
	if err := Unmarshal(data, &base); err != nil {
		return err
	}
	var probe openAIEmbeddingPartProbe
	if err := Unmarshal(data, &probe); err != nil {
		return err
	}

	switch strings.ToLower(strings.TrimSpace(probe.Type)) {
	case "image_url":
		base.Type = EmbeddingContentPartTypeImage
		if base.Image == nil {
			url, ok, err := parseMediaURLField(probe.ImageURL)
			if err != nil {
				return fmt.Errorf("invalid image_url payload: %w", err)
			}
			if ok {
				base.Image = &EmbeddingMediaPart{URL: &url}
			}
		}
	case "video_url":
		base.Type = EmbeddingContentPartTypeVideo
		if base.Video == nil {
			url, ok, err := parseMediaURLField(probe.VideoURL)
			if err != nil {
				return fmt.Errorf("invalid video_url payload: %w", err)
			}
			if ok {
				base.Video = &EmbeddingMediaPart{URL: &url}
			}
		}
	case "audio_url":
		base.Type = EmbeddingContentPartTypeAudio
		if base.Audio == nil {
			url, ok, err := parseMediaURLField(probe.AudioURL)
			if err != nil {
				return fmt.Errorf("invalid audio_url payload: %w", err)
			}
			if ok {
				base.Audio = &EmbeddingMediaPart{URL: &url}
			}
		}
	case "input_audio":
		base.Type = EmbeddingContentPartTypeAudio
		trimmedAudio := bytes.TrimSpace(probe.InputAudio)
		if base.Audio == nil && len(trimmedAudio) > 0 && !bytes.Equal(trimmedAudio, []byte("null")) {
			var ia struct {
				Data   string `json:"data"`
				Format string `json:"format"`
			}
			if err := Unmarshal(trimmedAudio, &ia); err != nil {
				return fmt.Errorf("invalid input_audio payload: %w", err)
			}
			dataCopy := ia.Data
			media := &EmbeddingMediaPart{Data: &dataCopy}
			if mime := audioFormatToMIME(ia.Format); mime != "" {
				media.MIMEType = &mime
			}
			base.Audio = media
		}
	}

	*p = EmbeddingContentPart(base)
	return nil
}

// UnmarshalEmbeddingMultimodalInput parses OpenAI-compatible multimodal embedding "input"
// shapes (single part object, single message object, flat array of content parts, array of
// messages, or batch of mixed text/tokens/part-arrays/conversations/per-item-param items).
func UnmarshalEmbeddingMultimodalInput(data []byte) ([]EmbeddingInputItem, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	switch trimmed[0] {
	case '{':
		item, err := unmarshalEmbeddingObjectItem(trimmed)
		if err != nil {
			return nil, err
		}
		return []EmbeddingInputItem{item}, nil
	case '[':
		var elems []json.RawMessage
		if err := Unmarshal(trimmed, &elems); err != nil {
			return nil, err
		}
		if len(elems) == 0 {
			return []EmbeddingInputItem{}, nil
		}
		allTypeOnly := true
		allMessages := true
		for _, elem := range elems {
			et := bytes.TrimSpace(elem)
			if len(et) == 0 || et[0] != '{' {
				allTypeOnly = false
				allMessages = false
				break
			}
			fields, ok := probeObjectFields(et)
			if !ok {
				allTypeOnly = false
				allMessages = false
				break
			}
			if !isObjectWithTypeOnlyFields(fields) {
				allTypeOnly = false
			}
			if !isObjectMessageFields(fields) {
				allMessages = false
			}
		}
		if allTypeOnly {
			var parts EmbeddingContent
			if err := Unmarshal(trimmed, &parts); err != nil {
				return nil, err
			}
			return []EmbeddingInputItem{{Content: parts}}, nil
		}
		if allMessages {
			var msgs []EmbeddingMessage
			if err := Unmarshal(trimmed, &msgs); err != nil {
				return nil, err
			}
			return []EmbeddingInputItem{{Content: flattenEmbeddingMessages(msgs), Messages: msgs}}, nil
		}

		items := make([]EmbeddingInputItem, len(elems))
		for idx, elem := range elems {
			item, err := unmarshalEmbeddingInputElement(elem, idx)
			if err != nil {
				return nil, err
			}
			items[idx] = item
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unsupported embedding input shape")
	}
}

func unmarshalEmbeddingInputElement(elem json.RawMessage, idx int) (EmbeddingInputItem, error) {
	et := bytes.TrimSpace(elem)
	if len(et) == 0 {
		return EmbeddingInputItem{}, fmt.Errorf("unsupported embedding input element at index %d", idx)
	}
	switch et[0] {
	case '"':
		var text string
		if err := Unmarshal(et, &text); err != nil {
			return EmbeddingInputItem{}, err
		}
		return EmbeddingInputItem{Content: EmbeddingContent{{Type: EmbeddingContentPartTypeText, Text: &text}}}, nil
	case '[':
		var inner []json.RawMessage
		if err := Unmarshal(et, &inner); err != nil {
			return EmbeddingInputItem{}, err
		}
		if len(inner) == 0 {
			return EmbeddingInputItem{}, nil
		}
		var tokens []int
		if err := Unmarshal(et, &tokens); err == nil {
			return EmbeddingInputItem{Content: EmbeddingContent{{Type: EmbeddingContentPartTypeTokens, Tokens: tokens}}}, nil
		}
		if isMessageArrayJSON(et) {
			var msgs []EmbeddingMessage
			if err := Unmarshal(et, &msgs); err != nil {
				return EmbeddingInputItem{}, err
			}
			return EmbeddingInputItem{Content: flattenEmbeddingMessages(msgs), Messages: msgs}, nil
		}
		var parts EmbeddingContent
		if err := Unmarshal(et, &parts); err != nil {
			return EmbeddingInputItem{}, err
		}
		return EmbeddingInputItem{Content: parts}, nil
	case '{':
		return unmarshalEmbeddingObjectItem(et)
	default:
		return EmbeddingInputItem{}, fmt.Errorf("unsupported embedding input element at index %d", idx)
	}
}

func unmarshalEmbeddingObjectItem(data []byte) (EmbeddingInputItem, error) {
	fields, ok := probeObjectFields(data)
	if ok {
		if isObjectMessageFields(fields) {
			var msg EmbeddingMessage
			if err := Unmarshal(data, &msg); err != nil {
				return EmbeddingInputItem{}, err
			}
			return EmbeddingInputItem{Content: msg.Content, Messages: []EmbeddingMessage{msg}}, nil
		}
		if isObjectItemFields(fields) {
			var item EmbeddingInputItem
			if err := Unmarshal(data, &item); err != nil {
				return EmbeddingInputItem{}, err
			}
			return item, nil
		}
	}
	var part EmbeddingContentPart
	if err := Unmarshal(data, &part); err != nil {
		return EmbeddingInputItem{}, err
	}
	return EmbeddingInputItem{Content: EmbeddingContent{part}}, nil
}

func unmarshalEmbeddingMessageContent(raw json.RawMessage) (EmbeddingContent, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		return EmbeddingContent{{Type: EmbeddingContentPartTypeText, Text: &text}}, nil
	}
	var content EmbeddingContent
	if err := Unmarshal(trimmed, &content); err != nil {
		return nil, err
	}
	return content, nil
}

func flattenEmbeddingMessages(msgs []EmbeddingMessage) EmbeddingContent {
	total := 0
	for _, msg := range msgs {
		total += len(msg.Content)
	}
	if total == 0 {
		return nil
	}
	out := make(EmbeddingContent, 0, total)
	for _, msg := range msgs {
		out = append(out, msg.Content...)
	}
	return out
}

func isMessageArrayJSON(data []byte) bool {
	var elems []json.RawMessage
	if err := Unmarshal(data, &elems); err != nil || len(elems) == 0 {
		return false
	}
	for _, elem := range elems {
		fields, ok := probeObjectFields(elem)
		if !ok || !isObjectMessageFields(fields) {
			return false
		}
	}
	return true
}

func probeObjectFields(data []byte) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := Unmarshal(trimmed, &fields); err != nil {
		return nil, false
	}
	return fields, true
}

func isObjectWithTypeOnlyFields(fields map[string]json.RawMessage) bool {
	_, hasType := fields["type"]
	_, hasContent := fields["content"]
	_, hasRole := fields["role"]
	return hasType && !hasContent && !hasRole
}

func isObjectMessageFields(fields map[string]json.RawMessage) bool {
	_, hasRole := fields["role"]
	_, hasContent := fields["content"]
	return hasRole && hasContent
}

func isObjectItemFields(fields map[string]json.RawMessage) bool {
	_, hasContent := fields["content"]
	_, hasMessages := fields["messages"]
	return hasContent || hasMessages
}

func parseMediaURLField(raw json.RawMessage) (string, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false, nil
	}
	switch trimmed[0] {
	case '"':
		var url string
		if err := Unmarshal(trimmed, &url); err != nil {
			return "", false, err
		}
		return url, true, nil
	case '{':
		var obj struct {
			URL *string `json:"url"`
		}
		if err := Unmarshal(trimmed, &obj); err != nil {
			return "", false, err
		}
		if obj.URL == nil {
			return "", false, nil
		}
		return *obj.URL, true, nil
	default:
		return "", false, fmt.Errorf("expected string or object with url")
	}
}

func audioFormatToMIME(format string) string {
	f := strings.ToLower(strings.TrimSpace(format))
	if f == "" {
		return ""
	}
	if strings.Contains(f, "/") {
		return f
	}
	switch f {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "webm":
		return "audio/webm"
	case "m4a", "mp4":
		return "audio/mp4"
	default:
		return "audio/" + f
	}
}
