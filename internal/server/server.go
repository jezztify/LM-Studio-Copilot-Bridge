package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"lmstudio-copilot-bridge/internal/config"
	"lmstudio-copilot-bridge/internal/lmstudio"
)

type ctxKey string

const requestMetaKey ctxKey = "request-meta"

type requestMeta struct {
	Route            string
	RequestID        string
	Model            string
	Streaming        bool
	UpstreamEndpoint string
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	return r.ResponseWriter.Write(data)
}

type Handler struct {
	logger  *slog.Logger
	client  *lmstudio.Client
	version string
	mux     *http.ServeMux
	seq     atomic.Uint64
}

func NewHandler(cfg config.Config, logger *slog.Logger, version string) http.Handler {
	return NewHandlerWithClient(cfg, logger, version, lmstudio.NewClient(cfg.UpstreamBaseURL, nil))
}

func NewHandlerWithClient(cfg config.Config, logger *slog.Logger, version string, client *lmstudio.Client) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	if version == "" {
		version = "dev"
	}

	h := &Handler{
		logger:  logger,
		client:  client,
		version: version,
		mux:     http.NewServeMux(),
	}
	h.routes()
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	meta := &requestMeta{
		Route:     r.URL.Path,
		RequestID: h.requestID(),
	}
	ctx := context.WithValue(r.Context(), requestMetaKey, meta)
	recorder := &statusRecorder{ResponseWriter: w}

	h.mux.ServeHTTP(recorder, r.WithContext(ctx))

	statusCode := recorder.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	h.logger.Info("request complete",
		"route", meta.Route,
		"model", meta.Model,
		"stream", meta.Streaming,
		"upstream_endpoint", meta.UpstreamEndpoint,
		"status_code", statusCode,
		"latency", time.Since(start).String(),
		"request_id", meta.RequestID,
	)
}

func (h *Handler) routes() {
	h.mux.HandleFunc("POST /api/generate", h.handleGenerate)
	h.mux.HandleFunc("POST /api/chat", h.handleChat)
	h.mux.HandleFunc("POST /api/embed", h.handleEmbed)
	h.mux.HandleFunc("POST /api/embeddings", h.handleLegacyEmbeddings)
	h.mux.HandleFunc("POST /api/show", h.handleShow)
	h.mux.HandleFunc("GET /api/tags", h.handleTags)
	h.mux.HandleFunc("GET /v1/models", h.handleOpenAIModels)
	h.mux.HandleFunc("POST /v1/chat/completions", h.handleOpenAIChatCompletions)
	h.mux.HandleFunc("GET /api/version", h.handleVersion)
	h.mux.HandleFunc("GET /healthz", h.handleHealth)
}

func (h *Handler) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	meta := requestMetaFromContext(r.Context())
	meta.UpstreamEndpoint = "/models"

	resp, err := h.client.Get(r.Context(), "/models")
	if err != nil {
		writeUpstreamUnavailable(w, err)
		return
	}
	defer resp.Body.Close()

	if err := proxyResponse(w, resp); err != nil {
		h.logger.Error("proxy openai models failed", "request_id", meta.RequestID, "route", meta.Route, "error", err)
	}
}

func (h *Handler) handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, req, err := decodeOpenAIChatCompletionsRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	meta := requestMetaFromContext(r.Context())
	meta.Model = req.Model
	meta.Streaming = requestStreamValue(req.Stream)
	meta.UpstreamEndpoint = "/chat/completions"

	h.logger.Debug("upstream request", "request_id", meta.RequestID, "body", string(body))

	accept := strings.TrimSpace(r.Header.Get("Accept"))
	if accept == "" {
		accept = "application/json"
	}

	resp, err := h.client.Post(r.Context(), "/chat/completions", strings.NewReader(string(body)), r.Header.Get("Content-Type"), accept)
	if err != nil {
		writeUpstreamUnavailable(w, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		proxyUpstreamError(w, resp)
		return
	}

	if requestStreamValue(req.Stream) {
		if err := streamFilteredOpenAIResponse(w, resp, h.logger, meta.RequestID); err != nil {
			h.logger.Error("proxy openai chat completions stream failed", "request_id", meta.RequestID, "route", meta.Route, "error", err)
		}
		return
	}

	// Non-streaming: read and log the full response before forwarding.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("read upstream response: %v", err))
		return
	}
	h.logger.Debug("upstream response", "request_id", meta.RequestID, "body", string(respBody))
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(respBody); err != nil {
		h.logger.Error("write response failed", "request_id", meta.RequestID, "error", err)
	}
}

func (h *Handler) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := decodeJSONRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	meta := requestMetaFromContext(r.Context())
	meta.Model = req.Model
	meta.Streaming = requestStreamValue(req.Stream)
	meta.UpstreamEndpoint = "/completions"

	logger := h.logger.With("request_id", meta.RequestID, "route", meta.Route)
	if err := validateGenerateRequest(logger, req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp, err := h.client.PostJSON(r.Context(), "/completions", translateGenerateRequest(req))
	if err != nil {
		writeUpstreamUnavailable(w, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		proxyUpstreamError(w, resp)
		return
	}
	if requestStreamValue(req.Stream) {
		if err := streamGenerateResponse(w, resp.Body); err != nil {
			logger.Error("stream generate failed", "error", err)
		}
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("read upstream response: %v", err))
		return
	}
	var upstream openAICompletionResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("decode upstream response: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, normalizeGenerateResponse(upstream))
}

func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := decodeJSONRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	meta := requestMetaFromContext(r.Context())
	meta.Model = req.Model
	meta.Streaming = requestStreamValue(req.Stream)
	meta.UpstreamEndpoint = "/chat/completions"

	logger := h.logger.With("request_id", meta.RequestID, "route", meta.Route)
	if err := validateChatRequest(logger, req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp, err := h.client.PostJSON(r.Context(), "/chat/completions", translateChatRequest(req))
	if err != nil {
		writeUpstreamUnavailable(w, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		proxyUpstreamError(w, resp)
		return
	}
	if requestStreamValue(req.Stream) {
		if err := streamChatResponse(w, resp.Body); err != nil {
			logger.Error("stream chat failed", "error", err)
		}
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("read upstream response: %v", err))
		return
	}
	var upstream openAIChatResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("decode upstream response: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, normalizeChatResponse(upstream))
}

func (h *Handler) handleEmbed(w http.ResponseWriter, r *http.Request) {
	h.handleEmbeddingsRoute(w, r, false)
}

func (h *Handler) handleLegacyEmbeddings(w http.ResponseWriter, r *http.Request) {
	h.handleEmbeddingsRoute(w, r, true)
}

func (h *Handler) handleEmbeddingsRoute(w http.ResponseWriter, r *http.Request, legacy bool) {
	var req embedRequest
	if err := decodeJSONRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	meta := requestMetaFromContext(r.Context())
	meta.Model = req.Model
	meta.UpstreamEndpoint = "/embeddings"

	logger := h.logger.With("request_id", meta.RequestID, "route", meta.Route)
	if err := validateEmbedRequest(logger, req, legacy); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	upstreamPayload, err := translateEmbedRequest(req, legacy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp, err := h.client.PostJSON(r.Context(), "/embeddings", upstreamPayload)
	if err != nil {
		writeUpstreamUnavailable(w, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		proxyUpstreamError(w, resp)
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("read upstream response: %v", err))
		return
	}
	var upstream openAIEmbeddingResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("decode upstream response: %v", err))
		return
	}
	if legacy {
		writeJSON(w, http.StatusOK, normalizeLegacyEmbeddingsResponse(upstream))
		return
	}
	writeJSON(w, http.StatusOK, normalizeEmbedResponse(upstream))
}

func (h *Handler) handleTags(w http.ResponseWriter, r *http.Request) {
	meta := requestMetaFromContext(r.Context())
	logger := h.logger.With("request_id", meta.RequestID, "route", meta.Route)

	models, resolution, upstreamResp, err := h.resolveModelMetadata(r.Context(), logger)
	if upstreamResp != nil {
		defer upstreamResp.Body.Close()
		meta.UpstreamEndpoint = "/models"
		proxyUpstreamError(w, upstreamResp)
		return
	}
	if err != nil {
		writeUpstreamUnavailable(w, err)
		return
	}
	if resolution.Source == metadataSourceREST {
		meta.UpstreamEndpoint = "/api/v1/models"
	} else {
		meta.UpstreamEndpoint = "/models"
	}
	writeJSON(w, http.StatusOK, normalizeTagsResponse(models))
}

func (h *Handler) handleShow(w http.ResponseWriter, r *http.Request) {
	var req showRequest
	if err := decodeJSONRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	effectiveModel, err := resolveShowModel(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	meta := requestMetaFromContext(r.Context())
	meta.Model = effectiveModel

	logger := h.logger.With("request_id", meta.RequestID, "route", meta.Route, "model", effectiveModel)
	if err := validateShowRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Verbose != nil && *req.Verbose {
		logger.Debug("verbose requested for show route")
	}

	models, resolution, upstreamResp, err := h.resolveModelMetadata(r.Context(), logger)
	if upstreamResp != nil {
		defer upstreamResp.Body.Close()
		meta.UpstreamEndpoint = "/models"
		proxyUpstreamError(w, upstreamResp)
		return
	}
	if err != nil {
		writeUpstreamUnavailable(w, err)
		return
	}
	if resolution.Source == metadataSourceREST {
		meta.UpstreamEndpoint = "/api/v1/models"
	} else {
		meta.UpstreamEndpoint = "/models"
	}

	if model, ok := findModelMetadata(models, effectiveModel); ok {
		writeJSON(w, http.StatusOK, normalizeShowResponse(model))
		return
	}

	writeError(w, http.StatusNotFound, fmt.Sprintf("model not found: %s", effectiveModel))
}

func (h *Handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "lmstudio-ollama-bridge",
		"version": h.version,
	})
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	meta := requestMetaFromContext(r.Context())
	meta.UpstreamEndpoint = "/models"

	resp, err := h.client.Get(r.Context(), "/models")
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "degraded",
			"upstream": "error",
			"version":  h.version,
			"error":    err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":          "degraded",
			"upstream":        "error",
			"version":         h.version,
			"upstream_status": resp.StatusCode,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"upstream": "ok",
		"version":  h.version,
	})
}

func decodeJSONRequest(r *http.Request, target any) error {
	contentType := r.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(contentType, "application/json") {
		return errors.New("content type must be application/json")
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON payload: %w", err)
	}
	if decoder.More() {
		return errors.New("invalid JSON payload: trailing data")
	}
	return nil
}

func decodeOpenAIChatCompletionsRequest(r *http.Request) ([]byte, openAIChatCompletionsRequest, error) {
	contentType := r.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(contentType, "application/json") {
		return nil, openAIChatCompletionsRequest{}, errors.New("content type must be application/json")
	}
	defer r.Body.Close()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, openAIChatCompletionsRequest{}, fmt.Errorf("read request body: %w", err)
	}

	var req openAIChatCompletionsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, openAIChatCompletionsRequest{}, fmt.Errorf("invalid JSON payload: %w", err)
	}

	return body, req, nil
}

func proxyUpstreamError(w http.ResponseWriter, resp *http.Response) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("read upstream error: %v", err))
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		writeError(w, resp.StatusCode, fmt.Sprintf("upstream returned status %d", resp.StatusCode))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func proxyResponse(w http.ResponseWriter, resp *http.Response) error {
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		readBytes, err := resp.Body.Read(buffer)
		if readBytes > 0 {
			if _, writeErr := w.Write(buffer[:readBytes]); writeErr != nil {
				return fmt.Errorf("write upstream response: %w", writeErr)
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read upstream response: %w", err)
		}
	}
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func writeUpstreamUnavailable(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadGateway, fmt.Sprintf("upstream unavailable: %v", err))
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]any{
		"error": map[string]any{
			"message": message,
		},
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
}

func requestMetaFromContext(ctx context.Context) *requestMeta {
	meta, _ := ctx.Value(requestMetaKey).(*requestMeta)
	if meta == nil {
		return &requestMeta{}
	}
	return meta
}

func (h *Handler) requestID() string {
	return fmt.Sprintf("req-%06d", h.seq.Add(1))
}
