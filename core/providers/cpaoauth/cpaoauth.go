// Package cpaoauth implements Bifrost providers for OAuth subscription channels
// (Antigravity, OpenAI Codex, Claude OAuth, and xAI Grok OAuth) backed by cpaembedded.
package cpaoauth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/router-for-me/CLIProxyAPI/v8/gptload-embedded/embedded"
)

// Provider implements schemas.Provider for Antigravity, Codex, ClaudeOAuth, and GrokOAuth.
type Provider struct {
	providerKey         schemas.ModelProvider
	logger              schemas.Logger
	networkConfig       schemas.NetworkConfig
	proxyURL            string
	sendBackRawRequest  bool
	sendBackRawResponse bool

	antigravityExec embedded.AntigravityHTTPExecutor
	codexExec       *embedded.CodexHTTPExecutor
	claudeExec      embedded.ClaudeHTTPExecutor
	grokExec        embedded.GrokHTTPExecutor
}

// NewProvider creates a new OAuth channel provider for Antigravity, Codex, Claude, or Grok.
func NewProvider(providerKey schemas.ModelProvider, config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	if config == nil {
		config = &schemas.ProviderConfig{}
	}
	config.CheckAndSetDefaults()

	return &Provider{
		providerKey:         providerKey,
		logger:              logger,
		networkConfig:       config.NetworkConfig,
		proxyURL:            extractProxyURL(config.ProxyConfig),
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
		antigravityExec:     embedded.NewAntigravityHTTPExecutor(),
		codexExec:           embedded.NewCodexHTTPExecutor(),
		claudeExec:          embedded.NewClaudeHTTPExecutor(),
		grokExec:            embedded.NewGrokHTTPExecutor(),
	}, nil
}

func extractProxyURL(proxyCfg *schemas.ProxyConfig) string {
	if proxyCfg == nil || proxyCfg.Type == schemas.NoProxy || proxyCfg.URL == nil {
		return ""
	}
	rawURL := strings.TrimSpace(proxyCfg.URL.GetValue())
	if rawURL == "" {
		return ""
	}
	if proxyCfg.Username != nil && proxyCfg.Username.GetValue() != "" {
		if u, err := url.Parse(rawURL); err == nil {
			pwd := ""
			if proxyCfg.Password != nil {
				pwd = proxyCfg.Password.GetValue()
			}
			u.User = url.UserPassword(proxyCfg.Username.GetValue(), pwd)
			return u.String()
		}
	}
	return rawURL
}

// GetProviderKey returns the provider identifier.
func (p *Provider) GetProviderKey() schemas.ModelProvider {
	return p.providerKey
}

func (p *Provider) collectExtraHeaders(ctx *schemas.BifrostContext) http.Header {
	var out http.Header
	if len(p.networkConfig.ExtraHeaders) > 0 {
		out = make(http.Header, len(p.networkConfig.ExtraHeaders))
		for k, v := range p.networkConfig.ExtraHeaders {
			if val := v.GetValue(); val != "" {
				out.Set(k, val)
			}
		}
	}
	if ctx != nil {
		if ctxHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
			if out == nil {
				out = make(http.Header, len(ctxHeaders))
			}
			for k, vals := range ctxHeaders {
				if len(vals) > 0 {
					out.Set(k, vals[len(vals)-1])
				}
			}
		}
	}
	return out
}

const discoveredModelsCacheTTL = 5 * time.Minute

func (p *Provider) discoverModelsForKey(ctx context.Context, key schemas.Key) []string {
	rawKey := strings.TrimSpace(key.Value.GetValue())
	if rawKey != "" {
		keyID := key.ID
		if keyID == "" {
			keyID = key.Name
		}
		cKey := cacheKeyFor(p.providerKey, keyID, rawKey)
		if val, ok := globalCredCache.Load(cKey); ok {
			entry := val.(*cachedCredential)
			entry.mu.RLock()
			if len(entry.discoveredModels) > 0 && time.Since(entry.modelsFetchedAt) < discoveredModelsCacheTTL {
				cached := append([]string(nil), entry.discoveredModels...)
				entry.mu.RUnlock()
				return cached
			}
			entry.mu.RUnlock()
		}

		storeDiscovered := func(merged []string) []string {
			val, _ := globalCredCache.LoadOrStore(cKey, &cachedCredential{})
			entry := val.(*cachedCredential)
			entry.mu.Lock()
			entry.discoveredModels = append([]string(nil), merged...)
			entry.modelsFetchedAt = time.Now()
			entry.mu.Unlock()
			return merged
		}

		httpClient := httpClientWithProxy(p.proxyURL)
		switch p.providerKey {
		case schemas.Antigravity:
			if cred, err := resolveAntigravityCredential(ctx, keyID, rawKey, p.proxyURL, false); err == nil {
				if models, dErr := embedded.DiscoverAntigravityModels(ctx, cred, embedded.AntigravityOptions{
					HTTPClient: httpClient,
				}); dErr == nil && len(models) > 0 {
					ids := make([]string, 0, len(models))
					for _, m := range models {
						if m.ID != "" {
							ids = append(ids, m.ID)
						}
					}
					return storeDiscovered(embedded.MergeModelCatalog(string(p.providerKey), ids))
				}
			}
		case schemas.Codex:
			if cred, err := resolveCodexCredential(ctx, keyID, rawKey, p.proxyURL, false); err == nil {
				if models, dErr := embedded.ListCodexModels(ctx, cred, p.networkConfig.BaseURL, embedded.Options{
					HTTPClient: httpClient,
				}); dErr == nil && len(models) > 0 {
					ids := make([]string, 0, len(models))
					for _, m := range models {
						if m.ID != "" {
							ids = append(ids, m.ID)
						}
					}
					return storeDiscovered(embedded.MergeModelCatalog(string(p.providerKey), ids))
				}
			}
		case schemas.ClaudeOAuth:
			if cred, err := resolveClaudeCredential(ctx, keyID, rawKey, p.proxyURL, false); err == nil {
				if models, dErr := embedded.DiscoverClaudeModels(ctx, cred, embedded.ClaudeOptions{
					HTTPClient: httpClient,
				}); dErr == nil && len(models) > 0 {
					ids := make([]string, 0, len(models))
					for _, m := range models {
						if m.ID != "" {
							ids = append(ids, m.ID)
						}
					}
					return storeDiscovered(embedded.MergeModelCatalog(string(p.providerKey), ids))
				}
			}
		case schemas.GrokOAuth:
			if cred, err := resolveGrokCredential(ctx, keyID, rawKey, p.proxyURL, false); err == nil {
				if ids, dErr := embedded.DiscoverGrokModels(ctx, cred, embedded.GrokOptions{
					HTTPClient: httpClient,
				}); dErr == nil && len(ids) > 0 {
					return storeDiscovered(embedded.MergeModelCatalog(string(p.providerKey), ids))
				}
			}
		}

		// Fall back to last cached live discovery if available before static-only catalog.
		if val, ok := globalCredCache.Load(cKey); ok {
			entry := val.(*cachedCredential)
			entry.mu.RLock()
			if len(entry.discoveredModels) > 0 {
				cached := append([]string(nil), entry.discoveredModels...)
				entry.mu.RUnlock()
				return cached
			}
			entry.mu.RUnlock()
		}
	}
	return embedded.MergeModelCatalog(string(p.providerKey), nil)
}

func (p *Provider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	startTime := time.Now()
	unfiltered := false
	if req != nil {
		unfiltered = req.Unfiltered
	}
	modelIDs := p.discoverModelsForKey(ctx, key)

	// Merge auto-detected effort families (<base>-low/medium/high, <base>-thinking) with explicit key.Aliases.
	// Explicit user aliases always take precedence.
	autoAliases := schemas.BuildAutoEffortAliases(modelIDs)
	var effectiveAliases schemas.KeyAliases
	if len(autoAliases) > 0 || len(key.Aliases) > 0 {
		effectiveAliases = make(schemas.KeyAliases, len(autoAliases)+len(key.Aliases))
		for k, v := range autoAliases {
			effectiveAliases[k] = v
		}
		for k, v := range key.Aliases {
			// Remove any case-insensitive duplicate from autoAliases before setting explicit alias
			for ak := range effectiveAliases {
				if !strings.EqualFold(ak, k) {
					continue
				}
				if ak != k {
					delete(effectiveAliases, ak)
				}
			}
			effectiveAliases[k] = v
		}
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     key.Models,
		BlacklistedModels: key.BlacklistedModels,
		Aliases:           effectiveAliases,
		Unfiltered:        unfiltered,
		ProviderKey:       p.providerKey,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(modelIDs)+len(effectiveAliases)),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse, nil
	}

	included := make(map[string]bool, len(modelIDs)+len(effectiveAliases))
	ownedBy := string(p.providerKey)
	for _, id := range modelIDs {
		for _, result := range pipeline.FilterModel(id) {
			lowerResolved := strings.ToLower(result.ResolvedID)
			if included[lowerResolved] {
				continue
			}
			entry := schemas.Model{
				ID:      string(p.providerKey) + "/" + result.ResolvedID,
				Name:    schemas.Ptr(providerUtils.ToDisplayName(result.ResolvedID)),
				OwnedBy: schemas.Ptr(ownedBy),
			}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[lowerResolved] = true
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data, pipeline.BackfillModels(included)...)
	slices.SortFunc(bifrostResponse.Data, func(a, b schemas.Model) int {
		return strings.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID))
	})
	bifrostResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
	return bifrostResponse, nil
}

// ListModels returns the catalog + dynamically discovered models for the OAuth channel.
func (p *Provider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return p.listModelsByKey(ctx, schemas.Key{}, request)
	}
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		p.listModelsByKey,
	)
}

// ModelRetrieve retrieves a single model's metadata from the OAuth channel catalog.
func (p *Provider) ModelRetrieve(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostModelRetrieveRequest) (*schemas.BifrostModelRetrieveResponse, *schemas.BifrostError) {
	if request == nil || strings.TrimSpace(request.Model) == "" {
		return nil, providerUtils.NewBifrostOperationError("model is required", nil)
	}
	return &schemas.BifrostModelRetrieveResponse{
		Model: schemas.Model{
			ID:      string(p.providerKey) + "/" + request.Model,
			Name:    schemas.Ptr(providerUtils.ToDisplayName(request.Model)),
			OwnedBy: schemas.Ptr(string(p.providerKey)),
		},
	}, nil
}

func (p *Provider) executeUnary(ctx *schemas.BifrostContext, key schemas.Key, model string, format string, payload []byte) (embedded.ExecuteResponse, error) {
	rawKey := strings.TrimSpace(key.Value.GetValue())
	keyID := key.ID
	if keyID == "" {
		keyID = key.Name
	}
	execReq := embedded.ExecuteRequest{
		Model:    model,
		Format:   format,
		Payload:  payload,
		BaseURL:  p.networkConfig.BaseURL,
		ProxyURL: p.proxyURL,
		Headers:  p.collectExtraHeaders(ctx),
	}

	runOnce := func(forceRefresh bool) (embedded.ExecuteResponse, error) {
		switch p.providerKey {
		case schemas.Antigravity:
			cred, err := resolveAntigravityCredential(ctx, keyID, rawKey, p.proxyURL, forceRefresh)
			if err != nil {
				return embedded.ExecuteResponse{}, err
			}
			return p.antigravityExec.ExecuteCanonical(ctx, keyID, cred, execReq)
		case schemas.Codex:
			cred, err := resolveCodexCredential(ctx, keyID, rawKey, p.proxyURL, forceRefresh)
			if err != nil {
				return embedded.ExecuteResponse{}, err
			}
			return p.codexExec.ExecuteCanonical(ctx, keyID, cred, execReq)
		case schemas.ClaudeOAuth:
			cred, err := resolveClaudeCredential(ctx, keyID, rawKey, p.proxyURL, forceRefresh)
			if err != nil {
				return embedded.ExecuteResponse{}, err
			}
			return p.claudeExec.ExecuteCanonical(ctx, keyID, cred, execReq)
		case schemas.GrokOAuth:
			cred, err := resolveGrokCredential(ctx, keyID, rawKey, p.proxyURL, forceRefresh)
			if err != nil {
				return embedded.ExecuteResponse{}, err
			}
			return p.grokExec.ExecuteCanonical(ctx, keyID, cred, execReq)
		default:
			return embedded.ExecuteResponse{}, fmt.Errorf("unsupported OAuth provider %s", p.providerKey)
		}
	}

	resp, err := runOnce(false)
	if err != nil && isUnauthorizedStatus(resp.StatusCode, err) {
		if retryResp, retryErr := runOnce(true); retryErr == nil || !isUnauthorizedStatus(retryResp.StatusCode, retryErr) {
			return retryResp, retryErr
		}
	}
	return resp, err
}

func (p *Provider) executeStream(ctx *schemas.BifrostContext, key schemas.Key, model string, format string, payload []byte) (<-chan embedded.ExecuteStreamChunk, error) {
	rawKey := strings.TrimSpace(key.Value.GetValue())
	keyID := key.ID
	if keyID == "" {
		keyID = key.Name
	}
	execReq := embedded.ExecuteRequest{
		Model:    model,
		Format:   format,
		Payload:  payload,
		BaseURL:  p.networkConfig.BaseURL,
		ProxyURL: p.proxyURL,
		Headers:  p.collectExtraHeaders(ctx),
	}

	openStream := func(forceRefresh bool) (<-chan embedded.ExecuteStreamChunk, error) {
		switch p.providerKey {
		case schemas.Antigravity:
			cred, err := resolveAntigravityCredential(ctx, keyID, rawKey, p.proxyURL, forceRefresh)
			if err != nil {
				return nil, err
			}
			resp, err := p.antigravityExec.ExecuteStreamCanonical(ctx, keyID, cred, execReq)
			if err != nil || resp == nil {
				return nil, err
			}
			return resp.Chunks, nil
		case schemas.Codex:
			cred, err := resolveCodexCredential(ctx, keyID, rawKey, p.proxyURL, forceRefresh)
			if err != nil {
				return nil, err
			}
			resp, err := p.codexExec.ExecuteStreamCanonical(ctx, keyID, cred, execReq)
			if err != nil || resp == nil {
				return nil, err
			}
			return resp.Chunks, nil
		case schemas.ClaudeOAuth:
			cred, err := resolveClaudeCredential(ctx, keyID, rawKey, p.proxyURL, forceRefresh)
			if err != nil {
				return nil, err
			}
			resp, err := p.claudeExec.ExecuteStreamCanonical(ctx, keyID, cred, execReq)
			if err != nil || resp == nil {
				return nil, err
			}
			return resp.Chunks, nil
		case schemas.GrokOAuth:
			cred, err := resolveGrokCredential(ctx, keyID, rawKey, p.proxyURL, forceRefresh)
			if err != nil {
				return nil, err
			}
			resp, err := p.grokExec.ExecuteStreamCanonical(ctx, keyID, cred, execReq)
			if err != nil || resp == nil {
				return nil, err
			}
			return resp.Chunks, nil
		default:
			return nil, fmt.Errorf("unsupported OAuth provider %s", p.providerKey)
		}
	}

	chunks, err := openStream(false)
	if err != nil {
		if isUnauthorizedStatus(0, err) {
			chunks, err = openStream(true)
		}
		if err != nil {
			return nil, err
		}
	}

	// Peek first chunk so pre-stream HTTP errors (e.g., 401/429/500) are caught synchronously
	// and 401 can trigger an automatic token refresh retry before returning a stream channel.
	firstChunk, ok := <-chunks
	if !ok {
		closedCh := make(chan embedded.ExecuteStreamChunk)
		close(closedCh)
		return closedCh, nil
	}
	if firstChunk.Err != nil {
		if isUnauthorizedStatus(0, firstChunk.Err) {
			retryChunks, retryErr := openStream(true)
			if retryErr != nil {
				return nil, retryErr
			}
			retryFirst, retryOk := <-retryChunks
			if !retryOk {
				closedCh := make(chan embedded.ExecuteStreamChunk)
				close(closedCh)
				return closedCh, nil
			}
			if retryFirst.Err != nil {
				return nil, retryFirst.Err
			}
			firstChunk = retryFirst
			chunks = retryChunks
		} else {
			return nil, firstChunk.Err
		}
	}

	out := make(chan embedded.ExecuteStreamChunk, 16)
	out <- firstChunk
	go func() {
		defer close(out)
		for ch := range chunks {
			out <- ch
		}
	}()
	return out, nil
}

var statusCodeRegex = regexp.MustCompile(`(?i)(?:status(?:\s+code)?|http)\s*[=:]?\s*([45]\d\d)\b`)

func extractStatusCodeFromError(statusCode int, err error) int {
	if statusCode >= 400 {
		return statusCode
	}
	if err == nil {
		return http.StatusBadGateway
	}
	if sc, ok := err.(interface{ StatusCode() int }); ok {
		if code := sc.StatusCode(); code >= 400 {
			return code
		}
	}
	if m := statusCodeRegex.FindStringSubmatch(err.Error()); len(m) == 2 {
		if code, convErr := strconv.Atoi(m[1]); convErr == nil && code >= 400 && code < 600 {
			return code
		}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "invalid_grant"):
		return http.StatusUnauthorized
	case strings.Contains(msg, "forbidden"):
		return http.StatusForbidden
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many requests") || strings.Contains(msg, "quota"):
		return http.StatusTooManyRequests
	default:
		return http.StatusBadGateway
	}
}

func (p *Provider) toBifrostError(ctx *schemas.BifrostContext, statusCode int, err error, rawReq []byte, rawResp []byte, latency time.Duration) *schemas.BifrostError {
	sendRawReq := providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest)
	sendRawResp := providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse)

	code := extractStatusCodeFromError(statusCode, err)
	msg := "provider request failed"
	if err != nil {
		msg = err.Error()
	}
	if len(rawResp) > 0 {
		var parsed schemas.BifrostError
		if uErr := sonic.Unmarshal(rawResp, &parsed); uErr == nil && parsed.Error != nil && parsed.Error.Message != "" {
			parsed.StatusCode = schemas.Ptr(code)
			parsed.IsBifrostError = false
			return providerUtils.EnrichError(ctx, &parsed, rawReq, rawResp, sendRawReq, sendRawResp, latency)
		}
	}
	bErr := providerUtils.NewProviderAPIError(msg, err, code, nil, nil)
	return providerUtils.EnrichError(ctx, bErr, rawReq, rawResp, sendRawReq, sendRawResp, latency)
}

func formatSSEChunk(payload []byte) []byte {
	trimmed := bytes.TrimRight(payload, "\r\n")
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte(":")) {
		var buf bytes.Buffer
		buf.Write(trimmed)
		buf.WriteString("\n\n")
		return buf.Bytes()
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	var buf bytes.Buffer
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) || bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte(":")) {
			buf.Write(line)
			buf.WriteByte('\n')
		} else {
			buf.WriteString("data: ")
			buf.Write(line)
			buf.WriteByte('\n')
		}
	}
	if buf.Len() > 0 {
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func pipeCPAStreamToReader(ctx context.Context, chunks <-chan embedded.ExecuteStreamChunk, appendDone bool) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		var streamErr error
		defer func() {
			if streamErr != nil {
				_ = pw.CloseWithError(streamErr)
				return
			}
			if appendDone {
				_, _ = pw.Write([]byte("data: [DONE]\n\n"))
			}
			_ = pw.Close()
		}()
		for {
			select {
			case <-ctx.Done():
				streamErr = ctx.Err()
				return
			case chunk, ok := <-chunks:
				if !ok {
					return
				}
				if chunk.Err != nil {
					streamErr = chunk.Err
					return
				}
				if formatted := formatSSEChunk(chunk.Payload); len(formatted) > 0 {
					if _, wErr := pw.Write(formatted); wErr != nil {
						return
					}
				}
			}
		}
	}()
	return pr
}

// ChatCompletion executes a non-streaming chat completion request through cpaembedded.
func (p *Provider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*openai.OpenAIChatRequest, error) {
			reqBody := openai.ToOpenAIChatRequest(ctx, request)
			if err := openai.ResolveChatFileURLs(ctx, request.Provider, reqBody); err != nil {
				return nil, err
			}
			return reqBody, nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	startTime := time.Now()
	execResp, err := p.executeUnary(ctx, key, request.Model, "openai", jsonData)
	latency := time.Since(startTime)
	if err != nil {
		return nil, p.toBifrostError(ctx, execResp.StatusCode, err, jsonData, execResp.Payload, latency)
	}

	response := &schemas.BifrostChatResponse{}
	rawRequest, rawResponse, bErr := providerUtils.HandleProviderResponseCtx(ctx, execResp.Payload, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bErr != nil {
		return nil, providerUtils.EnrichError(ctx, bErr, jsonData, execResp.Payload, sendBackRawRequest, sendBackRawResponse, latency)
	}

	if response.Model == "" || response.Model == "gemini-default" {
		response.Model = request.Model
	}
	response.Usage.PreserveTokenCountZeros(execResp.Payload, "usage.prompt_tokens", "usage.completion_tokens")
	response.Usage.NormalizeReasoningTokens()
	response.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ChatCompletionStream executes a streaming chat completion request through cpaembedded.
func (p *Provider) ChatCompletionStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, p.networkConfig.StreamIdleTimeoutInSeconds)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse)

	isResponsesFallback := false
	var responsesStreamState *schemas.ChatToResponsesStreamState
	if val, ok := ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback).(bool); ok && val {
		isResponsesFallback = true
		responsesStreamState = schemas.AcquireChatToResponsesStreamState()
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*openai.OpenAIChatRequest, error) {
			reqBody := openai.ToOpenAIChatRequest(ctx, request)
			if err := openai.ResolveChatFileURLs(ctx, request.Provider, reqBody); err != nil {
				if responsesStreamState != nil {
					schemas.ReleaseChatToResponsesStreamState(responsesStreamState)
				}
				return nil, err
			}
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
				reqBody.StreamOptions = &schemas.ChatStreamOptions{
					IncludeUsage: schemas.Ptr(true),
				}
			}
			return reqBody, nil
		},
	)
	if bifrostErr != nil {
		if responsesStreamState != nil {
			schemas.ReleaseChatToResponsesStreamState(responsesStreamState)
		}
		return nil, bifrostErr
	}

	startTime := time.Now()
	cpaChunks, err := p.executeStream(ctx, key, request.Model, "openai", jsonBody)
	latency := time.Since(startTime)
	if err != nil {
		if responsesStreamState != nil {
			schemas.ReleaseChatToResponsesStreamState(responsesStreamState)
		}
		return nil, p.toBifrostError(ctx, 0, err, jsonBody, nil, latency)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, p.logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, p.logger, postHookSpanFinalizer, jsonBody)
			}
			schemas.ReleaseChatToResponsesStreamState(responsesStreamState)
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.RecoverStreamPanic(ctx, postHookRunner, responseChan, p.logger, postHookSpanFinalizer)

		pipeReader := pipeCPAStreamToReader(ctx, cpaChunks, true)
		defer pipeReader.Close()

		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(pipeReader, pipeReader, providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		sseReader := providerUtils.GetSSEDataReader(ctx, reader)

		chunkIndex := -1
		usage := &schemas.BifrostLLMUsage{}
		ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, usage)
		lastChunkTime := startTime

		var finishReason *string
		var messageID string
		modelName := request.Model
		var created int
		forwardedTerminalFinishReason := false
		var pendingFinalEvent *schemas.BifrostResponsesStreamResponse
		usageSeen := false

		for {
			if ctx.Err() != nil {
				return
			}
			data, readErr := sseReader.ReadDataLine()
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				if readErr != io.EOF {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, p.logger, postHookSpanFinalizer)
					return
				}
				break
			}
			jsonData := string(data)

			if errorNode, _ := sonic.GetFromString(jsonData, "error"); errorNode.Exists() {
				var bErr schemas.BifrostError
				if uErr := sonic.UnmarshalString(jsonData, &bErr); uErr == nil && bErr.Error != nil && bErr.Error.Message != "" {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, &bErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, p.logger, postHookSpanFinalizer)
					return
				}
			}

			var response schemas.BifrostChatResponse
			if uErr := sonic.UnmarshalString(jsonData, &response); uErr != nil {
				continue
			}
			if response.Model == "" || response.Model == "gemini-default" {
				response.Model = request.Model
			}
			response.Usage.PreserveTokenCountZeros(data, "usage.prompt_tokens", "usage.completion_tokens")
			if response.Choices == nil {
				response.Choices = []schemas.BifrostResponseChoice{}
			}
			response.Usage.NormalizeReasoningTokens()
			for i := range response.Choices {
				if response.Choices[i].ChatStreamResponseChoice != nil {
					response.Choices[i].ChatNonStreamResponseChoice = nil
				}
			}

			if isResponsesFallback {
				if response.Usage != nil {
					usageSeen = true
					mergeUsage(usage, response.Usage)
				}
				spreadResponses := response.ToBifrostResponsesStreamResponse(responsesStreamState)
				for _, ev := range spreadResponses {
					if ev.Type == schemas.ResponsesStreamResponseTypeError {
						bErr := &schemas.BifrostError{
							Type:           schemas.Ptr(string(schemas.ResponsesStreamResponseTypeError)),
							IsBifrostError: false,
							Error:          &schemas.ErrorField{},
						}
						if ev.Message != nil {
							bErr.Error.Message = *ev.Message
						}
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, p.logger, postHookSpanFinalizer)
						return
					}
					ev.ExtraFields.ChunkIndex = ev.SequenceNumber
					ev.ExtraFields.ConvertedRequestType = schemas.ChatCompletionRequest
					if ev.Type == schemas.ResponsesStreamResponseTypeCompleted || ev.Type == schemas.ResponsesStreamResponseTypeIncomplete {
						pendingFinalEvent = ev
						continue
					}
					ev.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
					lastChunkTime = time.Now()
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, ev, nil, nil, nil), responseChan, postHookSpanFinalizer)
				}
				continue
			}

			if response.Usage != nil {
				mergeUsage(usage, response.Usage)
				response.Usage = nil
			}
			if response.Model != "" && response.Model != "gemini-default" {
				modelName = response.Model
			}
			if len(response.Choices) == 0 {
				continue
			}
			choice := response.Choices[0]
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			if response.ID != "" && messageID == "" {
				messageID = response.ID
			}
			if response.Created != 0 && created == 0 {
				created = response.Created
			}

			if choice.ChatStreamResponseChoice != nil &&
				choice.ChatStreamResponseChoice.Delta != nil &&
				(choice.ChatStreamResponseChoice.Delta.Role != nil ||
					(choice.ChatStreamResponseChoice.Delta.Content != nil && *choice.ChatStreamResponseChoice.Delta.Content != "") ||
					(choice.ChatStreamResponseChoice.Delta.Refusal != nil && *choice.ChatStreamResponseChoice.Delta.Refusal != "") ||
					len(choice.ChatStreamResponseChoice.Delta.Annotations) > 0 ||
					choice.ChatStreamResponseChoice.Delta.Reasoning != nil ||
					len(choice.ChatStreamResponseChoice.Delta.ReasoningDetails) > 0 ||
					choice.ChatStreamResponseChoice.Delta.Audio != nil ||
					len(choice.ChatStreamResponseChoice.Delta.Images) > 0 ||
					len(choice.ChatStreamResponseChoice.Delta.ToolCalls) > 0 ||
					len(choice.ChatStreamResponseChoice.Delta.ExtraContent) > 0) {
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					forwardedTerminalFinishReason = true
				}
				chunkIndex++
				response.ExtraFields.ChunkIndex = chunkIndex
				response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
				lastChunkTime = time.Now()
				if sendBackRawResponse {
					response.ExtraFields.RawResponse = jsonData
					response.ExtraFields.RawResponseFormat = schemas.RawResponseFormatOpenAIChatCompletionChunk
				}
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, &response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
			}
		}

		if isResponsesFallback {
			if pendingFinalEvent != nil {
				if usageSeen && pendingFinalEvent.Response != nil {
					pendingFinalEvent.Response.Usage = usage.ToResponsesResponseUsage()
				}
				if sendBackRawRequest {
					providerUtils.ParseAndSetRawRequest(&pendingFinalEvent.ExtraFields, jsonBody)
				}
				pendingFinalEvent.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, pendingFinalEvent, nil, nil, nil), responseChan, postHookSpanFinalizer)
			}
			return
		}

		finalFinishReason := finishReason
		if forwardedTerminalFinishReason {
			finalFinishReason = nil
		}
		finalResp := providerUtils.CreateBifrostChatCompletionChunkResponse(messageID, usage, finalFinishReason, chunkIndex, modelName, created)
		if sendBackRawRequest {
			providerUtils.ParseAndSetRawRequest(&finalResp.ExtraFields, jsonBody)
		}
		finalResp.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, finalResp, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
	}()

	return responseChan, nil
}

func mergeUsage(dst *schemas.BifrostLLMUsage, src *schemas.BifrostLLMUsage) {
	if dst == nil || src == nil {
		return
	}
	dst.PromptTokensExplicitZero = dst.PromptTokensExplicitZero || src.PromptTokensExplicitZero
	dst.CompletionTokensExplicitZero = dst.CompletionTokensExplicitZero || src.CompletionTokensExplicitZero
	if src.PromptTokens > dst.PromptTokens {
		dst.PromptTokens = src.PromptTokens
	}
	if src.CompletionTokens > dst.CompletionTokens {
		dst.CompletionTokens = src.CompletionTokens
	}
	if src.TotalTokens > dst.TotalTokens {
		dst.TotalTokens = src.TotalTokens
	}
	if calc := dst.PromptTokens + dst.CompletionTokens; calc > dst.TotalTokens {
		dst.TotalTokens = calc
	}
	if src.PromptTokensDetails != nil {
		dst.PromptTokensDetails = src.PromptTokensDetails
	}
	if src.CompletionTokensDetails != nil {
		dst.CompletionTokensDetails = src.CompletionTokensDetails
	}
	if src.Cost != nil {
		dst.Cost = src.Cost
	}
}

// Responses executes a non-streaming OpenAI Responses request.
// Codex natively speaks OpenAI Responses; Antigravity, Claude, and Grok bridge via ChatCompletion.
func (p *Provider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if p.providerKey != schemas.Codex {
		chatResponse, err := p.ChatCompletion(ctx, key, request.ToChatRequest())
		if err != nil {
			return nil, err
		}
		responsesResponse := chatResponse.ToBifrostResponsesResponse()
		responsesResponse.ExtraFields.ConvertedRequestType = schemas.ChatCompletionRequest
		return responsesResponse, nil
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*openai.OpenAIResponsesRequest, error) {
			return openai.ToOpenAIResponsesRequest(ctx, request), nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	startTime := time.Now()
	execResp, err := p.executeUnary(ctx, key, request.Model, "openai-response", jsonData)
	latency := time.Since(startTime)
	if err != nil {
		return nil, p.toBifrostError(ctx, execResp.StatusCode, err, jsonData, execResp.Payload, latency)
	}

	response := &schemas.BifrostResponsesResponse{}
	rawRequest, rawResponse, bErr := providerUtils.HandleProviderResponseCtx(ctx, execResp.Payload, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bErr != nil {
		return nil, providerUtils.EnrichError(ctx, bErr, jsonData, execResp.Payload, sendBackRawRequest, sendBackRawResponse, latency)
	}
	response.Usage.PreserveTokenCountZeros(execResp.Payload, "usage.input_tokens", "usage.output_tokens")
	response.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ResponsesStream executes a streaming OpenAI Responses request.
func (p *Provider) ResponsesStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if p.providerKey != schemas.Codex {
		ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
		return p.ChatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request.ToChatRequest())
	}

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, p.networkConfig.StreamIdleTimeoutInSeconds)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse)

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*openai.OpenAIResponsesRequest, error) {
			reqBody := openai.ToOpenAIResponsesRequest(ctx, request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
			}
			return reqBody, nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	startTime := time.Now()
	cpaChunks, err := p.executeStream(ctx, key, request.Model, "openai-response", jsonBody)
	latency := time.Since(startTime)
	if err != nil {
		return nil, p.toBifrostError(ctx, 0, err, jsonBody, nil, latency)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, p.logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, p.logger, postHookSpanFinalizer, jsonBody)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.RecoverStreamPanic(ctx, postHookRunner, responseChan, p.logger, postHookSpanFinalizer)

		pipeReader := pipeCPAStreamToReader(ctx, cpaChunks, false)
		defer pipeReader.Close()

		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(pipeReader, pipeReader, providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		sseReader := providerUtils.GetSSEDataReader(ctx, reader)
		lastChunkTime := startTime

		for {
			if ctx.Err() != nil {
				return
			}
			data, readErr := sseReader.ReadDataLine()
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				if readErr != io.EOF {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, p.logger, postHookSpanFinalizer)
					return
				}
				break
			}
			jsonData := string(data)

			var response schemas.BifrostResponsesStreamResponse
			umErr := sonic.UnmarshalString(jsonData, &response)
			if umErr != nil {
				response = schemas.BifrostResponsesStreamResponse{}
				umErr = schemas.UnmarshalResponsesStreamObjectDelta([]byte(jsonData), &response)
			}
			if umErr != nil {
				continue
			}

			if sendBackRawResponse {
				response.ExtraFields.RawResponse = jsonData
				response.ExtraFields.RawResponseFormat = schemas.RawResponseFormatOpenAIResponsesStreamEvent
			}
			if response.Response != nil {
				response.Response.Usage.PreserveTokenCountZeros(data, "response.usage.input_tokens", "response.usage.output_tokens")
			}

			if response.Type == schemas.ResponsesStreamResponseTypeError || response.Type == schemas.ResponsesStreamResponseTypeFailed {
				bErr := &schemas.BifrostError{
					Type:           schemas.Ptr(string(response.Type)),
					IsBifrostError: false,
					Error:          &schemas.ErrorField{Message: "responses stream error"},
				}
				if response.Message != nil && *response.Message != "" {
					bErr.Error.Message = *response.Message
				} else if response.Response != nil && response.Response.Error != nil && response.Response.Error.Message != "" {
					bErr.Error.Message = response.Response.Error.Message
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bErr, jsonBody, []byte(jsonData), sendBackRawRequest, sendBackRawResponse, latency), responseChan, p.logger, postHookSpanFinalizer)
				return
			}

			response.ExtraFields.ChunkIndex = response.SequenceNumber
			if response.Type == schemas.ResponsesStreamResponseTypeCompleted || response.Type == schemas.ResponsesStreamResponseTypeIncomplete {
				if sendBackRawRequest {
					providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
				}
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &response, nil, nil, nil), responseChan, postHookSpanFinalizer)
				return
			}

			response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
			lastChunkTime = time.Now()
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &response, nil, nil, nil), responseChan, postHookSpanFinalizer)
		}
	}()

	return responseChan, nil
}

// CountTokens counts input tokens using cpaembedded's canonical token counter.
func (p *Provider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("request is nil", nil)
	}
	chatReq := request.ToChatRequest()
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		chatReq,
		func() (*openai.OpenAIChatRequest, error) {
			return openai.ToOpenAIChatRequest(ctx, chatReq), nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	rawKey := strings.TrimSpace(key.Value.GetValue())
	keyID := key.ID
	if keyID == "" {
		keyID = key.Name
	}
	execReq := embedded.ExecuteRequest{
		Model:    request.Model,
		Format:   "openai",
		Payload:  jsonData,
		BaseURL:  p.networkConfig.BaseURL,
		ProxyURL: p.proxyURL,
		Headers:  p.collectExtraHeaders(ctx),
	}

	var execResp embedded.ExecuteResponse
	var err error
	switch p.providerKey {
	case schemas.Antigravity:
		if cred, cErr := resolveAntigravityCredential(ctx, keyID, rawKey, p.proxyURL, false); cErr == nil {
			execResp, err = p.antigravityExec.CountTokensCanonical(ctx, keyID, cred, execReq)
		} else {
			err = cErr
		}
	case schemas.Codex:
		if cred, cErr := resolveCodexCredential(ctx, keyID, rawKey, p.proxyURL, false); cErr == nil {
			execResp, err = p.codexExec.CountTokensCanonical(ctx, keyID, cred, execReq)
		} else {
			err = cErr
		}
	case schemas.ClaudeOAuth:
		if cred, cErr := resolveClaudeCredential(ctx, keyID, rawKey, p.proxyURL, false); cErr == nil {
			execResp, err = p.claudeExec.CountTokensCanonical(ctx, keyID, cred, execReq)
		} else {
			err = cErr
		}
	case schemas.GrokOAuth:
		execResp, err = p.grokExec.CountTokensCanonical(ctx, execReq)
	default:
		return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, p.providerKey)
	}
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("count tokens failed", err)
	}

	resp := &schemas.BifrostCountTokensResponse{
		Model: request.Model,
	}
	_ = sonic.Unmarshal(execResp.Payload, resp)
	if resp.InputTokens == 0 {
		var alt struct {
			TotalTokens int `json:"total_tokens"`
			Total       int `json:"totalTokens"`
		}
		if sonic.Unmarshal(execResp.Payload, &alt) == nil {
			if alt.TotalTokens > 0 {
				resp.InputTokens = alt.TotalTokens
			} else if alt.Total > 0 {
				resp.InputTokens = alt.Total
			}
		}
	}
	return resp, nil
}

// Unsupported operations for OAuth subscription channels

func (p *Provider) TextCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, p.providerKey)
}

func (p *Provider) TextCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, p.providerKey)
}

func (p *Provider) Embedding(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, p.providerKey)
}

func (p *Provider) Speech(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, p.providerKey)
}

func (p *Provider) SpeechStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, p.providerKey)
}

func (p *Provider) Transcription(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, p.providerKey)
}

func (p *Provider) TranscriptionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, p.providerKey)
}

func (p *Provider) ImageGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, p.providerKey)
}

func (p *Provider) ImageGenerationStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, p.providerKey)
}

func (p *Provider) ImageEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, p.providerKey)
}

func (p *Provider) ImageEditStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, p.providerKey)
}

func (p *Provider) ImageVariation(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, p.providerKey)
}

func (p *Provider) Rerank(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, p.providerKey)
}

func (p *Provider) Decision(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.DecisionRequest, p.providerKey)
}

func (p *Provider) OCR(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, p.providerKey)
}

func (p *Provider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, p.providerKey)
}

func (p *Provider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, p.providerKey)
}

func (p *Provider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, p.providerKey)
}

func (p *Provider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, p.providerKey)
}

func (p *Provider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, p.providerKey)
}

func (p *Provider) VideoEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoEditRequest) (*schemas.BifrostVideoEditResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoEditRequest, p.providerKey)
}

func (p *Provider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, p.providerKey)
}

func (p *Provider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, p.providerKey)
}

func (p *Provider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, p.providerKey)
}

func (p *Provider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, p.providerKey)
}

func (p *Provider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, p.providerKey)
}

func (p *Provider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, p.providerKey)
}

func (p *Provider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, p.providerKey)
}

func (p *Provider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, p.providerKey)
}

func (p *Provider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, p.providerKey)
}

func (p *Provider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, p.providerKey)
}

func (p *Provider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, p.providerKey)
}

func (p *Provider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, p.providerKey)
}

func (p *Provider) CachedContentCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostCachedContentCreateRequest) (*schemas.BifrostCachedContentCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentCreateRequest, p.providerKey)
}

func (p *Provider) CachedContentList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentListRequest) (*schemas.BifrostCachedContentListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentListRequest, p.providerKey)
}

func (p *Provider) CachedContentRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentRetrieveRequest) (*schemas.BifrostCachedContentRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentRetrieveRequest, p.providerKey)
}

func (p *Provider) CachedContentUpdate(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentUpdateRequest) (*schemas.BifrostCachedContentUpdateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentUpdateRequest, p.providerKey)
}

func (p *Provider) CachedContentDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentDeleteRequest) (*schemas.BifrostCachedContentDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentDeleteRequest, p.providerKey)
}

func (p *Provider) Compaction(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, p.providerKey)
}

func (p *Provider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, p.providerKey)
}

func (p *Provider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, p.providerKey)
}

func (p *Provider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, p.providerKey)
}

func (p *Provider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, p.providerKey)
}

func (p *Provider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, p.providerKey)
}

func (p *Provider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, p.providerKey)
}

func (p *Provider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, p.providerKey)
}

func (p *Provider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, p.providerKey)
}

func (p *Provider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, p.providerKey)
}

func (p *Provider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, p.providerKey)
}

func (p *Provider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, p.providerKey)
}
