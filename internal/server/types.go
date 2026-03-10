package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

type stopSequences struct {
	Values []string
	Set    bool
}

func (s *stopSequences) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	s.Set = true
	if trimmed == "null" {
		s.Values = nil
		return nil
	}

	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		s.Values = []string{single}
		return nil
	}

	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		s.Values = many
		return nil
	}

	return fmt.Errorf("stop must be a string or array of strings")
}

type options struct {
	Temperature *float64      `json:"temperature"`
	TopP        *float64      `json:"top_p"`
	Stop        stopSequences `json:"stop"`
	Seed        *int64        `json:"seed"`
	NumPredict  *int          `json:"num_predict"`
	MaxTokens   *int          `json:"max_tokens"`
}

type generateRequest struct {
	Model       string          `json:"model"`
	Prompt      string          `json:"prompt"`
	Stream      *bool           `json:"stream"`
	Temperature *float64        `json:"temperature"`
	TopP        *float64        `json:"top_p"`
	Stop        stopSequences   `json:"stop"`
	Seed        *int64          `json:"seed"`
	MaxTokens   *int            `json:"max_tokens"`
	Options     options         `json:"options"`
	Raw         json.RawMessage `json:"raw"`
	Template    json.RawMessage `json:"template"`
	Context     json.RawMessage `json:"context"`
	Images      json.RawMessage `json:"images"`
	Tools       json.RawMessage `json:"tools"`
	Think       json.RawMessage `json:"think"`
	KeepAlive   json.RawMessage `json:"keep_alive"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content string          `json:"content"`
	Images  json.RawMessage `json:"images"`
}

type chatRequest struct {
	Model       string          `json:"model"`
	Messages    []chatMessage   `json:"messages"`
	Stream      *bool           `json:"stream"`
	Temperature *float64        `json:"temperature"`
	TopP        *float64        `json:"top_p"`
	Stop        stopSequences   `json:"stop"`
	Seed        *int64          `json:"seed"`
	MaxTokens   *int            `json:"max_tokens"`
	Options     options         `json:"options"`
	Raw         json.RawMessage `json:"raw"`
	Template    json.RawMessage `json:"template"`
	Context     json.RawMessage `json:"context"`
	Images      json.RawMessage `json:"images"`
	Tools       json.RawMessage `json:"tools"`
	Think       json.RawMessage `json:"think"`
	KeepAlive   json.RawMessage `json:"keep_alive"`
}

type openAIChatCompletionsRequest struct {
	Model  string `json:"model"`
	Stream *bool  `json:"stream"`
}

type embedRequest struct {
	Model     string          `json:"model"`
	Input     json.RawMessage `json:"input"`
	Prompt    json.RawMessage `json:"prompt"`
	KeepAlive json.RawMessage `json:"keep_alive"`
	Images    json.RawMessage `json:"images"`
	Think     json.RawMessage `json:"think"`
}

type showRequest struct {
	Model    string          `json:"model"`
	Name     string          `json:"name"`
	Verbose  *bool           `json:"verbose"`
	System   json.RawMessage `json:"system"`
	Template json.RawMessage `json:"template"`
	Options  json.RawMessage `json:"options"`
}

type openAICompletionResponse struct {
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type openAIChatResponse struct {
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type openAIEmbeddingResponse struct {
	Model string `json:"model"`
	Data  []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

type openAICompletionStreamChunk struct {
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Text         string  `json:"text"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

type openAIChatStreamChunk struct {
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}
