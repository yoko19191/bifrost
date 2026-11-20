package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func embeddingCtx(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/embeddings")
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBodyString(body)
	return ctx
}

func TestPrepareEmbeddingRequest_MultimodalAndMessages(t *testing.T) {
	t.Run("accepts OpenAI multimodal parts in input", func(t *testing.T) {
		_, bifrostReq, err := prepareEmbeddingRequest(embeddingCtx(`{
			"model": "cloudflare/embeddinggemma-2",
			"input": [
				{"type": "text", "text": "trail runner"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}
			],
			"dimensions": 256,
			"encoding_format": "base64"
		}`), nil)
		require.NoError(t, err)
		require.Equal(t, schemas.Cloudflare, bifrostReq.Provider)
		require.Equal(t, "embeddinggemma-2", bifrostReq.Model)
		require.Len(t, bifrostReq.Input, 1)
		require.Len(t, bifrostReq.Input[0].Content, 2)
		require.Equal(t, 256, *bifrostReq.Params.Dimensions)
		require.Equal(t, "base64", *bifrostReq.Params.EncodingFormat)
	})

	t.Run("accepts top-level messages without leaking into ExtraParams", func(t *testing.T) {
		_, bifrostReq, err := prepareEmbeddingRequest(embeddingCtx(`{
			"model": "cloudflare/@cf/google/embeddinggemma-2",
			"messages": [
				{"role": "user", "content": [{"type": "input_audio", "input_audio": {"data": "UklGRg==", "format": "wav"}}]}
			],
			"dimensions": 256
		}`), nil)
		require.NoError(t, err)
		require.Len(t, bifrostReq.Input, 1)
		require.Len(t, bifrostReq.Input[0].Messages, 1)
		_, leaked := bifrostReq.Params.ExtraParams["messages"]
		require.False(t, leaked)
	})

	t.Run("rejects both input and messages", func(t *testing.T) {
		_, _, err := prepareEmbeddingRequest(embeddingCtx(`{
			"model": "cloudflare/@cf/google/embeddinggemma-2",
			"input": "hello",
			"messages": [{"role": "user", "content": "world"}]
		}`), nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "both input and messages")
	})
}
