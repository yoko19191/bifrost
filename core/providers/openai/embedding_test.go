package openai_test

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// The OpenAI converter is shared by OpenAI-compatible providers, so its errors must not name openai.
func TestToOpenAIEmbeddingRequestErrorsDoNotNameOpenAI(t *testing.T) {
	text := "hello"
	dims := 8
	cases := map[string][]schemas.EmbeddingInputItem{
		"image input": {
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/a.png")}}}},
		},
		"mixed text and tokens": {
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}, {Type: schemas.EmbeddingContentPartTypeTokens, Tokens: []int{1, 2}}}},
		},
		"per-item params": {
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}, Params: &schemas.EmbeddingParameters{Dimensions: &dims}},
		},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := openai.ToOpenAIEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
				Provider: schemas.Azure,
				Model:    "text-embedding-3-small",
				Input:    input,
			}, schemas.Azure)
			require.Error(t, err)
			require.Contains(t, err.Error(), "this provider")
			require.NotContains(t, err.Error(), "openai")
		})
	}
}

// task_type/title/auto_truncate are not OpenAI wire fields; strict OpenAI-compatible
// providers (Mistral) reject them with 422, so only encoding_format and dimensions go out.
func TestToOpenAIEmbeddingRequestSendsOnlyOpenAIParams(t *testing.T) {
	text := "hello"
	dims := 8
	format := "float"
	req, err := openai.ToOpenAIEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Provider: schemas.Mistral,
		Model:    "mistral-embed",
		Input:    []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
		Params: &schemas.EmbeddingParameters{
			EncodingFormat: &format,
			Dimensions:     &dims,
			TaskType:       schemas.Ptr("RETRIEVAL_QUERY"),
			Title:          schemas.Ptr("t"),
			AutoTruncate:   schemas.Ptr(true),
		},
	}, schemas.Mistral)
	require.NoError(t, err)

	body, err := schemas.MarshalSorted(req)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"mistral-embed","input":"hello","encoding_format":"float","dimensions":8}`, string(body))
}

func TestOpenAIEmbeddingRequestMultimodalAndMessages(t *testing.T) {
	t.Run("flat OpenAI multimodal parts in input", func(t *testing.T) {
		var req openai.OpenAIEmbeddingRequest
		err := json.Unmarshal([]byte(`{
			"model": "cloudflare/@cf/google/embeddinggemma-2",
			"input": [
				{"type": "text", "text": "a shoe"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}},
				{"type": "video_url", "video_url": {"url": "data:video/mp4;base64,BBBB"}},
				{"type": "input_audio", "input_audio": {"data": "CCCC", "format": "wav"}},
				{"type": "audio_url", "audio_url": {"url": "data:audio/mp3;base64,DDDD"}}
			],
			"dimensions": 256,
			"encoding_format": "base64"
		}`), &req)
		require.NoError(t, err)

		bfReq := req.ToBifrostEmbeddingRequest(nil)
		require.Equal(t, schemas.Cloudflare, bfReq.Provider)
		require.Equal(t, "@cf/google/embeddinggemma-2", bfReq.Model)
		require.Len(t, bfReq.Input, 1)
		require.Len(t, bfReq.Input[0].Content, 5)
		require.Equal(t, 256, *bfReq.Params.Dimensions)
		require.Equal(t, "base64", *bfReq.Params.EncodingFormat)
		require.NoError(t, schemas.ValidateEmbeddingInput(bfReq.Input))
	})

	t.Run("batch of multimodal part arrays in input", func(t *testing.T) {
		var req openai.OpenAIEmbeddingRequest
		err := json.Unmarshal([]byte(`{
			"model": "cloudflare/embeddinggemma-2",
			"input": [
				[{"type": "text", "text": "shoe"}, {"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}],
				[{"type": "text", "text": "query"}]
			]
		}`), &req)
		require.NoError(t, err)

		bfReq := req.ToBifrostEmbeddingRequest(nil)
		require.Len(t, bfReq.Input, 2)
		require.Len(t, bfReq.Input[0].Content, 2)
		require.Len(t, bfReq.Input[1].Content, 1)
		require.NoError(t, schemas.ValidateEmbeddingInput(bfReq.Input))
	})

	t.Run("per-item content and params object and batch", func(t *testing.T) {
		var req openai.OpenAIEmbeddingRequest
		err := json.Unmarshal([]byte(`{
			"model": "cloudflare/@cf/google/embeddinggemma-2",
			"input": [
				{"content": "where is my order", "params": {"task_type": "RETRIEVAL_QUERY"}},
				{"content": [{"type": "text", "text": "order FAQ"}], "params": {"task_type": "RETRIEVAL_DOCUMENT", "title": "Support"}}
			]
		}`), &req)
		require.NoError(t, err)

		bfReq := req.ToBifrostEmbeddingRequest(nil)
		require.Len(t, bfReq.Input, 2)
		require.Equal(t, "RETRIEVAL_QUERY", *bfReq.Input[0].Params.TaskType)
		require.Equal(t, "Support", *bfReq.Input[1].Params.Title)
	})

	t.Run("top-level messages single and batch conversations", func(t *testing.T) {
		var single openai.OpenAIEmbeddingRequest
		require.NoError(t, json.Unmarshal([]byte(`{
			"model": "cloudflare/@cf/google/embeddinggemma-2",
			"messages": [{"role": "user", "content": [{"type": "input_audio", "input_audio": {"data": "UklGRg==", "format": "wav"}}]}],
			"dimensions": 512
		}`), &single))
		bfSingle := single.ToBifrostEmbeddingRequest(nil)
		require.Len(t, bfSingle.Input, 1)
		require.Len(t, bfSingle.Input[0].Messages, 1)
		require.Equal(t, 512, *bfSingle.Params.Dimensions)

		var batch openai.OpenAIEmbeddingRequest
		require.NoError(t, json.Unmarshal([]byte(`{
			"model": "cloudflare/@cf/google/embeddinggemma-2",
			"messages": [
				[{"role": "system", "content": "ctx"}, {"role": "user", "content": "one"}],
				[{"role": "user", "content": "two"}]
			]
		}`), &batch))
		bfBatch := batch.ToBifrostEmbeddingRequest(nil)
		require.Len(t, bfBatch.Input, 2)
		require.Len(t, bfBatch.Input[0].Messages, 2)
		require.Len(t, bfBatch.Input[1].Messages, 1)
	})
}
