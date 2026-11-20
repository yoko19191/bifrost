package cloudflare

import (
	"fmt"
	"maps"
	"net/http"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	embeddingGemma2CanonicalModel = "@cf/google/embeddinggemma-2"
	embeddingGemma2ShortModel     = "embeddinggemma-2"
	cloudflareWorkersAIPrefix     = "workers-ai/"
)

// CloudflareEmbeddingRequest matches the Workers AI /ai/run/@cf/google/embeddinggemma-2
// input schema, which accepts either Text ("input") or Multimodal ("messages").
type CloudflareEmbeddingRequest struct {
	Input          *CloudflareEmbeddingTextInput `json:"input,omitempty"`
	Messages       *CloudflareEmbeddingMessages  `json:"messages,omitempty"`
	Dimensions     *int                          `json:"dimensions,omitempty"`
	EncodingFormat *string                       `json:"encoding_format,omitempty"`
	ExtraParams    map[string]interface{}        `json:"-"`
}

// GetExtraParams implements providerUtils.RequestBodyWithExtraParams.
func (r *CloudflareEmbeddingRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// CloudflareEmbeddingTextInput holds one of string, []string, []int, or [][]int.
type CloudflareEmbeddingTextInput struct {
	Text         *string
	Texts        []string
	Tokens       []int
	TokenBatches [][]int
}

// MarshalJSON emits the active text or token variant.
func (i CloudflareEmbeddingTextInput) MarshalJSON() ([]byte, error) {
	switch {
	case i.Text != nil:
		return schemas.MarshalSorted(*i.Text)
	case i.Texts != nil:
		return schemas.MarshalSorted(i.Texts)
	case i.Tokens != nil:
		return schemas.MarshalSorted(i.Tokens)
	case i.TokenBatches != nil:
		return schemas.MarshalSorted(i.TokenBatches)
	default:
		return nil, fmt.Errorf("cloudflare embedding text input is empty")
	}
}

// CloudflareEmbeddingMessages encodes one conversation as Message[] or multiple
// conversations as Message[][] per Workers AI's schema.
type CloudflareEmbeddingMessages struct {
	Conversations [][]CloudflareEmbeddingMessage
}

// MarshalJSON emits []CloudflareEmbeddingMessage for a single conversation and
// [][]CloudflareEmbeddingMessage for a batch.
func (m CloudflareEmbeddingMessages) MarshalJSON() ([]byte, error) {
	if len(m.Conversations) == 1 {
		return schemas.MarshalSorted(m.Conversations[0])
	}
	if len(m.Conversations) > 1 {
		return schemas.MarshalSorted(m.Conversations)
	}
	return nil, fmt.Errorf("cloudflare embedding messages are empty")
}

// CloudflareEmbeddingMessage represents one message in a Workers AI multimodal request.
type CloudflareEmbeddingMessage struct {
	Role    string                           `json:"role"`
	Content CloudflareEmbeddingMessageContent `json:"content"`
}

// CloudflareEmbeddingMessageContent marshals as a plain string or content-part array.
type CloudflareEmbeddingMessageContent struct {
	Text  *string
	Parts []CloudflareEmbeddingContentPart
}

// MarshalJSON emits a string when Text is set, otherwise the part slice.
func (c CloudflareEmbeddingMessageContent) MarshalJSON() ([]byte, error) {
	if c.Text != nil {
		return schemas.MarshalSorted(*c.Text)
	}
	if len(c.Parts) > 0 {
		return schemas.MarshalSorted(c.Parts)
	}
	return nil, fmt.Errorf("cloudflare embedding message content is empty")
}

// CloudflareEmbeddingContentPart represents one text or media part inside a message.
type CloudflareEmbeddingContentPart struct {
	Type       string                `json:"type"`
	Text       *string               `json:"text,omitempty"`
	ImageURL   *CloudflareMediaURL   `json:"image_url,omitempty"`
	VideoURL   *CloudflareMediaURL   `json:"video_url,omitempty"`
	AudioURL   *CloudflareMediaURL   `json:"audio_url,omitempty"`
	InputAudio *CloudflareInputAudio `json:"input_audio,omitempty"`
}

// CloudflareMediaURL carries a base64 data URI for image_url, video_url, or audio_url parts.
type CloudflareMediaURL struct {
	URL string `json:"url"`
}

// CloudflareInputAudio carries base64-encoded audio and format for input_audio parts.
type CloudflareInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// CloudflareEmbeddingResponseEnvelope decodes Workers AI /ai/run responses, with fallback
// fields for unwrapped OpenAI-format responses.
type CloudflareEmbeddingResponseEnvelope struct {
	Result  *schemas.BifrostEmbeddingResponse `json:"result"`
	Success *bool                             `json:"success"`
	Data    []schemas.EmbeddingData           `json:"data,omitempty"`
	Model   string                            `json:"model,omitempty"`
	Object  string                            `json:"object,omitempty"`
	Usage   *schemas.BifrostLLMUsage          `json:"usage,omitempty"`
}

func stripCloudflareModelPrefix(model string) string {
	trimmed := strings.TrimSpace(model)
	if len(trimmed) > len(cloudflareWorkersAIPrefix) && strings.EqualFold(trimmed[:len(cloudflareWorkersAIPrefix)], cloudflareWorkersAIPrefix) {
		return trimmed[len(cloudflareWorkersAIPrefix):]
	}
	return trimmed
}

func isEmbeddingGemma2Model(model string) bool {
	normalized := strings.ToLower(stripCloudflareModelPrefix(model))
	return normalized == embeddingGemma2CanonicalModel || normalized == embeddingGemma2ShortModel
}

func shouldUseNativeCloudflareEmbedding(request *schemas.BifrostEmbeddingRequest) bool {
	if request == nil {
		return false
	}
	if isEmbeddingGemma2Model(request.Model) {
		return true
	}
	for _, item := range request.Input {
		if len(item.Messages) > 0 || item.Params != nil {
			return true
		}
		for _, part := range item.Content {
			if part.Type != schemas.EmbeddingContentPartTypeText && part.Type != schemas.EmbeddingContentPartTypeTokens {
				return true
			}
		}
	}
	return false
}

func nativeEmbeddingModelPath(model string) (string, *schemas.BifrostError) {
	clean := stripCloudflareModelPrefix(model)
	if strings.EqualFold(clean, embeddingGemma2ShortModel) {
		clean = embeddingGemma2CanonicalModel
	}
	if clean == "" {
		return "", providerUtils.NewBifrostBadRequestError("model is required for cloudflare embedding")
	}
	escaped, err := providerUtils.EscapeResourcePath(clean, "model")
	if err != nil {
		return "", err
	}
	return "/ai/run/" + escaped, nil
}

// ToCloudflareEmbeddingRequest converts a BifrostEmbeddingRequest into Cloudflare Workers AI's
// /ai/run request body.
func ToCloudflareEmbeddingRequest(request *schemas.BifrostEmbeddingRequest) (*CloudflareEmbeddingRequest, error) {
	if request == nil || len(request.Input) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("embedding input is not provided")
	}

	dims, encodingFmt, extraParams, err := resolveCloudflareEmbeddingParams(request)
	if err != nil {
		return nil, err
	}

	cfReq := &CloudflareEmbeddingRequest{
		Dimensions:     dims,
		EncodingFormat: encodingFmt,
		ExtraParams:    extraParams,
	}

	isGemma2 := isEmbeddingGemma2Model(request.Model)

	if canUseCloudflareTextInput(request.Input) {
		textInput, err := buildCloudflareTextInput(request, isGemma2)
		if err != nil {
			return nil, err
		}
		cfReq.Input = textInput
		return cfReq, nil
	}

	conversations, err := buildCloudflareMessages(request, isGemma2)
	if err != nil {
		return nil, err
	}
	cfReq.Messages = &CloudflareEmbeddingMessages{Conversations: conversations}
	return cfReq, nil
}

func resolveCloudflareEmbeddingParams(request *schemas.BifrostEmbeddingRequest) (*int, *string, map[string]interface{}, error) {
	var dims *int
	var encodingFmt *string
	var extraParams map[string]interface{}

	if request.Params != nil {
		if request.Params.Dimensions != nil {
			if *request.Params.Dimensions <= 0 {
				return nil, nil, nil, providerUtils.InvalidRequestErrorf("dimensions must be a positive integer, got %d", *request.Params.Dimensions)
			}
			d := *request.Params.Dimensions
			dims = &d
		}
		if request.Params.EncodingFormat != nil {
			normalized, err := validateCloudflareEncodingFormat(*request.Params.EncodingFormat)
			if err != nil {
				return nil, nil, nil, err
			}
			encodingFmt = &normalized
		}
		if len(request.Params.ExtraParams) > 0 {
			extraParams = maps.Clone(request.Params.ExtraParams)
		}
	}

	for _, item := range request.Input {
		if item.Params == nil {
			continue
		}
		if item.Params.Dimensions != nil {
			if *item.Params.Dimensions <= 0 {
				return nil, nil, nil, providerUtils.InvalidRequestErrorf("dimensions must be a positive integer, got %d", *item.Params.Dimensions)
			}
			if len(request.Input) == 1 && dims == nil {
				d := *item.Params.Dimensions
				dims = &d
			} else if dims == nil || *item.Params.Dimensions != *dims {
				return nil, nil, nil, providerUtils.InvalidRequestErrorf("cloudflare embeddings apply one dimensions value to the whole request; conflicting per-item dimensions are not supported")
			}
		}
		if item.Params.EncodingFormat != nil {
			normalized, err := validateCloudflareEncodingFormat(*item.Params.EncodingFormat)
			if err != nil {
				return nil, nil, nil, err
			}
			if len(request.Input) == 1 && encodingFmt == nil {
				encodingFmt = &normalized
			} else if encodingFmt == nil || normalized != *encodingFmt {
				return nil, nil, nil, providerUtils.InvalidRequestErrorf("cloudflare embeddings apply one encoding_format to the whole request; conflicting per-item encoding_format values are not supported")
			}
		}
	}

	return dims, encodingFmt, extraParams, nil
}

func validateCloudflareEncodingFormat(format string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(format))
	switch normalized {
	case schemas.EmbeddingEncodingFloat, schemas.EmbeddingEncodingBase64:
		return normalized, nil
	default:
		return "", providerUtils.InvalidRequestErrorf("cloudflare embeddings only support float or base64 encoding_format, got %q", format)
	}
}

func canUseCloudflareTextInput(items []schemas.EmbeddingInputItem) bool {
	if len(items) == 0 {
		return false
	}
	hasText := false
	hasTokens := false
	for _, item := range items {
		if len(item.Messages) > 0 || len(item.Content) != 1 {
			return false
		}
		part := item.Content[0]
		switch part.Type {
		case schemas.EmbeddingContentPartTypeText:
			if part.Text == nil {
				return false
			}
			hasText = true
		case schemas.EmbeddingContentPartTypeTokens:
			if len(part.Tokens) == 0 {
				return false
			}
			hasTokens = true
		default:
			return false
		}
	}
	return hasText != hasTokens
}

func buildCloudflareTextInput(request *schemas.BifrostEmbeddingRequest, isGemma2 bool) (*CloudflareEmbeddingTextInput, error) {
	firstType := request.Input[0].Content[0].Type
	if firstType == schemas.EmbeddingContentPartTypeText {
		texts := make([]string, len(request.Input))
		for i, item := range request.Input {
			text := *item.Content[0].Text
			if isGemma2 {
				text = applyEmbeddingGemmaTaskPrefix(text, item.EffectiveParams(request.Params))
			}
			texts[i] = text
		}
		if len(texts) == 1 {
			return &CloudflareEmbeddingTextInput{Text: &texts[0]}, nil
		}
		return &CloudflareEmbeddingTextInput{Texts: texts}, nil
	}

	tokenBatches := make([][]int, len(request.Input))
	for i, item := range request.Input {
		tokenBatches[i] = item.Content[0].Tokens
	}
	if len(tokenBatches) == 1 {
		return &CloudflareEmbeddingTextInput{Tokens: tokenBatches[0]}, nil
	}
	return &CloudflareEmbeddingTextInput{TokenBatches: tokenBatches}, nil
}

func buildCloudflareMessages(request *schemas.BifrostEmbeddingRequest, isGemma2 bool) ([][]CloudflareEmbeddingMessage, error) {
	conversations := make([][]CloudflareEmbeddingMessage, 0, len(request.Input))
	for _, item := range request.Input {
		effectiveParams := item.EffectiveParams(request.Params)
		if len(item.Messages) > 0 {
			conv := make([]CloudflareEmbeddingMessage, 0, len(item.Messages))
			for _, msg := range item.Messages {
				role := strings.TrimSpace(msg.Role)
				if role == "" {
					role = "user"
				}
				content, err := buildCloudflareMessageContent(msg.Content, effectiveParams, isGemma2)
				if err != nil {
					return nil, err
				}
				conv = append(conv, CloudflareEmbeddingMessage{Role: role, Content: content})
			}
			conversations = append(conversations, conv)
			continue
		}

		content, err := buildCloudflareMessageContent(item.Content, effectiveParams, isGemma2)
		if err != nil {
			return nil, err
		}
		conversations = append(conversations, []CloudflareEmbeddingMessage{{
			Role:    "user",
			Content: content,
		}})
	}
	return conversations, nil
}

func buildCloudflareMessageContent(content schemas.EmbeddingContent, params *schemas.EmbeddingParameters, isGemma2 bool) (CloudflareEmbeddingMessageContent, error) {
	if len(content) == 0 {
		return CloudflareEmbeddingMessageContent{}, providerUtils.InvalidRequestErrorf("embedding message content is empty")
	}
	if len(content) == 1 && content[0].Type == schemas.EmbeddingContentPartTypeText {
		if content[0].Text == nil || *content[0].Text == "" {
			return CloudflareEmbeddingMessageContent{}, providerUtils.InvalidRequestErrorf("text embedding part has no text")
		}
		text := *content[0].Text
		if isGemma2 {
			text = applyEmbeddingGemmaTaskPrefix(text, params)
		}
		return CloudflareEmbeddingMessageContent{Text: &text}, nil
	}

	parts := make([]CloudflareEmbeddingContentPart, 0, len(content))
	for _, part := range content {
		cfPart, err := toCloudflareEmbeddingContentPart(part, params, isGemma2)
		if err != nil {
			return CloudflareEmbeddingMessageContent{}, err
		}
		parts = append(parts, *cfPart)
	}
	return CloudflareEmbeddingMessageContent{Parts: parts}, nil
}

func toCloudflareEmbeddingContentPart(part schemas.EmbeddingContentPart, params *schemas.EmbeddingParameters, isGemma2 bool) (*CloudflareEmbeddingContentPart, error) {
	if err := part.Validate(); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}

	switch part.Type {
	case schemas.EmbeddingContentPartTypeText:
		text := *part.Text
		if isGemma2 {
			text = applyEmbeddingGemmaTaskPrefix(text, params)
		}
		return &CloudflareEmbeddingContentPart{Type: "text", Text: &text}, nil
	case schemas.EmbeddingContentPartTypeImage:
		uri, err := cloudflareMediaDataURI(part.Image, "image/jpeg")
		if err != nil {
			return nil, err
		}
		return &CloudflareEmbeddingContentPart{
			Type:     "image_url",
			ImageURL: &CloudflareMediaURL{URL: uri},
		}, nil
	case schemas.EmbeddingContentPartTypeVideo:
		uri, err := cloudflareMediaDataURI(part.Video, "video/mp4")
		if err != nil {
			return nil, err
		}
		return &CloudflareEmbeddingContentPart{
			Type:     "video_url",
			VideoURL: &CloudflareMediaURL{URL: uri},
		}, nil
	case schemas.EmbeddingContentPartTypeAudio:
		return cloudflareAudioPart(part.Audio)
	case schemas.EmbeddingContentPartTypeTokens:
		return nil, providerUtils.InvalidRequestErrorf("cloudflare does not support mixing token IDs with multimodal or message embedding inputs")
	default:
		return nil, providerUtils.InvalidRequestErrorf("cloudflare embeddings do not support %s input parts", part.Type)
	}
}

func cloudflareMediaDataURI(media *schemas.EmbeddingMediaPart, defaultMIME string) (string, error) {
	if err := media.Validate(); err != nil {
		return "", providerUtils.InvalidRequestErrorf("%s", err)
	}
	if media.URL != nil {
		return *media.URL, nil
	}
	data := *media.Data
	if strings.HasPrefix(data, "data:") {
		return data, nil
	}
	mime := defaultMIME
	if media.MIMEType != nil && strings.TrimSpace(*media.MIMEType) != "" {
		mime = strings.TrimSpace(*media.MIMEType)
	}
	return "data:" + mime + ";base64," + data, nil
}

func cloudflareAudioPart(media *schemas.EmbeddingMediaPart) (*CloudflareEmbeddingContentPart, error) {
	if err := media.Validate(); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}
	if media.URL != nil {
		return &CloudflareEmbeddingContentPart{
			Type:     "audio_url",
			AudioURL: &CloudflareMediaURL{URL: *media.URL},
		}, nil
	}

	rawData := *media.Data
	extractedMIME := ""
	if strings.HasPrefix(rawData, "data:") {
		if header, payload, ok := strings.Cut(rawData, ","); ok {
			rawData = payload
			meta := strings.TrimPrefix(header, "data:")
			extractedMIME, _, _ = strings.Cut(meta, ";")
		}
	}
	mime := extractedMIME
	if media.MIMEType != nil && strings.TrimSpace(*media.MIMEType) != "" {
		mime = strings.TrimSpace(*media.MIMEType)
	}
	return &CloudflareEmbeddingContentPart{
		Type: "input_audio",
		InputAudio: &CloudflareInputAudio{
			Data:   rawData,
			Format: mimeToAudioFormat(mime),
		},
	}, nil
}

func mimeToAudioFormat(mime string) string {
	base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(mime)), ";")
	base = strings.TrimSpace(base)
	switch base {
	case "", "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/ogg":
		return "ogg"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/webm":
		return "webm"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "mp4"
	default:
		if strings.HasPrefix(base, "audio/") {
			if f := strings.TrimPrefix(base, "audio/"); f != "" {
				return f
			}
		}
		return "wav"
	}
}

func applyEmbeddingGemmaTaskPrefix(text string, params *schemas.EmbeddingParameters) string {
	if params == nil {
		return text
	}
	hasTask := params.TaskType != nil && strings.TrimSpace(*params.TaskType) != ""
	hasTitle := params.Title != nil && strings.TrimSpace(*params.Title) != ""
	if !hasTask && !hasTitle {
		return text
	}

	lowerTrimmed := strings.ToLower(strings.TrimSpace(text))
	if strings.HasPrefix(lowerTrimmed, "task: ") || strings.HasPrefix(lowerTrimmed, "title: ") {
		return text
	}

	if hasTask {
		task := strings.TrimSpace(*params.TaskType)
		switch strings.ToLower(task) {
		case "retrieval_query", "search_query", "search result", "search_result", "query":
			return "task: search result | query: " + text
		case "retrieval_document", "search_document", "document":
			title := "none"
			if hasTitle {
				title = strings.TrimSpace(*params.Title)
			}
			return "title: " + title + " | text: " + text
		case "question_answering", "question answering":
			return "task: question answering | query: " + text
		case "fact_verification", "fact_checking", "fact checking":
			return "task: fact checking | query: " + text
		case "classification":
			return "task: classification | query: " + text
		case "clustering":
			return "task: clustering | query: " + text
		case "semantic_similarity", "sentence_similarity", "sentence similarity", "sts", "similarity":
			return "task: sentence similarity | query: " + text
		case "code_retrieval_query", "code_retrieval", "code retrieval":
			return "task: code retrieval | query: " + text
		default:
			return "task: " + task + " | query: " + text
		}
	}

	return "title: " + strings.TrimSpace(*params.Title) + " | text: " + text
}

func (provider *CloudflareProvider) nativeEmbedding(ctx *schemas.BifrostContext, key schemas.Key, accountURL string, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		return nil, providerUtils.NewBifrostBadRequestError("large payload passthrough is not supported for cloudflare multimodal embeddings")
	}

	modelPath, bifrostErr := nativeEmbeddingModelPath(request.Model)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*CloudflareEmbeddingRequest, error) {
			return ToCloudflareEmbeddingRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(accountURL + providerUtils.GetPathFromContext(ctx, modelPath))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	req.SetBody(jsonData)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseCloudflareError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var envelope CloudflareEmbeddingResponseEnvelope
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &envelope, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if envelope.Success != nil && !*envelope.Success {
		return nil, providerUtils.EnrichError(ctx, parseCloudflareError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp := envelope.Result
	if bifrostResp == nil && len(envelope.Data) > 0 {
		bifrostResp = &schemas.BifrostEmbeddingResponse{
			Data:   envelope.Data,
			Model:  envelope.Model,
			Object: envelope.Object,
			Usage:  envelope.Usage,
		}
	}
	if bifrostResp == nil {
		return nil, providerUtils.EnrichError(ctx, parseCloudflareError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	if bifrostResp.Object == "" {
		bifrostResp.Object = "list"
	}
	bifrostResp.BackfillParams(request)
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}
