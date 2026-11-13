package bifrost

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestEffortModelFusionModeAAndModeB(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Key 1: Mode A fused alias "claude-opus-4-6" -> {low, medium, high}
	keyModeA := schemas.Key{
		ID:     "k1",
		Name:   "mode-a-key",
		Models: schemas.WhiteList{"claude-opus-4-6"},
		Aliases: schemas.KeyAliases{
			"claude-opus-4-6": {
				ModelID: "claude-opus-4-6-medium",
				EffortModels: &schemas.EffortModels{
					Low:     "claude-opus-4-6-low",
					Medium:  "claude-opus-4-6-medium",
					High:    "claude-opus-4-6-high",
					Default: "medium",
				},
			},
		},
	}

	// Verify keySupportsModel admits both "claude-opus-4-6" (Mode A) and "claude-opus-4-6-high" (Mode B virtual suffix)
	if !keySupportsModel(schemas.Anthropic, &keyModeA, "claude-opus-4-6") {
		t.Fatal("keySupportsModel should admit base alias claude-opus-4-6")
	}
	if !keySupportsModel(schemas.Anthropic, &keyModeA, "claude-opus-4-6-high") {
		t.Fatal("keySupportsModel should admit virtual suffix claude-opus-4-6-high via base alias")
	}

	// Test Mode A: request "claude-opus-4-6" with explicit reasoning.effort = "high"
	reqA := &ChannelMessage{
		Context: ctx,
		BifrostRequest: schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.Anthropic,
				Model:    "claude-opus-4-6",
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
		},
	}
	callerEffort, callerMaxTokens := extractRequestReasoning(&reqA.BifrostRequest)
	injected := false
	var bf *Bifrost
	resolved := bf.resolveAttemptModel(reqA, keyModeA, schemas.Anthropic, "claude-opus-4-6", callerEffort, callerMaxTokens, &injected)
	if resolved != "claude-opus-4-6-high" || reqA.ChatRequest.Model != "claude-opus-4-6-high" {
		t.Fatalf("Mode A resolved = %q (req.Model=%q), want claude-opus-4-6-high", resolved, reqA.ChatRequest.Model)
	}

	// Test Mode B + Mode A combined: request "claude-opus-4-6-low" with no explicit reasoning param
	reqAB := &ChannelMessage{
		Context: ctx,
		BifrostRequest: schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.Anthropic,
				Model:    "claude-opus-4-6-low",
			},
		},
	}
	callerEffort, callerMaxTokens = extractRequestReasoning(&reqAB.BifrostRequest)
	injected = false
	resolved = bf.resolveAttemptModel(reqAB, keyModeA, schemas.Anthropic, "claude-opus-4-6-low", callerEffort, callerMaxTokens, &injected)
	if resolved != "claude-opus-4-6-low" {
		t.Fatalf("Mode B+A resolved = %q, want claude-opus-4-6-low", resolved)
	}
	if reqAB.ChatRequest.Params == nil || reqAB.ChatRequest.Params.Reasoning == nil || reqAB.ChatRequest.Params.Reasoning.Effort == nil || *reqAB.ChatRequest.Params.Reasoning.Effort != "low" {
		t.Fatalf("Mode B+A did not inject reasoning.effort=low: %+v", reqAB.ChatRequest.Params)
	}

	// Test Mode B pure decomposition: Key 2 has Models=["gpt-5"], caller requests "gpt-5-xhigh"
	keyModeB := schemas.Key{
		ID:     "k2",
		Name:   "mode-b-key",
		Models: schemas.WhiteList{"gpt-5"},
	}
	if !keySupportsModel(schemas.OpenAI, &keyModeB, "gpt-5-xhigh") {
		t.Fatal("keySupportsModel should admit gpt-5-xhigh when gpt-5 is allowed")
	}
	reqB := &ChannelMessage{
		Context: ctx,
		BifrostRequest: schemas.BifrostRequest{
			RequestType: schemas.ResponsesRequest,
			ResponsesRequest: &schemas.BifrostResponsesRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-5-xhigh",
			},
		},
	}
	callerEffort, callerMaxTokens = extractRequestReasoning(&reqB.BifrostRequest)
	injected = false
	resolved = bf.resolveAttemptModel(reqB, keyModeB, schemas.OpenAI, "gpt-5-xhigh", callerEffort, callerMaxTokens, &injected)
	if resolved != "gpt-5" || reqB.ResponsesRequest.Model != "gpt-5" {
		t.Fatalf("Mode B resolved = %q, want gpt-5", resolved)
	}
	if reqB.ResponsesRequest.Params == nil || reqB.ResponsesRequest.Params.Reasoning == nil || reqB.ResponsesRequest.Params.Reasoning.Effort == nil || *reqB.ResponsesRequest.Params.Reasoning.Effort != "xhigh" {
		t.Fatalf("Mode B did not inject reasoning.effort=xhigh on ResponsesRequest")
	}

	// Test Literal Model Priority: Key 3 explicitly lists "custom-model-high" in key.Models
	keyLiteral := schemas.Key{
		ID:     "k3",
		Name:   "literal-key",
		Models: schemas.WhiteList{"custom-model-high"},
	}
	reqLit := &ChannelMessage{
		Context: ctx,
		BifrostRequest: schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "custom-model-high",
			},
		},
	}
	callerEffort, callerMaxTokens = extractRequestReasoning(&reqLit.BifrostRequest)
	injected = false
	resolved = bf.resolveAttemptModel(reqLit, keyLiteral, schemas.OpenAI, "custom-model-high", callerEffort, callerMaxTokens, &injected)
	if resolved != "custom-model-high" {
		t.Fatalf("Literal model resolved = %q, want custom-model-high", resolved)
	}
}

func BenchmarkResolveAttemptModel_CommonPath(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		ID:     "k-common",
		Name:   "common-key",
		Models: schemas.WhiteList{"gpt-4o"},
	}
	req := &ChannelMessage{
		Context: ctx,
		BifrostRequest: schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o",
			},
		},
	}
	var bf *Bifrost
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		injected := false
		_ = bf.resolveAttemptModel(req, key, schemas.OpenAI, "gpt-4o", "", nil, &injected)
	}
}

func BenchmarkResolveAttemptModel_EffortFusion(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		ID:     "k-fused",
		Name:   "fused-key",
		Models: schemas.WhiteList{"claude-opus-4-6"},
		Aliases: schemas.KeyAliases{
			"claude-opus-4-6": {
				ModelID: "claude-opus-4-6-medium",
				EffortModels: &schemas.EffortModels{
					Low:     "claude-opus-4-6-low",
					Medium:  "claude-opus-4-6-medium",
					High:    "claude-opus-4-6-high",
					Default: "medium",
				},
			},
		},
	}
	req := &ChannelMessage{
		Context: ctx,
		BifrostRequest: schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.Anthropic,
				Model:    "claude-opus-4-6-high",
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{},
				},
			},
		},
	}
	var bf *Bifrost
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req.ChatRequest.Model = "claude-opus-4-6-high"
		req.ChatRequest.Params.Reasoning.Effort = nil
		injected := false
		_ = bf.resolveAttemptModel(req, key, schemas.Anthropic, "claude-opus-4-6-high", "", nil, &injected)
	}
}
