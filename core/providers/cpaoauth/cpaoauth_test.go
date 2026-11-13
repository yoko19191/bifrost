package cpaoauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProviderAndCatalogListModels(t *testing.T) {
	providers := []schemas.ModelProvider{
		schemas.Antigravity,
		schemas.Codex,
		schemas.ClaudeOAuth,
		schemas.GrokOAuth,
	}

	for _, prov := range providers {
		t.Run(string(prov), func(t *testing.T) {
			p, err := NewProvider(prov, &schemas.ProviderConfig{}, nil)
			require.NoError(t, err)
			assert.Equal(t, prov, p.GetProviderKey())

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, bErr := p.ListModels(ctx, nil, &schemas.BifrostListModelsRequest{
				Provider:   prov,
				Unfiltered: true,
			})
			require.Nil(t, bErr)
			require.NotNil(t, resp)
			assert.NotEmpty(t, resp.Data, "expected built-in catalog models for %s", prov)
		})
	}
}

func TestStartBrowserOAuthFlows(t *testing.T) {
	ctx := context.Background()
	for _, prov := range []schemas.ModelProvider{schemas.Antigravity, schemas.Codex, schemas.ClaudeOAuth} {
		t.Run(string(prov), func(t *testing.T) {
			res, err := StartOAuthFlow(ctx, prov, "")
			require.NoError(t, err)
			assert.Equal(t, string(prov), res.Provider)
			assert.Equal(t, "browser", res.FlowType)
			assert.NotEmpty(t, res.AuthURL)
			assert.NotEmpty(t, res.State)
			if prov != schemas.Antigravity {
				assert.NotEmpty(t, res.Verifier)
			}
			assert.NotEmpty(t, res.RedirectURI)
		})
	}
}

func TestExtractCodeAndState(t *testing.T) {
	code, state, err := extractCodeAndState(OAuthCompleteRequest{
		CallbackURL: "http://localhost:1455/auth/callback?code=auth_code_123&state=state_xyz",
	})
	require.NoError(t, err)
	assert.Equal(t, "auth_code_123", code)
	assert.Equal(t, "state_xyz", state)

	// Claude code#state format
	code, state, err = extractCodeAndState(OAuthCompleteRequest{
		CallbackURL: "claude_code_abc#claude_state_def",
	})
	require.NoError(t, err)
	assert.Equal(t, "claude_code_abc", code)
	assert.Equal(t, "claude_state_def", state)
}

func TestCodexProviderChatAndResponsesExecution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer codex-test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"status\":\"in_progress\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello from Codex OAuth!\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello from Codex OAuth!\"}]}],\"usage\":{\"input_tokens\":8,\"output_tokens\":5,\"total_tokens\":13}}}\n\n")
	}))
	defer srv.Close()

	p, err := NewProvider(schemas.Codex, &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: srv.URL,
		},
	}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		ID:    "codex-key-1",
		Name:  "Codex Test Account",
		Value: *schemas.NewSecretVar(`{"type":"codex","access_token":"codex-test-token","refresh_token":"rt-test","account_id":"acct-123","email":"user@openai.com"}`),
	}
	msg := "Hello"
	resp, bErr := p.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.Codex,
		Model:    "gpt-5.4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &msg,
				},
			},
		},
	})
	require.Nil(t, bErr)
	require.NotNil(t, resp)
	require.Len(t, resp.Choices, 1)
	require.NotNil(t, resp.Choices[0].ChatNonStreamResponseChoice)
	require.NotNil(t, resp.Choices[0].ChatNonStreamResponseChoice.Message)
	require.NotNil(t, resp.Choices[0].ChatNonStreamResponseChoice.Message.Content)
	require.NotNil(t, resp.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)
	assert.Equal(t, "Hello from Codex OAuth!", *resp.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)
}
