package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/providers/cpaoauth"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const oauthChannelTimeout = 45 * time.Second

// OAuthChannelSignInHandler handles interactive OAuth flows, JSON credential imports,
// and live subscription quota inspection for Antigravity, Codex, Claude, and Grok.
type OAuthChannelSignInHandler struct {
	config *lib.Config
}

// NewOAuthChannelSignInHandler creates a new handler for OAuth subscription channels.
func NewOAuthChannelSignInHandler(config *lib.Config) *OAuthChannelSignInHandler {
	return &OAuthChannelSignInHandler{
		config: config,
	}
}

// RegisterRoutes mounts the OAuth channel management endpoints.
func (h *OAuthChannelSignInHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/api/providers/{provider}/oauth/start", lib.ChainMiddlewares(h.startFlow, middlewares...))
	r.POST("/api/providers/{provider}/oauth/complete", lib.ChainMiddlewares(h.completeFlow, middlewares...))
	r.POST("/api/providers/{provider}/oauth/import", lib.ChainMiddlewares(h.importCredential, middlewares...))
	r.POST("/api/providers/{provider}/oauth/quota", lib.ChainMiddlewares(h.observeQuota, middlewares...))
}

func parseOAuthProvider(ctx *fasthttp.RequestCtx) (schemas.ModelProvider, bool) {
	raw, _ := ctx.UserValue("provider").(string)
	p := schemas.ModelProvider(strings.ToLower(strings.TrimSpace(raw)))
	switch p {
	case schemas.Antigravity, schemas.Codex, schemas.ClaudeOAuth, schemas.GrokOAuth:
		return p, true
	default:
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("OAuth channel flow is not supported for provider %q", raw))
		return "", false
	}
}

func (h *OAuthChannelSignInHandler) resolveProviderProxyAndBaseURL(provider schemas.ModelProvider, explicitProxy string, explicitBaseURL string) (proxyURL string, baseURL string) {
	proxyURL = strings.TrimSpace(explicitProxy)
	baseURL = strings.TrimSpace(explicitBaseURL)
	if h.config == nil {
		return proxyURL, baseURL
	}
	rawCfg, err := h.config.GetProviderConfigRaw(provider)
	if err != nil || rawCfg == nil {
		return proxyURL, baseURL
	}
	if baseURL == "" && rawCfg.NetworkConfig != nil {
		baseURL = strings.TrimSpace(rawCfg.NetworkConfig.BaseURL)
	}
	if proxyURL == "" && rawCfg.ProxyConfig != nil && rawCfg.ProxyConfig.Type != schemas.NoProxy && rawCfg.ProxyConfig.URL != nil {
		proxyURL = strings.TrimSpace(rawCfg.ProxyConfig.URL.GetValue())
	}
	return proxyURL, baseURL
}

// startFlow initiates a browser PKCE or device-code OAuth flow.
// POST /api/providers/{provider}/oauth/start
func (h *OAuthChannelSignInHandler) startFlow(ctx *fasthttp.RequestCtx) {
	provider, ok := parseOAuthProvider(ctx)
	if !ok {
		return
	}
	var req struct {
		ProxyURL string `json:"proxy_url,omitempty"`
	}
	if len(ctx.PostBody()) > 0 {
		_ = sonic.Unmarshal(ctx.PostBody(), &req)
	}
	proxyURL, _ := h.resolveProviderProxyAndBaseURL(provider, req.ProxyURL, "")

	reqCtx, cancel := context.WithTimeout(context.Background(), oauthChannelTimeout)
	defer cancel()

	res, err := cpaoauth.StartOAuthFlow(reqCtx, provider, proxyURL)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, fmt.Sprintf("Failed to start %s OAuth flow: %v", provider, err))
		return
	}
	SendJSON(ctx, res)
}

// completeFlow exchanges the browser callback URL/code or polls the device-code flow.
// POST /api/providers/{provider}/oauth/complete
func (h *OAuthChannelSignInHandler) completeFlow(ctx *fasthttp.RequestCtx) {
	provider, ok := parseOAuthProvider(ctx)
	if !ok {
		return
	}
	var req cpaoauth.OAuthCompleteRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request payload: %v", err))
		return
	}
	req.ProxyURL, _ = h.resolveProviderProxyAndBaseURL(provider, req.ProxyURL, "")

	reqCtx, cancel := context.WithTimeout(context.Background(), oauthChannelTimeout)
	defer cancel()

	res, err := cpaoauth.CompleteOAuthFlow(reqCtx, provider, req)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, fmt.Sprintf("Failed to complete %s OAuth flow: %v", provider, err))
		return
	}
	SendJSON(ctx, res)
}

// importCredential validates and normalizes an uploaded or pasted OAuth credential JSON.
// POST /api/providers/{provider}/oauth/import
func (h *OAuthChannelSignInHandler) importCredential(ctx *fasthttp.RequestCtx) {
	provider, ok := parseOAuthProvider(ctx)
	if !ok {
		return
	}
	var req struct {
		Credential json.RawMessage `json:"credential"`
		RawJSON    string          `json:"raw_json,omitempty"`
		ProxyURL   string          `json:"proxy_url,omitempty"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON payload: %v", err))
		return
	}
	rawBytes := []byte(strings.TrimSpace(req.RawJSON))
	if len(rawBytes) == 0 && len(req.Credential) > 0 {
		// If credential was passed as a JSON string, unquote it; if object, use raw bytes.
		var asStr string
		if json.Unmarshal(req.Credential, &asStr) == nil && strings.TrimSpace(asStr) != "" {
			rawBytes = []byte(strings.TrimSpace(asStr))
		} else {
			rawBytes = req.Credential
		}
	}
	if len(rawBytes) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "credential JSON is required")
		return
	}
	proxyURL, _ := h.resolveProviderProxyAndBaseURL(provider, req.ProxyURL, "")

	reqCtx, cancel := context.WithTimeout(context.Background(), oauthChannelTimeout)
	defer cancel()

	res, err := cpaoauth.ImportCredentialJSON(reqCtx, provider, rawBytes, proxyURL)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid %s credential JSON: %v", provider, err))
		return
	}
	SendJSON(ctx, res)
}

// observeQuota queries live subscription quota, rate-limit windows, and discovered models.
// POST /api/providers/{provider}/oauth/quota
func (h *OAuthChannelSignInHandler) observeQuota(ctx *fasthttp.RequestCtx) {
	provider, ok := parseOAuthProvider(ctx)
	if !ok {
		return
	}
	var req struct {
		KeyID      string `json:"key_id,omitempty"`
		KeyName    string `json:"key_name,omitempty"`
		Credential string `json:"credential,omitempty"`
		BaseURL    string `json:"base_url,omitempty"`
		ProxyURL   string `json:"proxy_url,omitempty"`
	}
	if len(ctx.PostBody()) > 0 {
		if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON payload: %v", err))
			return
		}
	}

	rawCred := strings.TrimSpace(req.Credential)
	isRedacted := rawCred == "" || strings.EqualFold(rawCred, "<REDACTED>") || strings.HasPrefix(rawCred, "***") || strings.Contains(rawCred, "••••")
	if isRedacted && h.config != nil {
		if rawCfg, err := h.config.GetProviderConfigRaw(provider); err == nil && rawCfg != nil {
			for _, k := range rawCfg.Keys {
				if (req.KeyID != "" && k.ID == req.KeyID) || (req.KeyName != "" && k.Name == req.KeyName) {
					rawCred = strings.TrimSpace(k.Value.GetValue())
					break
				}
			}
			if rawCred == "" && req.KeyID == "" && req.KeyName == "" && len(rawCfg.Keys) == 1 {
				rawCred = strings.TrimSpace(rawCfg.Keys[0].Value.GetValue())
			}
		}
	}
	if rawCred == "" || strings.EqualFold(rawCred, "<REDACTED>") {
		SendError(ctx, fasthttp.StatusBadRequest, "A valid OAuth credential or saved key_id is required to inspect quota")
		return
	}

	proxyURL, baseURL := h.resolveProviderProxyAndBaseURL(provider, req.ProxyURL, req.BaseURL)

	reqCtx, cancel := context.WithTimeout(context.Background(), oauthChannelTimeout)
	defer cancel()

	res, err := cpaoauth.ObserveAccountQuota(reqCtx, provider, rawCred, baseURL, proxyURL)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, fmt.Sprintf("Failed to query %s account quota: %v", provider, err))
		return
	}
	SendJSON(ctx, res)
}
