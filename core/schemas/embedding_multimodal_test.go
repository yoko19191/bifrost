package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddingInputValidateRejectsEmpty(t *testing.T) {
	err := ValidateEmbeddingInput(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

func TestEmbeddingContentPartValidateRejectsMultipleModalities(t *testing.T) {
	text := "bad"
	part := EmbeddingContentPart{
		Type:  EmbeddingContentPartTypeImage,
		Text:  &text,
		Image: &EmbeddingMediaPart{URL: Ptr("https://example.com/img.png")},
	}

	err := part.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one modality")
}

func TestEmbeddingContentPartUnmarshalOpenAIParts(t *testing.T) {
	raw := `[
		{"type":"text","text":"caption"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},
		{"type":"video_url","video_url":{"url":"data:video/mp4;base64,BBBB"}},
		{"type":"audio_url","audio_url":{"url":"data:audio/wav;base64,CCCC"}},
		{"type":"input_audio","input_audio":{"data":"DDDD","format":"mp3"}}
	]`
	var content EmbeddingContent
	require.NoError(t, json.Unmarshal([]byte(raw), &content))
	require.NoError(t, content.Validate())
	require.Len(t, content, 5)
	require.Equal(t, EmbeddingContentPartTypeText, content[0].Type)
	require.Equal(t, "caption", *content[0].Text)
	require.Equal(t, EmbeddingContentPartTypeImage, content[1].Type)
	require.Equal(t, "data:image/png;base64,AAAA", *content[1].Image.URL)
	require.Equal(t, EmbeddingContentPartTypeVideo, content[2].Type)
	require.Equal(t, "data:video/mp4;base64,BBBB", *content[2].Video.URL)
	require.Equal(t, EmbeddingContentPartTypeAudio, content[3].Type)
	require.Equal(t, "data:audio/wav;base64,CCCC", *content[3].Audio.URL)
	require.Equal(t, EmbeddingContentPartTypeAudio, content[4].Type)
	require.Equal(t, "DDDD", *content[4].Audio.Data)
	require.Equal(t, "audio/mpeg", *content[4].Audio.MIMEType)
}

func TestUnmarshalEmbeddingMultimodalInputShapes(t *testing.T) {
	t.Run("flat OpenAI parts array produces one combined item", func(t *testing.T) {
		items, err := UnmarshalEmbeddingMultimodalInput([]byte(`[
			{"type":"text","text":"shoes"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}
		]`))
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Len(t, items[0].Content, 2)
		require.NoError(t, ValidateEmbeddingInput(items))
	})

	t.Run("batch of part arrays and strings produces N items", func(t *testing.T) {
		items, err := UnmarshalEmbeddingMultimodalInput([]byte(`[
			[{"type":"text","text":"shoes"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}],
			"query text",
			{"content":"doc text","params":{"task_type":"RETRIEVAL_DOCUMENT","title":"Doc"}}
		]`))
		require.NoError(t, err)
		require.Len(t, items, 3)
		require.Len(t, items[0].Content, 2)
		require.Equal(t, "query text", *items[1].Content[0].Text)
		require.Equal(t, "doc text", *items[2].Content[0].Text)
		require.NotNil(t, items[2].Params)
		require.Equal(t, "RETRIEVAL_DOCUMENT", *items[2].Params.TaskType)
		require.NoError(t, ValidateEmbeddingInput(items))
	})

	t.Run("single part object produces one item", func(t *testing.T) {
		items, err := UnmarshalEmbeddingMultimodalInput([]byte(`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}`))
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, EmbeddingContentPartTypeImage, items[0].Content[0].Type)
		require.NoError(t, ValidateEmbeddingInput(items))
	})
}

func TestEmbeddingMessagesInput(t *testing.T) {
	t.Run("single conversation", func(t *testing.T) {
		var msgs EmbeddingMessagesInput
		require.NoError(t, json.Unmarshal([]byte(`[
			{"role":"system","content":"be concise"},
			{"role":"user","content":[{"type":"text","text":"look"},{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]}
		]`), &msgs))
		items := msgs.ToEmbeddingInput()
		require.Len(t, items, 1)
		require.Len(t, items[0].Messages, 2)
		require.Len(t, items[0].Content, 3)
		require.NoError(t, ValidateEmbeddingInput(items))
	})

	t.Run("batch of conversations", func(t *testing.T) {
		var msgs EmbeddingMessagesInput
		require.NoError(t, json.Unmarshal([]byte(`[
			[{"role":"user","content":"first"}],
			[{"role":"user","content":[{"type":"text","text":"second"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]
		]`), &msgs))
		items := msgs.ToEmbeddingInput()
		require.Len(t, items, 2)
		require.Len(t, items[0].Messages, 1)
		require.Len(t, items[1].Messages, 1)
		require.Len(t, items[1].Content, 2)
		require.NoError(t, ValidateEmbeddingInput(items))
	})
}

