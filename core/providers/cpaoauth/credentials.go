package cpaoauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/router-for-me/CLIProxyAPI/v8/gptload-embedded/embedded"
	"golang.org/x/sync/singleflight"
)

const tokenRefreshSkew = 5 * time.Minute

type cachedCredential struct {
	mu               sync.RWMutex
	antigravity      *embedded.AntigravityCredential
	codex            *embedded.CodexCredential
	claude           *embedded.ClaudeCredential
	grok             *embedded.GrokCredential
	rawJSON          string
	lastRefresh      time.Time
	discoveredModels []string
	modelsFetchedAt  time.Time
}

var (
	globalCredCache sync.Map // map[string]*cachedCredential
	refreshGroup    singleflight.Group
)

func cacheKeyFor(provider schemas.ModelProvider, keyID string, rawValue string) string {
	sum := sha256.Sum256([]byte(rawValue))
	return string(provider) + ":" + keyID + ":" + hex.EncodeToString(sum[:8])
}

func httpClientWithProxy(proxyURL string) *http.Client {
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsed),
		},
	}
}

// OAuthStartResponse describes the initial step of an OAuth authorization flow.
type OAuthStartResponse struct {
	Provider                string                    `json:"provider"`
	FlowType                string                    `json:"flow_type"` // "browser" | "device"
	AuthURL                 string                    `json:"auth_url,omitempty"`
	RedirectURI             string                    `json:"redirect_uri,omitempty"`
	State                   string                    `json:"state,omitempty"`
	Verifier                string                    `json:"verifier,omitempty"`
	VerificationURI         string                    `json:"verification_uri,omitempty"`
	VerificationURIComplete string                    `json:"verification_uri_complete,omitempty"`
	UserCode                string                    `json:"user_code,omitempty"`
	ExpiresIn               int                       `json:"expires_in,omitempty"`
	Interval                int                       `json:"interval,omitempty"`
	DeviceState             *embedded.GrokDeviceState `json:"device_state,omitempty"`
}

// OAuthCompleteRequest carries the completion parameters for browser or device OAuth flows.
type OAuthCompleteRequest struct {
	CallbackURL   string                    `json:"callback_url,omitempty"`
	Code          string                    `json:"code,omitempty"`
	State         string                    `json:"state,omitempty"`
	Expected      string                    `json:"expected,omitempty"`
	ExpectedState string                    `json:"expected_state,omitempty"`
	Verifier      string                    `json:"verifier,omitempty"`
	RedirectURI   string                    `json:"redirect_uri,omitempty"`
	DeviceState   *embedded.GrokDeviceState `json:"device_state,omitempty"`
	ProxyURL      string                    `json:"proxy_url,omitempty"`
}

func (r OAuthCompleteRequest) expectedState() string {
	if s := strings.TrimSpace(r.ExpectedState); s != "" {
		return s
	}
	if s := strings.TrimSpace(r.Expected); s != "" {
		return s
	}
	return strings.TrimSpace(r.State)
}

// OAuthCredentialResult represents a normalized OAuth credential ready to store in a Bifrost Key.
type OAuthCredentialResult struct {
	Provider         string `json:"provider"`
	Status           string `json:"status"` // "authorized" | "pending" | "callback_captured" | "denied" | "expired"
	Interval         int    `json:"interval,omitempty"`
	CallbackURL      string `json:"callback_url,omitempty"`
	Error            string `json:"error,omitempty"`
	CredentialJSON   string `json:"credential_json,omitempty"`
	Email            string `json:"email,omitempty"`
	AccountID        string `json:"account_id,omitempty"`
	ProjectID        string `json:"project_id,omitempty"`
	PlanType         string `json:"plan_type,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	Expire           string `json:"expire,omitempty"`
}

// OAuthQuotaWindow represents a rate-limit or subscription usage window.
type OAuthQuotaWindow struct {
	Name          string  `json:"name"`
	UsedPercent   float64 `json:"used_percent"`
	ResetAt       int64   `json:"reset_at,omitempty"`
	ResetAfterSec int64   `json:"reset_after_seconds,omitempty"`
	WindowSeconds int64   `json:"window_seconds,omitempty"`
	LimitReached  bool    `json:"limit_reached,omitempty"`
	Allowed       bool    `json:"allowed,omitempty"`
}

// OAuthQuotaResponse represents live subscription usage, quota windows, and discovered models.
type OAuthQuotaResponse struct {
	Provider          string             `json:"provider"`
	Email             string             `json:"email,omitempty"`
	AccountID         string             `json:"account_id,omitempty"`
	ProjectID         string             `json:"project_id,omitempty"`
	PlanType          string             `json:"plan_type,omitempty"`
	SubscriptionTier  string             `json:"subscription_tier,omitempty"`
	Allowed           bool               `json:"allowed"`
	LimitReached      bool               `json:"limit_reached"`
	HasCredits        *bool              `json:"has_credits,omitempty"`
	Unlimited         *bool              `json:"unlimited,omitempty"`
	CreditBalance     string             `json:"credit_balance,omitempty"`
	Windows           []OAuthQuotaWindow `json:"windows,omitempty"`
	Models            []string           `json:"models,omitempty"`
	UpdatedCredential string             `json:"updated_credential,omitempty"`
}

type capturedBrowserCallback struct {
	Code   string
	State  string
	RawURL string
	Err    error
}

var (
	localCallbackServers     sync.Map // map[string]bool (port -> running)
	capturedBrowserCallbacks sync.Map // map[string]capturedBrowserCallback (state -> callback)
)

func ensureLocalCallbackServer(redirectURI string) {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Host == "" {
		return
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" {
		return
	}
	if _, loaded := localCallbackServers.LoadOrStore(port, true); loaded {
		return
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		localCallbackServers.Delete(port)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state := strings.TrimSpace(q.Get("state"))
		code := strings.TrimSpace(q.Get("code"))
		errParam := strings.TrimSpace(q.Get("error"))
		errDesc := strings.TrimSpace(q.Get("error_description"))
		fullCallbackURL := "http://localhost:" + port + r.URL.RequestURI()
		if state != "" {
			if errParam != "" {
				msg := errParam
				if errDesc != "" {
					msg += " (" + errDesc + ")"
				}
				capturedBrowserCallbacks.Store(state, capturedBrowserCallback{
					State:  state,
					RawURL: fullCallbackURL,
					Err:    fmt.Errorf("oauth callback error: %s", msg),
				})
			} else if code != "" {
				capturedBrowserCallbacks.Store(state, capturedBrowserCallback{
					Code:   code,
					State:  state,
					RawURL: fullCallbackURL,
				})
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		pageHTML := fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><title>Bifrost OAuth Callback</title><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#09090b;color:#fafafa;padding:16px}.card{border:1px solid #27272a;border-radius:12px;padding:28px;max-width:560px;width:100%%;background:#18181b;box-shadow:0 10px 30px rgba(0,0,0,.4)}h2{margin:0 0 10px;color:#22c55e;font-size:20px}p{margin:0 0 16px;color:#a1a1aa;font-size:14px;line-height:1.6}.box{display:flex;gap:8px;align-items:center;margin-top:12px}input{flex:1;background:#09090b;border:1px solid #3f3f46;color:#e4e4e7;border-radius:6px;padding:8px 10px;font-family:monospace;font-size:12px}button{background:#22c55e;color:#052e16;border:none;border-radius:6px;padding:8px 14px;font-weight:600;font-size:12px;cursor:pointer;white-space:nowrap}button:hover{background:#16a34a}</style></head><body><div class="card"><h2>✓ OAuth 授权回调已成功捕获</h2><p>Bifrost 正在后台自动换取凭证并回填至控制台，请切回 Bifrost 页面查看。<br/>如果控制台未自动完成，您也可以复制下方完整回调 URL 粘贴到 Bifrost 窗口中：</p><div class="box"><input id="cb" readonly value=%q onclick="this.select()"/><button onclick="navigator.clipboard.writeText(document.getElementById('cb').value);this.textContent='已复制 ✓'">复制回调 URL</button></div></div></body></html>`, fullCallbackURL)
		_, _ = w.Write([]byte(pageHTML))
	})
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = srv.Serve(ln)
		localCallbackServers.Delete(port)
	}()
}

// StartOAuthFlow begins a browser PKCE or device-code OAuth flow for the specified provider.
func StartOAuthFlow(ctx context.Context, provider schemas.ModelProvider, proxyURL string) (*OAuthStartResponse, error) {
	switch provider {
	case schemas.Antigravity:
		auth, err := embedded.BeginAntigravityBrowserAuthorization()
		if err != nil {
			return nil, err
		}
		ensureLocalCallbackServer(embedded.AntigravityRedirectURI)
		return &OAuthStartResponse{
			Provider:    string(provider),
			FlowType:    "browser",
			AuthURL:     auth.AuthorizationURL,
			RedirectURI: embedded.AntigravityRedirectURI,
			State:       auth.State,
			Verifier:    auth.CodeVerifier,
		}, nil
	case schemas.Codex:
		auth, err := embedded.BeginCodexBrowserAuthorization()
		if err != nil {
			return nil, err
		}
		ensureLocalCallbackServer(embedded.CodexRedirectURI)
		return &OAuthStartResponse{
			Provider:    string(provider),
			FlowType:    "browser",
			AuthURL:     auth.AuthorizationURL,
			RedirectURI: embedded.CodexRedirectURI,
			State:       auth.State,
			Verifier:    auth.CodeVerifier,
		}, nil
	case schemas.ClaudeOAuth:
		auth, err := embedded.BeginClaudeBrowserAuthorization()
		if err != nil {
			return nil, err
		}
		ensureLocalCallbackServer(embedded.ClaudeRedirectURI)
		return &OAuthStartResponse{
			Provider:    string(provider),
			FlowType:    "browser",
			AuthURL:     auth.AuthorizationURL,
			RedirectURI: embedded.ClaudeRedirectURI,
			State:       auth.State,
			Verifier:    auth.CodeVerifier,
		}, nil
	case schemas.GrokOAuth:
		auth, err := embedded.BeginGrokDeviceAuthorization(ctx, embedded.GrokOptions{
			HTTPClient: httpClientWithProxy(proxyURL),
		})
		if err != nil {
			return nil, err
		}
		state := auth.State
		expiresIn := int(time.Until(auth.ExpiresAt).Seconds())
		if expiresIn <= 0 {
			expiresIn = 600
		}
		interval := int(auth.PollInterval.Seconds())
		if interval <= 0 {
			interval = 5
		}
		return &OAuthStartResponse{
			Provider:                string(provider),
			FlowType:                "device",
			VerificationURI:         auth.VerificationURL,
			VerificationURIComplete: auth.VerificationURL,
			UserCode:                auth.UserCode,
			ExpiresIn:               expiresIn,
			Interval:                interval,
			DeviceState:             &state,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported OAuth provider: %s", provider)
	}
}

func extractCodeAndState(req OAuthCompleteRequest) (code string, state string, err error) {
	code = strings.TrimSpace(req.Code)
	state = strings.TrimSpace(req.State)
	rawCallback := strings.TrimSpace(req.CallbackURL)
	if rawCallback != "" {
		if strings.Contains(rawCallback, "://") || strings.HasPrefix(rawCallback, "?") || strings.Contains(rawCallback, "code=") {
			parsedURL := rawCallback
			if !strings.Contains(parsedURL, "://") {
				if !strings.HasPrefix(parsedURL, "?") {
					parsedURL = "?" + parsedURL
				}
				parsedURL = "http://localhost" + parsedURL
			}
			u, parseErr := url.Parse(parsedURL)
			if parseErr != nil {
				return "", "", fmt.Errorf("invalid callback URL: %w", parseErr)
			}
			q := u.Query()
			if errMsg := strings.TrimSpace(q.Get("error")); errMsg != "" {
				desc := strings.TrimSpace(q.Get("error_description"))
				if desc != "" {
					return "", "", fmt.Errorf("oauth callback error: %s (%s)", errMsg, desc)
				}
				return "", "", fmt.Errorf("oauth callback error: %s", errMsg)
			}
			if c := strings.TrimSpace(q.Get("code")); c != "" {
				code = c
			}
			if s := strings.TrimSpace(q.Get("state")); s != "" {
				state = s
			}
			if u.Fragment != "" && code == "" {
				if fragQ, fragErr := url.ParseQuery(u.Fragment); fragErr == nil {
					if c := strings.TrimSpace(fragQ.Get("code")); c != "" {
						code = c
					}
					if s := strings.TrimSpace(fragQ.Get("state")); s != "" {
						state = s
					}
				}
			}
		} else if code == "" {
			if idx := strings.IndexByte(rawCallback, '#'); idx > 0 {
				code = strings.TrimSpace(rawCallback[:idx])
				if state == "" {
					state = strings.TrimSpace(rawCallback[idx+1:])
				}
			} else {
				code = rawCallback
			}
		}
	}
	if code != "" && strings.Contains(code, "#") {
		parts := strings.SplitN(code, "#", 2)
		code = strings.TrimSpace(parts[0])
		if state == "" {
			state = strings.TrimSpace(parts[1])
		}
	}
	if state == "" {
		state = req.expectedState()
	}
	if code == "" {
		return "", "", fmt.Errorf("authorization code or callback URL is required")
	}
	return code, state, nil
}

// CompleteOAuthFlow completes a browser PKCE callback or polls a device-code OAuth flow.
func CompleteOAuthFlow(ctx context.Context, provider schemas.ModelProvider, req OAuthCompleteRequest) (*OAuthCredentialResult, error) {
	httpClient := httpClientWithProxy(req.ProxyURL)
	var capturedRawURL string
	expected := req.expectedState()
	if provider == schemas.Antigravity || provider == schemas.Codex || provider == schemas.ClaudeOAuth {
		if strings.TrimSpace(req.Code) == "" && strings.TrimSpace(req.CallbackURL) == "" && expected != "" {
			val, ok := capturedBrowserCallbacks.Load(expected)
			if !ok {
				return &OAuthCredentialResult{
					Provider: string(provider),
					Status:   "pending",
					Interval: 2,
				}, nil
			}
			captured := val.(capturedBrowserCallback)
			if captured.Err != nil {
				capturedBrowserCallbacks.Delete(expected)
				return nil, captured.Err
			}
			req.Code = captured.Code
			req.State = captured.State
			capturedRawURL = captured.RawURL
			capturedBrowserCallbacks.Delete(expected)
		}
	}
	switch provider {
	case schemas.Antigravity:
		code, state, err := extractCodeAndState(req)
		if err != nil {
			return nil, err
		}
		if expected == "" {
			expected = state
		}
		cred, err := embedded.CompleteAntigravityBrowserAuthorization(ctx, embedded.BrowserAuthorizationCompletion{
			Code:          code,
			ReturnedState: state,
			ExpectedState: expected,
			CodeVerifier:  strings.TrimSpace(req.Verifier),
		}, embedded.AntigravityOptions{HTTPClient: httpClient})
		if err != nil {
			if capturedRawURL != "" {
				return &OAuthCredentialResult{
					Provider:    string(provider),
					Status:      "callback_captured",
					CallbackURL: capturedRawURL,
					Error:       err.Error(),
				}, nil
			}
			return nil, err
		}
		res, err := buildAntigravityResult(cred)
		if err == nil && res != nil && capturedRawURL != "" {
			res.CallbackURL = capturedRawURL
		}
		return res, err
	case schemas.Codex:
		code, state, err := extractCodeAndState(req)
		if err != nil {
			return nil, err
		}
		if expected == "" {
			expected = state
		}
		cred, err := embedded.CompleteCodexBrowserAuthorization(ctx, embedded.BrowserAuthorizationCompletion{
			Code:          code,
			ReturnedState: state,
			ExpectedState: expected,
			CodeVerifier:  strings.TrimSpace(req.Verifier),
		}, embedded.Options{HTTPClient: httpClient})
		if err != nil {
			if capturedRawURL != "" {
				return &OAuthCredentialResult{
					Provider:    string(provider),
					Status:      "callback_captured",
					CallbackURL: capturedRawURL,
					Error:       err.Error(),
				}, nil
			}
			return nil, err
		}
		res, err := buildCodexResult(cred)
		if err == nil && res != nil && capturedRawURL != "" {
			res.CallbackURL = capturedRawURL
		}
		return res, err
	case schemas.ClaudeOAuth:
		code, state, err := extractCodeAndState(req)
		if err != nil {
			return nil, err
		}
		if expected == "" {
			expected = state
		}
		cred, err := embedded.CompleteClaudeBrowserAuthorization(ctx, embedded.BrowserAuthorizationCompletion{
			Code:          code,
			ReturnedState: state,
			ExpectedState: expected,
			CodeVerifier:  strings.TrimSpace(req.Verifier),
		}, embedded.ClaudeOptions{HTTPClient: httpClient})
		if err != nil {
			if capturedRawURL != "" {
				return &OAuthCredentialResult{
					Provider:    string(provider),
					Status:      "callback_captured",
					CallbackURL: capturedRawURL,
					Error:       err.Error(),
				}, nil
			}
			return nil, err
		}
		res, err := buildClaudeResult(cred)
		if err == nil && res != nil && capturedRawURL != "" {
			res.CallbackURL = capturedRawURL
		}
		return res, err
	case schemas.GrokOAuth:
		if req.DeviceState == nil {
			return nil, fmt.Errorf("device_state is required for Grok OAuth polling")
		}
		poll, err := embedded.PollGrokDeviceAuthorizationOnce(ctx, *req.DeviceState, embedded.GrokOptions{
			HTTPClient: httpClient,
		})
		if err != nil {
			return nil, err
		}
		if poll.Status != embedded.GrokDeviceAuthorized {
			return &OAuthCredentialResult{
				Provider: string(provider),
				Status:   string(poll.Status),
				Interval: int(poll.PollInterval.Seconds()),
			}, nil
		}
		return buildGrokResult(poll.Credential)
	default:
		return nil, fmt.Errorf("unsupported OAuth provider: %s", provider)
	}
}

// ImportCredentialJSON validates and normalizes an imported JSON credential file/payload.
// If the payload already conforms to the canonical credential schema, it is accepted immediately;
// otherwise it falls back to upstream enrichment via Import*Credential.
func ImportCredentialJSON(ctx context.Context, provider schemas.ModelProvider, raw []byte, proxyURL string) (*OAuthCredentialResult, error) {
	httpClient := httpClientWithProxy(proxyURL)
	switch provider {
	case schemas.Antigravity:
		if cred, err := embedded.ParseAntigravityCredentialJSON(raw); err == nil {
			return buildAntigravityResult(cred)
		}
		cred, err := embedded.ImportAntigravityCredential(ctx, raw, embedded.AntigravityOptions{HTTPClient: httpClient})
		if err != nil {
			return nil, err
		}
		return buildAntigravityResult(cred)
	case schemas.Codex:
		if cred, err := embedded.ParseCodexCredentialJSON(raw); err == nil {
			return buildCodexResult(cred)
		}
		cred, err := embedded.ImportCodexCredential(ctx, raw, embedded.Options{HTTPClient: httpClient})
		if err != nil {
			return nil, err
		}
		return buildCodexResult(cred)
	case schemas.ClaudeOAuth:
		if cred, err := embedded.ParseClaudeCredentialJSON(raw); err == nil {
			return buildClaudeResult(cred)
		}
		cred, err := embedded.ImportClaudeCredential(ctx, raw, embedded.ClaudeOptions{HTTPClient: httpClient})
		if err != nil {
			return nil, err
		}
		return buildClaudeResult(cred)
	case schemas.GrokOAuth:
		if cred, err := embedded.ParseGrokCredentialJSON(raw); err == nil {
			return buildGrokResult(cred)
		}
		cred, err := embedded.ImportGrokCredential(ctx, raw, embedded.GrokOptions{HTTPClient: httpClient})
		if err != nil {
			return nil, err
		}
		return buildGrokResult(cred)
	default:
		return nil, fmt.Errorf("unsupported OAuth provider: %s", provider)
	}
}

func buildAntigravityResult(cred embedded.AntigravityCredential) (*OAuthCredentialResult, error) {
	if cred.Type == "" {
		cred.Type = "antigravity"
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	return &OAuthCredentialResult{
		Provider:       string(schemas.Antigravity),
		Status:         "authorized",
		CredentialJSON: string(raw),
		Email:          cred.Email,
		AccountID:      cred.AccountID,
		ProjectID:      cred.ProjectID,
		Expire:         cred.Expire,
	}, nil
}

func buildCodexResult(cred embedded.CodexCredential) (*OAuthCredentialResult, error) {
	if cred.Type == "" {
		cred.Type = "codex"
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	return &OAuthCredentialResult{
		Provider:       string(schemas.Codex),
		Status:         "authorized",
		CredentialJSON: string(raw),
		Email:          cred.Email,
		AccountID:      cred.AccountID,
		PlanType:       cred.PlanType,
		Expire:         cred.Expire,
	}, nil
}

func buildClaudeResult(cred embedded.ClaudeCredential) (*OAuthCredentialResult, error) {
	if cred.Type == "" {
		cred.Type = "claude"
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	return &OAuthCredentialResult{
		Provider:         string(schemas.ClaudeOAuth),
		Status:           "authorized",
		CredentialJSON:   string(raw),
		Email:            cred.Email,
		AccountID:        cred.AccountUUID,
		OrganizationName: cred.OrganizationName,
		Expire:           cred.Expire,
	}, nil
}

func buildGrokResult(cred embedded.GrokCredential) (*OAuthCredentialResult, error) {
	if cred.Type == "" {
		cred.Type = "grok"
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	return &OAuthCredentialResult{
		Provider:       string(schemas.GrokOAuth),
		Status:         "authorized",
		CredentialJSON: string(raw),
		Email:          cred.Email,
		AccountID:      cred.AccountID,
		Expire:         cred.Expire,
	}, nil
}

// ObserveAccountQuota queries live subscription quota/credits and available models for a credential.
func ObserveAccountQuota(ctx context.Context, provider schemas.ModelProvider, rawCredential string, baseURL string, proxyURL string) (*OAuthQuotaResponse, error) {
	rawCredential = strings.TrimSpace(rawCredential)
	if rawCredential == "" {
		return nil, fmt.Errorf("credential is required")
	}
	httpClient := httpClientWithProxy(proxyURL)

	switch provider {
	case schemas.Antigravity:
		cred, err := resolveAntigravityCredential(ctx, "quota-check", rawCredential, proxyURL, false)
		if err != nil {
			return nil, err
		}
		opts := embedded.AntigravityOptions{HTTPClient: httpClient}
		obs, err := embedded.ObserveAntigravityAccount(ctx, cred, opts)
		if err != nil {
			return nil, err
		}
		tier := obs.CurrentTierID
		if tier == "" {
			tier = obs.PlanID
		}
		out := &OAuthQuotaResponse{
			Provider:         string(provider),
			Email:            cred.Email,
			AccountID:        cred.AccountID,
			ProjectID:        cred.ProjectID,
			PlanType:         obs.PlanID,
			SubscriptionTier: tier,
			Allowed:          true,
		}
		if obs.GoogleOneAICredits != nil {
			hasCreds := obs.GoogleOneAICredits.Amount > 0
			out.HasCredits = &hasCreds
			out.CreditBalance = fmt.Sprintf("%.0f", obs.GoogleOneAICredits.Amount)
		}
		for _, g := range obs.QuotaGroups {
			for _, b := range g.Buckets {
				usedPct := 0.0
				if b.RemainingFraction != nil {
					usedPct = (1.0 - *b.RemainingFraction) * 100.0
					if usedPct < 0 {
						usedPct = 0
					}
					if usedPct > 100 {
						usedPct = 100
					}
				}
				name := g.DisplayName
				if b.DisplayName != "" && b.DisplayName != g.DisplayName {
					name = g.DisplayName + " (" + b.DisplayName + ")"
				}
				win := OAuthQuotaWindow{
					Name:         name,
					UsedPercent:  usedPct,
					LimitReached: usedPct >= 100,
					Allowed:      usedPct < 100,
				}
				if b.ResetTime != "" {
					if t, parseErr := time.Parse(time.RFC3339, b.ResetTime); parseErr == nil {
						win.ResetAt = t.Unix()
					}
				}
				if win.LimitReached {
					out.LimitReached = true
				}
				out.Windows = append(out.Windows, win)
			}
		}
		if models, mErr := embedded.DiscoverAntigravityModels(ctx, cred, opts); mErr == nil {
			for _, m := range models {
				if m.ID != "" {
					out.Models = append(out.Models, m.ID)
				}
			}
		}
		return out, nil

	case schemas.Codex:
		cred, err := resolveCodexCredential(ctx, "quota-check", rawCredential, proxyURL, false)
		if err != nil {
			return nil, err
		}
		obs, err := embedded.ObserveCodexAccount(ctx, cred, baseURL, embedded.Options{HTTPClient: httpClient})
		if err != nil {
			return nil, err
		}
		var parsed struct {
			PlanType  string `json:"plan_type"`
			RateLimit *struct {
				Allowed         bool `json:"allowed"`
				LimitReached    bool `json:"limit_reached"`
				PrimaryWindow   *struct {
					UsedPercent        float64 `json:"used_percent"`
					ResetAt            int64   `json:"reset_at"`
					ResetAfterSeconds  int64   `json:"reset_after_seconds"`
					LimitWindowSeconds int64   `json:"limit_window_seconds"`
				} `json:"primary_window"`
				SecondaryWindow *struct {
					UsedPercent        float64 `json:"used_percent"`
					ResetAt            int64   `json:"reset_at"`
					ResetAfterSeconds  int64   `json:"reset_after_seconds"`
					LimitWindowSeconds int64   `json:"limit_window_seconds"`
				} `json:"secondary_window"`
			} `json:"rate_limit"`
			Credits *struct {
				HasCredits bool   `json:"has_credits"`
				Unlimited  bool   `json:"unlimited"`
				Balance    string `json:"balance"`
			} `json:"credits"`
		}
		_ = json.Unmarshal(obs.Payload, &parsed)
		plan := parsed.PlanType
		if plan == "" {
			plan = cred.PlanType
		}
		out := &OAuthQuotaResponse{
			Provider:  string(provider),
			Email:     cred.Email,
			AccountID: cred.AccountID,
			PlanType:  plan,
			Allowed:   true,
		}
		if parsed.RateLimit != nil {
			out.Allowed = parsed.RateLimit.Allowed
			out.LimitReached = parsed.RateLimit.LimitReached
			if w := parsed.RateLimit.PrimaryWindow; w != nil {
				out.Windows = append(out.Windows, OAuthQuotaWindow{
					Name:          "Primary Window (5h)",
					UsedPercent:   w.UsedPercent,
					ResetAt:       w.ResetAt,
					ResetAfterSec: w.ResetAfterSeconds,
					WindowSeconds: w.LimitWindowSeconds,
					Allowed:       parsed.RateLimit.Allowed,
					LimitReached:  w.UsedPercent >= 100,
				})
			}
			if w := parsed.RateLimit.SecondaryWindow; w != nil {
				out.Windows = append(out.Windows, OAuthQuotaWindow{
					Name:          "Secondary Window (Weekly)",
					UsedPercent:   w.UsedPercent,
					ResetAt:       w.ResetAt,
					ResetAfterSec: w.ResetAfterSeconds,
					WindowSeconds: w.LimitWindowSeconds,
					Allowed:       parsed.RateLimit.Allowed,
					LimitReached:  w.UsedPercent >= 100,
				})
			}
		}
		if parsed.Credits != nil {
			out.HasCredits = &parsed.Credits.HasCredits
			out.Unlimited = &parsed.Credits.Unlimited
			out.CreditBalance = parsed.Credits.Balance
		}
		return out, nil

	case schemas.ClaudeOAuth:
		cred, err := resolveClaudeCredential(ctx, "quota-check", rawCredential, proxyURL, false)
		if err != nil {
			return nil, err
		}
		opts := embedded.ClaudeOptions{HTTPClient: httpClient}
		obs, err := embedded.ObserveClaudeAccount(ctx, cred, opts)
		if err != nil {
			return nil, err
		}
		email := obs.Profile.Email
		if email == "" {
			email = cred.Email
		}
		acct := obs.Profile.AccountUUID
		if acct == "" {
			acct = cred.AccountUUID
		}
		tier := obs.Profile.UserRateLimitTier
		if tier == "" {
			tier = obs.Profile.OrganizationRateLimitTier
		}
		out := &OAuthQuotaResponse{
			Provider:         string(provider),
			Email:            email,
			AccountID:        acct,
			PlanType:         obs.Profile.OrganizationName,
			SubscriptionTier: tier,
			Allowed:          true,
		}
		appendClaudeWin := func(name string, w *embedded.ClaudeUsageWindow) {
			if w == nil || w.Utilization == nil {
				return
			}
			pct := *w.Utilization
			if pct <= 1.0 && pct > 0 {
				pct = pct * 100.0
			}
			win := OAuthQuotaWindow{
				Name:         name,
				UsedPercent:  pct,
				LimitReached: pct >= 100,
				Allowed:      pct < 100,
			}
			if w.ResetsAt != nil && *w.ResetsAt != "" {
				if t, pErr := time.Parse(time.RFC3339, *w.ResetsAt); pErr == nil {
					win.ResetAt = t.Unix()
				}
			}
			if win.LimitReached {
				out.LimitReached = true
			}
			out.Windows = append(out.Windows, win)
		}
		appendClaudeWin("5-Hour Window", obs.Usage.FiveHour)
		appendClaudeWin("7-Day Window", obs.Usage.SevenDay)
		appendClaudeWin("7-Day Opus Window", obs.Usage.SevenDayOpus)
		appendClaudeWin("7-Day Sonnet Window", obs.Usage.SevenDaySonnet)
		for _, lim := range obs.Usage.Limits {
			name := lim.Kind
			if lim.ModelDisplayName != "" {
				name = lim.ModelDisplayName
			}
			win := OAuthQuotaWindow{
				Name:         name,
				UsedPercent:  lim.Percent,
				LimitReached: lim.Percent >= 100,
				Allowed:      lim.Percent < 100,
			}
			if lim.ResetsAt != nil && *lim.ResetsAt != "" {
				if t, pErr := time.Parse(time.RFC3339, *lim.ResetsAt); pErr == nil {
					win.ResetAt = t.Unix()
				}
			}
			out.Windows = append(out.Windows, win)
		}
		if obs.Usage.ExtraUsage != nil {
			out.HasCredits = obs.Usage.ExtraUsage.Enabled
			if obs.Usage.ExtraUsage.UsedCredits != nil && obs.Usage.ExtraUsage.MonthlyLimit != nil {
				out.CreditBalance = fmt.Sprintf("%.2f / %.2f", *obs.Usage.ExtraUsage.UsedCredits, *obs.Usage.ExtraUsage.MonthlyLimit)
			}
		}
		return out, nil

	case schemas.GrokOAuth:
		cred, err := resolveGrokCredential(ctx, "quota-check", rawCredential, proxyURL, false)
		if err != nil {
			return nil, err
		}
		opts := embedded.GrokOptions{HTTPClient: httpClient}
		obs, err := embedded.ObserveGrokAccount(ctx, cred, opts)
		if err != nil {
			return nil, err
		}
		out := &OAuthQuotaResponse{
			Provider:  string(provider),
			Email:     cred.Email,
			AccountID: cred.AccountID,
			PlanType:  obs.Billing.PeriodType,
			Allowed:   true,
		}
		if obs.Tier != nil {
			out.SubscriptionTier = fmt.Sprintf("Tier %d", *obs.Tier)
		}
		if obs.Billing.UsagePercent != nil {
			pct := *obs.Billing.UsagePercent
			win := OAuthQuotaWindow{
				Name:         "Billing Period Usage",
				UsedPercent:  pct,
				LimitReached: pct >= 100,
				Allowed:      pct < 100,
			}
			if obs.Billing.PeriodEnd != "" {
				if t, pErr := time.Parse(time.RFC3339, obs.Billing.PeriodEnd); pErr == nil {
					win.ResetAt = t.Unix()
				}
			}
			out.Windows = append(out.Windows, win)
		}
		for _, p := range obs.Billing.ProductUsage {
			if p.UsagePercent != nil {
				out.Windows = append(out.Windows, OAuthQuotaWindow{
					Name:         p.Product,
					UsedPercent:  *p.UsagePercent,
					LimitReached: *p.UsagePercent >= 100,
					Allowed:      *p.UsagePercent < 100,
				})
			}
		}
		if obs.Billing.UsedCents != nil && obs.Billing.MonthlyLimitCents != nil {
			out.CreditBalance = fmt.Sprintf("$%.2f / $%.2f", *obs.Billing.UsedCents/100.0, *obs.Billing.MonthlyLimitCents/100.0)
		}
		return out, nil

	default:
		return nil, fmt.Errorf("unsupported OAuth provider: %s", provider)
	}
}

func fillAntigravityDefaults(c *embedded.AntigravityCredential) {
	if c.Type == "" {
		c.Type = "antigravity"
	}
	if c.RefreshToken == "" {
		c.RefreshToken = "none"
	}
	if c.AccountID == "" {
		c.AccountID = "default-account"
	}
	if c.Email == "" {
		c.Email = "user@antigravity.local"
	}
	if c.ProjectID == "" {
		c.ProjectID = "default-project"
	}
	if c.Expire == "" {
		c.Expire = "2099-01-01T00:00:00Z"
	}
}

func fillCodexDefaults(c *embedded.CodexCredential) {
	if c.Type == "" {
		c.Type = "codex"
	}
	if c.RefreshToken == "" {
		c.RefreshToken = "none"
	}
	if c.AccountID == "" {
		c.AccountID = "default-account"
	}
	if c.Expire == "" {
		c.Expire = "2099-01-01T00:00:00Z"
	}
}

func fillClaudeDefaults(c *embedded.ClaudeCredential) {
	if c.Type == "" {
		c.Type = "claude"
	}
	if c.RefreshToken == "" {
		c.RefreshToken = "none"
	}
	if c.AccountUUID == "" {
		c.AccountUUID = "00000000-0000-0000-0000-000000000000"
	}
	if len(c.DeviceIDs) == 0 {
		c.DeviceIDs = []string{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	}
	if c.Expire == "" {
		c.Expire = "2099-01-01T00:00:00Z"
	}
}

func fillGrokDefaults(c *embedded.GrokCredential) {
	if c.Type == "" {
		c.Type = "grok"
	}
	if c.RefreshToken == "" {
		c.RefreshToken = "none"
	}
	if c.AccountID == "" {
		c.AccountID = "default-account"
	}
	if c.Email == "" {
		c.Email = "user@x.ai"
	}
	if c.Expire == "" {
		c.Expire = "2099-01-01T00:00:00Z"
	}
	if c.TokenEndpoint == "" {
		c.TokenEndpoint = "https://accounts.x.ai/oauth2/token"
	}
}

func hasRealRefreshToken(rt string) bool {
	trimmed := strings.TrimSpace(rt)
	return trimmed != "" && trimmed != "none"
}

func parseOrWrapAntigravity(raw string) (embedded.AntigravityCredential, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return embedded.AntigravityCredential{}, fmt.Errorf("empty Antigravity credential")
	}
	if strings.HasPrefix(trimmed, "{") {
		cred, err := embedded.ParseAntigravityCredentialJSON([]byte(trimmed))
		if err == nil {
			return cred, nil
		}
		var relaxed embedded.AntigravityCredential
		if uErr := json.Unmarshal([]byte(trimmed), &relaxed); uErr == nil && (relaxed.AccessToken != "" || relaxed.RefreshToken != "") {
			fillAntigravityDefaults(&relaxed)
			return relaxed, nil
		}
		return embedded.AntigravityCredential{}, err
	}
	cred := embedded.AntigravityCredential{AccessToken: trimmed}
	fillAntigravityDefaults(&cred)
	return cred, nil
}

func parseOrWrapCodex(raw string) (embedded.CodexCredential, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return embedded.CodexCredential{}, fmt.Errorf("empty Codex credential")
	}
	if strings.HasPrefix(trimmed, "{") {
		cred, err := embedded.ParseCodexCredentialJSON([]byte(trimmed))
		if err == nil {
			return cred, nil
		}
		var relaxed embedded.CodexCredential
		if uErr := json.Unmarshal([]byte(trimmed), &relaxed); uErr == nil && (relaxed.AccessToken != "" || relaxed.RefreshToken != "") {
			fillCodexDefaults(&relaxed)
			return relaxed, nil
		}
		return embedded.CodexCredential{}, err
	}
	cred := embedded.CodexCredential{AccessToken: trimmed}
	fillCodexDefaults(&cred)
	return cred, nil
}

func parseOrWrapClaude(raw string) (embedded.ClaudeCredential, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return embedded.ClaudeCredential{}, fmt.Errorf("empty Claude credential")
	}
	if strings.HasPrefix(trimmed, "{") {
		cred, err := embedded.ParseClaudeCredentialJSON([]byte(trimmed))
		if err == nil {
			return cred, nil
		}
		var relaxed embedded.ClaudeCredential
		if uErr := json.Unmarshal([]byte(trimmed), &relaxed); uErr == nil && (relaxed.AccessToken != "" || relaxed.RefreshToken != "") {
			fillClaudeDefaults(&relaxed)
			return relaxed, nil
		}
		return embedded.ClaudeCredential{}, err
	}
	cred := embedded.ClaudeCredential{AccessToken: trimmed}
	fillClaudeDefaults(&cred)
	return cred, nil
}

func parseOrWrapGrok(raw string) (embedded.GrokCredential, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return embedded.GrokCredential{}, fmt.Errorf("empty Grok credential")
	}
	if strings.HasPrefix(trimmed, "{") {
		cred, err := embedded.ParseGrokCredentialJSON([]byte(trimmed))
		if err == nil {
			return cred, nil
		}
		var relaxed embedded.GrokCredential
		if uErr := json.Unmarshal([]byte(trimmed), &relaxed); uErr == nil && (relaxed.AccessToken != "" || relaxed.RefreshToken != "") {
			fillGrokDefaults(&relaxed)
			return relaxed, nil
		}
		return embedded.GrokCredential{}, err
	}
	cred := embedded.GrokCredential{AccessToken: trimmed}
	fillGrokDefaults(&cred)
	return cred, nil
}

func shouldRefresh(expiresAt time.Time, hasExp bool, hasRefreshToken bool, force bool) bool {
	if !hasRefreshToken {
		return false
	}
	if force {
		return true
	}
	if !hasExp {
		return false
	}
	return time.Now().Add(tokenRefreshSkew).After(expiresAt)
}

func resolveAntigravityCredential(ctx context.Context, keyID string, raw string, proxyURL string, forceRefresh bool) (embedded.AntigravityCredential, error) {
	ck := cacheKeyFor(schemas.Antigravity, keyID, raw)
	var entry *cachedCredential
	if existing, ok := globalCredCache.Load(ck); ok {
		entry = existing.(*cachedCredential)
	} else {
		parsed, err := parseOrWrapAntigravity(raw)
		if err != nil {
			return embedded.AntigravityCredential{}, err
		}
		newEntry := &cachedCredential{antigravity: &parsed, rawJSON: raw}
		actual, _ := globalCredCache.LoadOrStore(ck, newEntry)
		entry = actual.(*cachedCredential)
	}

	entry.mu.RLock()
	current := *entry.antigravity
	entry.mu.RUnlock()

	exp, hasExp := embedded.AntigravityCredentialExpiresAt(current)
	needsRefresh := shouldRefresh(exp, hasExp, hasRealRefreshToken(current.RefreshToken), forceRefresh) ||
		(strings.TrimSpace(current.AccessToken) == "" && hasRealRefreshToken(current.RefreshToken))
	if !needsRefresh {
		return current, nil
	}

	val, err, _ := refreshGroup.Do(ck, func() (interface{}, error) {
		entry.mu.RLock()
		latest := *entry.antigravity
		entry.mu.RUnlock()

		latestExp, latestHasExp := embedded.AntigravityCredentialExpiresAt(latest)
		if !forceRefresh && strings.TrimSpace(latest.AccessToken) != "" &&
			!shouldRefresh(latestExp, latestHasExp, hasRealRefreshToken(latest.RefreshToken), false) {
			return latest, nil
		}

		refreshed, rErr := embedded.RefreshAntigravityCredentialOnce(ctx, latest, embedded.AntigravityOptions{
			HTTPClient: httpClientWithProxy(proxyURL),
		})
		if rErr != nil {
			if strings.TrimSpace(latest.AccessToken) != "" && !forceRefresh {
				return latest, nil
			}
			return embedded.AntigravityCredential{}, rErr
		}
		entry.mu.Lock()
		entry.antigravity = &refreshed
		entry.lastRefresh = time.Now()
		entry.mu.Unlock()
		return refreshed, nil
	})
	if err != nil {
		return embedded.AntigravityCredential{}, err
	}
	return val.(embedded.AntigravityCredential), nil
}

func resolveCodexCredential(ctx context.Context, keyID string, raw string, proxyURL string, forceRefresh bool) (embedded.CodexCredential, error) {
	ck := cacheKeyFor(schemas.Codex, keyID, raw)
	var entry *cachedCredential
	if existing, ok := globalCredCache.Load(ck); ok {
		entry = existing.(*cachedCredential)
	} else {
		parsed, err := parseOrWrapCodex(raw)
		if err != nil {
			return embedded.CodexCredential{}, err
		}
		newEntry := &cachedCredential{codex: &parsed, rawJSON: raw}
		actual, _ := globalCredCache.LoadOrStore(ck, newEntry)
		entry = actual.(*cachedCredential)
	}

	entry.mu.RLock()
	current := *entry.codex
	entry.mu.RUnlock()

	exp, hasExp := embedded.CodexCredentialExpiresAt(current)
	needsRefresh := shouldRefresh(exp, hasExp, hasRealRefreshToken(current.RefreshToken), forceRefresh) ||
		(strings.TrimSpace(current.AccessToken) == "" && hasRealRefreshToken(current.RefreshToken))
	if !needsRefresh {
		return current, nil
	}

	val, err, _ := refreshGroup.Do(ck, func() (interface{}, error) {
		entry.mu.RLock()
		latest := *entry.codex
		entry.mu.RUnlock()

		latestExp, latestHasExp := embedded.CodexCredentialExpiresAt(latest)
		if !forceRefresh && strings.TrimSpace(latest.AccessToken) != "" &&
			!shouldRefresh(latestExp, latestHasExp, hasRealRefreshToken(latest.RefreshToken), false) {
			return latest, nil
		}

		refreshed, rErr := embedded.RefreshCodexCredentialOnce(ctx, latest, embedded.Options{
			HTTPClient: httpClientWithProxy(proxyURL),
		})
		if rErr != nil {
			if strings.TrimSpace(latest.AccessToken) != "" && !forceRefresh {
				return latest, nil
			}
			return embedded.CodexCredential{}, rErr
		}
		entry.mu.Lock()
		entry.codex = &refreshed
		entry.lastRefresh = time.Now()
		entry.mu.Unlock()
		return refreshed, nil
	})
	if err != nil {
		return embedded.CodexCredential{}, err
	}
	return val.(embedded.CodexCredential), nil
}

func resolveClaudeCredential(ctx context.Context, keyID string, raw string, proxyURL string, forceRefresh bool) (embedded.ClaudeCredential, error) {
	ck := cacheKeyFor(schemas.ClaudeOAuth, keyID, raw)
	var entry *cachedCredential
	if existing, ok := globalCredCache.Load(ck); ok {
		entry = existing.(*cachedCredential)
	} else {
		parsed, err := parseOrWrapClaude(raw)
		if err != nil {
			return embedded.ClaudeCredential{}, err
		}
		newEntry := &cachedCredential{claude: &parsed, rawJSON: raw}
		actual, _ := globalCredCache.LoadOrStore(ck, newEntry)
		entry = actual.(*cachedCredential)
	}

	entry.mu.RLock()
	current := *entry.claude
	entry.mu.RUnlock()

	exp, hasExp := embedded.ClaudeCredentialExpiresAt(current)
	needsRefresh := shouldRefresh(exp, hasExp, hasRealRefreshToken(current.RefreshToken), forceRefresh) ||
		(strings.TrimSpace(current.AccessToken) == "" && hasRealRefreshToken(current.RefreshToken))
	if !needsRefresh {
		return current, nil
	}

	val, err, _ := refreshGroup.Do(ck, func() (interface{}, error) {
		entry.mu.RLock()
		latest := *entry.claude
		entry.mu.RUnlock()

		latestExp, latestHasExp := embedded.ClaudeCredentialExpiresAt(latest)
		if !forceRefresh && strings.TrimSpace(latest.AccessToken) != "" &&
			!shouldRefresh(latestExp, latestHasExp, hasRealRefreshToken(latest.RefreshToken), false) {
			return latest, nil
		}

		refreshed, rErr := embedded.RefreshClaudeCredentialOnce(ctx, latest, embedded.ClaudeOptions{
			HTTPClient: httpClientWithProxy(proxyURL),
		})
		if rErr != nil {
			if strings.TrimSpace(latest.AccessToken) != "" && !forceRefresh {
				return latest, nil
			}
			return embedded.ClaudeCredential{}, rErr
		}
		entry.mu.Lock()
		entry.claude = &refreshed
		entry.lastRefresh = time.Now()
		entry.mu.Unlock()
		return refreshed, nil
	})
	if err != nil {
		return embedded.ClaudeCredential{}, err
	}
	return val.(embedded.ClaudeCredential), nil
}

func resolveGrokCredential(ctx context.Context, keyID string, raw string, proxyURL string, forceRefresh bool) (embedded.GrokCredential, error) {
	ck := cacheKeyFor(schemas.GrokOAuth, keyID, raw)
	var entry *cachedCredential
	if existing, ok := globalCredCache.Load(ck); ok {
		entry = existing.(*cachedCredential)
	} else {
		parsed, err := parseOrWrapGrok(raw)
		if err != nil {
			return embedded.GrokCredential{}, err
		}
		newEntry := &cachedCredential{grok: &parsed, rawJSON: raw}
		actual, _ := globalCredCache.LoadOrStore(ck, newEntry)
		entry = actual.(*cachedCredential)
	}

	entry.mu.RLock()
	current := *entry.grok
	entry.mu.RUnlock()

	exp, hasExp := embedded.GrokCredentialExpiresAt(current)
	needsRefresh := shouldRefresh(exp, hasExp, hasRealRefreshToken(current.RefreshToken), forceRefresh) ||
		(strings.TrimSpace(current.AccessToken) == "" && hasRealRefreshToken(current.RefreshToken))
	if !needsRefresh {
		return current, nil
	}

	val, err, _ := refreshGroup.Do(ck, func() (interface{}, error) {
		entry.mu.RLock()
		latest := *entry.grok
		entry.mu.RUnlock()

		latestExp, latestHasExp := embedded.GrokCredentialExpiresAt(latest)
		if !forceRefresh && strings.TrimSpace(latest.AccessToken) != "" &&
			!shouldRefresh(latestExp, latestHasExp, hasRealRefreshToken(latest.RefreshToken), false) {
			return latest, nil
		}

		refreshed, rErr := embedded.RefreshGrokCredentialOnce(ctx, latest, embedded.GrokOptions{
			HTTPClient: httpClientWithProxy(proxyURL),
		})
		if rErr != nil {
			if strings.TrimSpace(latest.AccessToken) != "" && !forceRefresh {
				return latest, nil
			}
			return embedded.GrokCredential{}, rErr
		}
		entry.mu.Lock()
		entry.grok = &refreshed
		entry.lastRefresh = time.Now()
		entry.mu.Unlock()
		return refreshed, nil
	})
	if err != nil {
		return embedded.GrokCredential{}, err
	}
	return val.(embedded.GrokCredential), nil
}

func isUnauthorizedStatus(statusCode int, err error) bool {
	if statusCode == http.StatusUnauthorized {
		return true
	}
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status 401") ||
		strings.Contains(msg, "status=401") ||
		strings.Contains(msg, "http 401") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "invalid_grant")
}
