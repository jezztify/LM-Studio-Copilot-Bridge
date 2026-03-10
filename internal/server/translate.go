package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func translateGenerateRequest(req generateRequest) map[string]any {
	payload := map[string]any{
		"model":  req.Model,
		"prompt": req.Prompt,
		"stream": requestStreamValue(req.Stream),
	}
	applySamplingSettings(payload, req.Temperature, req.TopP, req.Stop, req.Seed, req.MaxTokens, req.Options)
	return payload
}

func translateChatRequest(req chatRequest) map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, message := range req.Messages {
		messages = append(messages, map[string]any{
			"role":    message.Role,
			"content": message.Content,
		})
	}

	payload := map[string]any{
		"model":    req.Model,
		"messages": messages,
		"stream":   requestStreamValue(req.Stream),
	}
	applySamplingSettings(payload, req.Temperature, req.TopP, req.Stop, req.Seed, req.MaxTokens, req.Options)
	return payload
}

func translateEmbedRequest(req embedRequest, legacy bool) (map[string]any, error) {
	input := req.Input
	if legacy && !unsupportedFieldPresent(req.Prompt) {
		input = req.Prompt
	}
	if !legacy && !unsupportedFieldPresent(input) {
		input = req.Prompt
	}
	if !unsupportedFieldPresent(input) {
		return nil, fmt.Errorf("input is required")
	}

	var normalized any
	if err := json.Unmarshal(input, &normalized); err != nil {
		return nil, fmt.Errorf("decode embedding input: %w", err)
	}

	return map[string]any{
		"model": req.Model,
		"input": normalized,
	}, nil
}

func normalizeGenerateResponse(resp openAICompletionResponse) map[string]any {
	choice := firstCompletionChoice(resp.Choices)
	body := map[string]any{
		"model":      resp.Model,
		"created_at": formatCreatedAt(resp.Created),
		"response":   choice.Text,
		"done":       true,
	}
	if choice.FinishReason != "" {
		body["done_reason"] = choice.FinishReason
	}
	return body
}

func normalizeChatResponse(resp openAIChatResponse) map[string]any {
	choice := firstChatChoice(resp.Choices)
	body := map[string]any{
		"model":      resp.Model,
		"created_at": formatCreatedAt(resp.Created),
		"message": map[string]any{
			"role":    choice.Message.Role,
			"content": choice.Message.Content,
		},
		"done": true,
	}
	if choice.FinishReason != "" {
		body["done_reason"] = choice.FinishReason
	}
	return body
}

func normalizeEmbedResponse(resp openAIEmbeddingResponse) map[string]any {
	embeddings := make([][]float64, 0, len(resp.Data))
	for _, item := range resp.Data {
		embeddings = append(embeddings, item.Embedding)
	}
	return map[string]any{
		"model":      resp.Model,
		"embeddings": embeddings,
	}
}

func normalizeLegacyEmbeddingsResponse(resp openAIEmbeddingResponse) map[string]any {
	if len(resp.Data) == 0 {
		return map[string]any{"embedding": []float64{}}
	}
	return map[string]any{"embedding": resp.Data[0].Embedding}
}

func normalizeTagsResponse(modelsMetadata []modelMetadata) map[string]any {
	models := make([]map[string]any, 0, len(modelsMetadata))
	for _, model := range modelsMetadata {
		models = append(models, map[string]any{
			"name":        model.CanonicalID,
			"model":       model.CanonicalID,
			"modified_at": "",
			"size":        model.SizeBytes,
			"digest":      "",
			"details":     modelDetails(model),
		})
	}
	return map[string]any{"models": models}
}

func normalizeShowResponse(model modelMetadata) map[string]any {
	return map[string]any{
		"model":        model.CanonicalID,
		"license":      "",
		"modelfile":    "",
		"parameters":   model.ParameterSize,
		"template":     "",
		"system":       "",
		"modified_at":  "",
		"capabilities": showCapabilities(model),
		"messages":     []any{},
		"details":      modelDetails(model),
		"model_info":   modelInfo(model),
	}
}

func showCapabilities(model modelMetadata) []string {
	capabilities := make([]string, 0, 3)
	if supportsCompletionCapability(model) {
		capabilities = append(capabilities, "completion")
	}
	if supportsToolsCapability(model) {
		capabilities = append(capabilities, "tools")
	}
	if supportsEmbeddingCapability(model) {
		capabilities = append(capabilities, "embedding")
	}
	return capabilities
}

func supportsCompletionCapability(model modelMetadata) bool {
	modelType := normalizedModelType(model.Type)
	if modelType == "" {
		return model.MetadataSource == metadataSourceOpenAI
	}
	return strings.Contains(modelType, "llm") ||
		strings.Contains(modelType, "text") ||
		strings.Contains(modelType, "chat") ||
		strings.Contains(modelType, "completion") ||
		strings.Contains(modelType, "instruct") ||
		strings.Contains(modelType, "generate") ||
		strings.Contains(modelType, "vlm")
}

func supportsEmbeddingCapability(model modelMetadata) bool {
	modelType := normalizedModelType(model.Type)
	return strings.Contains(modelType, "embedding") || strings.Contains(modelType, "embed")
}

func supportsToolsCapability(model modelMetadata) bool {
	if !supportsCompletionCapability(model) {
		return false
	}
	return model.TrainedForToolUse != nil && *model.TrainedForToolUse
}

func normalizedModelType(modelType string) string {
	return strings.ToLower(strings.TrimSpace(modelType))
}

func modelDetails(model modelMetadata) map[string]any {
	details := sparseModelDetails()
	if model.Format != "" {
		details["format"] = model.Format
	}
	if model.Architecture != "" {
		details["family"] = model.Architecture
	}
	if model.ParameterSize != "" {
		details["parameter_size"] = model.ParameterSize
	}
	if model.QuantizationLevel != "" {
		details["quantization_level"] = model.QuantizationLevel
	}
	return details
}

func modelInfo(model modelMetadata) map[string]any {
	info := map[string]any{}
	if model.DisplayName != "" {
		info["display_name"] = model.DisplayName
	}
	if model.Architecture != "" {
		info["architecture"] = model.Architecture
	}
	if model.Format != "" {
		info["format"] = model.Format
	}
	if model.QuantizationLevel != "" {
		quantization := map[string]any{"name": model.QuantizationLevel}
		if model.QuantizationBits > 0 {
			quantization["bits_per_weight"] = model.QuantizationBits
		}
		info["quantization"] = quantization
	}
	if model.MaxContextLength > 0 {
		info["max_context_length"] = model.MaxContextLength
	}
	if model.LoadedContextLength > 0 {
		info["loaded_context_length"] = model.LoadedContextLength
	}
	if model.SizeBytes > 0 {
		info["size_bytes"] = model.SizeBytes
	}
	if model.Type != "" {
		info["type"] = model.Type
	}
	if model.Publisher != "" {
		info["publisher"] = model.Publisher
	}
	if model.Description != "" {
		info["description"] = model.Description
	}
	capabilities := map[string]any{}
	if model.Vision != nil {
		capabilities["vision"] = *model.Vision
	}
	if model.TrainedForToolUse != nil {
		capabilities["trained_for_tool_use"] = *model.TrainedForToolUse
	}
	if len(capabilities) > 0 {
		info["capabilities"] = capabilities
	}
	return info
}

func sparseModelDetails() map[string]any {
	return map[string]any{
		"parent_model":       "",
		"format":             "",
		"family":             "",
		"families":           nil,
		"parameter_size":     "",
		"quantization_level": "",
	}
}

func requestStreamValue(stream *bool) bool {
	if stream == nil {
		return false
	}
	return *stream
}

func applySamplingSettings(payload map[string]any, temperature, topP *float64, stop stopSequences, seed *int64, maxTokens *int, opts options) {
	if value := firstFloat(temperature, opts.Temperature); value != nil {
		payload["temperature"] = *value
	}
	if value := firstFloat(topP, opts.TopP); value != nil {
		payload["top_p"] = *value
	}
	if value := firstStop(stop, opts.Stop); len(value) > 0 {
		payload["stop"] = value
	}
	if value := firstInt64(seed, opts.Seed); value != nil {
		payload["seed"] = *value
	}
	if value := firstInt(maxTokens, opts.MaxTokens, opts.NumPredict); value != nil {
		payload["max_tokens"] = *value
	}
}

func firstFloat(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstInt(values ...*int) *int {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstInt64(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstStop(values ...stopSequences) []string {
	for _, value := range values {
		if value.Set {
			return value.Values
		}
	}
	return nil
}

func formatCreatedAt(created int64) string {
	if created <= 0 {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return time.Unix(created, 0).UTC().Format(time.RFC3339Nano)
}

func firstCompletionChoice(choices []struct {
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason"`
}) struct {
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason"`
} {
	if len(choices) == 0 {
		return struct {
			Text         string `json:"text"`
			FinishReason string `json:"finish_reason"`
		}{}
	}
	return choices[0]
}

func firstChatChoice(choices []struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}) struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
} {
	if len(choices) == 0 {
		return struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{}
	}
	return choices[0]
}