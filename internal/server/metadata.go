package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const (
	metadataSourceREST   = "rest"
	metadataSourceOpenAI = "openai"
)

type modelMetadata struct {
	CanonicalID        string
	AlternateIDs       []string
	MetadataSource     string
	DisplayName        string
	Architecture       string
	Format             string
	ParameterSize      string
	QuantizationLevel  string
	QuantizationBits   int
	SizeBytes          int64
	MaxContextLength   int
	LoadedContextLength int
	Type               string
	Publisher          string
	Description        string
	Vision             *bool
	TrainedForToolUse  *bool
}

type metadataResolution struct {
	Source       string
	FallbackUsed bool
	Reason       string
}

type restModelsResponse struct {
	Models []restModel `json:"models"`
}

type restModel struct {
	Type            string `json:"type"`
	Publisher       string `json:"publisher"`
	Key             string `json:"key"`
	DisplayName     string `json:"display_name"`
	Architecture    string `json:"architecture"`
	Quantization    *struct {
		Name          string `json:"name"`
		BitsPerWeight int    `json:"bits_per_weight"`
	} `json:"quantization"`
	SizeBytes       int64 `json:"size_bytes"`
	ParamsString    string `json:"params_string"`
	LoadedInstances []struct {
		ID     string `json:"id"`
		Config struct {
			ContextLength int `json:"context_length"`
		} `json:"config"`
	} `json:"loaded_instances"`
	MaxContextLength int `json:"max_context_length"`
	Format           string `json:"format"`
	Capabilities     *struct {
		Vision            bool `json:"vision"`
		TrainedForToolUse bool `json:"trained_for_tool_use"`
	} `json:"capabilities"`
	Description *string `json:"description"`
}

func (m modelMetadata) matches(requested string) bool {
	if m.CanonicalID == requested {
		return true
	}
	for _, candidate := range m.AlternateIDs {
		if candidate == requested {
			return true
		}
	}
	return false
}

func (h *Handler) resolveModelMetadata(ctx context.Context, logger *slog.Logger) ([]modelMetadata, metadataResolution, *http.Response, error) {
	models, err := h.fetchRESTModelMetadata(ctx)
	if err == nil {
		resolution := metadataResolution{Source: metadataSourceREST}
		logMetadataResolution(logger, resolution, len(models))
		return models, resolution, nil, nil
	}

	fallbackModels, upstreamResp, fallbackErr := h.fetchOpenAIModelMetadata(ctx)
	if upstreamResp != nil || fallbackErr != nil {
		return nil, metadataResolution{}, upstreamResp, fmt.Errorf("resolve metadata fallback: %w", err)
	}

	resolution := metadataResolution{
		Source:       metadataSourceOpenAI,
		FallbackUsed: true,
		Reason:       err.Error(),
	}
	logMetadataResolution(logger, resolution, len(fallbackModels))
	return fallbackModels, resolution, nil, nil
}

func (h *Handler) fetchRESTModelMetadata(ctx context.Context) ([]modelMetadata, error) {
	resp, err := h.client.Get(ctx, "/api/v1/models")
	if err != nil {
		return nil, fmt.Errorf("request rich metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("request rich metadata: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read rich metadata: %w", err)
	}

	var upstream restModelsResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode rich metadata: %w", err)
	}

	models := normalizeRESTModelMetadata(upstream.Models)
	if len(upstream.Models) > 0 && len(models) == 0 {
		return nil, fmt.Errorf("decode rich metadata: no usable models")
	}
	return models, nil
}

func (h *Handler) fetchOpenAIModelMetadata(ctx context.Context) ([]modelMetadata, *http.Response, error) {
	resp, err := h.client.Get(ctx, "/models")
	if err != nil {
		return nil, nil, fmt.Errorf("request fallback metadata: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, resp, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read fallback metadata: %w", err)
	}

	var upstream openAIModelsResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, nil, fmt.Errorf("decode fallback metadata: %w", err)
	}

	models := make([]modelMetadata, 0, len(upstream.Data))
	for _, model := range upstream.Data {
		identifier := strings.TrimSpace(model.ID)
		if identifier == "" {
			continue
		}
		models = append(models, modelMetadata{
			CanonicalID:    identifier,
			MetadataSource: metadataSourceOpenAI,
		})
	}
	if len(upstream.Data) > 0 && len(models) == 0 {
		return nil, nil, fmt.Errorf("decode fallback metadata: no usable models")
	}
	return models, nil, nil
}

func normalizeRESTModelMetadata(models []restModel) []modelMetadata {
	normalized := make([]modelMetadata, 0, len(models))
	for _, model := range models {
		metadata, ok := normalizeRESTModel(model)
		if !ok {
			continue
		}
		normalized = append(normalized, metadata)
	}
	return normalized
}

func normalizeRESTModel(model restModel) (modelMetadata, bool) {
	key := strings.TrimSpace(model.Key)
	if key == "" {
		return modelMetadata{}, false
	}

	uniqueLoadedIDs := make([]string, 0, len(model.LoadedInstances))
	seen := map[string]struct{}{}
	loadedContextLength := 0
	for _, instance := range model.LoadedInstances {
		id := strings.TrimSpace(instance.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueLoadedIDs = append(uniqueLoadedIDs, id)
		if loadedContextLength == 0 && instance.Config.ContextLength > 0 {
			loadedContextLength = instance.Config.ContextLength
		}
	}

	canonicalID := key
	if len(uniqueLoadedIDs) == 1 && uniqueLoadedIDs[0] != key {
		canonicalID = uniqueLoadedIDs[0]
	}

	alternateIDs := make([]string, 0, len(uniqueLoadedIDs)+1)
	if canonicalID != key {
		alternateIDs = append(alternateIDs, key)
	}
	for _, id := range uniqueLoadedIDs {
		if id != canonicalID {
			alternateIDs = append(alternateIDs, id)
		}
	}

	metadata := modelMetadata{
		CanonicalID:         canonicalID,
		AlternateIDs:        alternateIDs,
		MetadataSource:      metadataSourceREST,
		DisplayName:         strings.TrimSpace(model.DisplayName),
		Architecture:        strings.TrimSpace(model.Architecture),
		Format:              strings.TrimSpace(model.Format),
		ParameterSize:       strings.TrimSpace(model.ParamsString),
		SizeBytes:           model.SizeBytes,
		MaxContextLength:    model.MaxContextLength,
		LoadedContextLength: loadedContextLength,
		Type:                strings.TrimSpace(model.Type),
		Publisher:           strings.TrimSpace(model.Publisher),
		Description:         strings.TrimSpace(derefString(model.Description)),
	}
	if model.Quantization != nil {
		metadata.QuantizationLevel = strings.TrimSpace(model.Quantization.Name)
		metadata.QuantizationBits = model.Quantization.BitsPerWeight
	}
	if model.Capabilities != nil {
		vision := model.Capabilities.Vision
		trainedForToolUse := model.Capabilities.TrainedForToolUse
		metadata.Vision = &vision
		metadata.TrainedForToolUse = &trainedForToolUse
	}
	return metadata, true
}

func findModelMetadata(models []modelMetadata, requested string) (modelMetadata, bool) {
	for _, model := range models {
		if model.matches(requested) {
			return model, true
		}
	}
	return modelMetadata{}, false
}

func logMetadataResolution(logger *slog.Logger, resolution metadataResolution, modelCount int) {
	if logger == nil {
		return
	}
	attrs := []any{
		"metadata_source", resolution.Source,
		"fallback_used", resolution.FallbackUsed,
		"model_count", modelCount,
	}
	if resolution.Reason != "" {
		attrs = append(attrs, "fallback_reason", resolution.Reason)
	}
	logger.Debug("resolved model metadata", attrs...)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}