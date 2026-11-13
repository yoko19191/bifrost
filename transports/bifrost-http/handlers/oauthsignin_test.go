package handlers

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/cpaoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestOAuthChannelSignInHandler(t *testing.T) {
	h := NewOAuthChannelSignInHandler(nil)

	for _, prov := range []string{"antigravity", "codex", "claude"} {
		t.Run("start_"+prov, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.SetUserValue("provider", prov)
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			h.startFlow(ctx)
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

			var res cpaoauth.OAuthStartResponse
			require.NoError(t, json.Unmarshal(ctx.Response.Body(), &res))
			assert.Equal(t, prov, res.Provider)
			assert.Equal(t, "browser", res.FlowType)
			assert.NotEmpty(t, res.AuthURL)
			assert.NotEmpty(t, res.State)
		})
	}

	t.Run("import_antigravity", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue("provider", "antigravity")
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.SetBodyString(`{"raw_json":"{\"type\":\"antigravity\",\"access_token\":\"ya29.test\",\"refresh_token\":\"1//test\",\"account_id\":\"12345\",\"email\":\"dev@example.com\",\"project_id\":\"proj-abc\",\"expired\":\"2099-01-01T00:00:00Z\"}"}`)
		h.importCredential(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

		var res cpaoauth.OAuthCredentialResult
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &res))
		assert.Equal(t, "authorized", res.Status)
		assert.Equal(t, "dev@example.com", res.Email)
		assert.Equal(t, "proj-abc", res.ProjectID)
		assert.Contains(t, res.CredentialJSON, `"access_token":"ya29.test"`)
	})
}
