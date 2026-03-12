package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lmstudio-copilot-bridge/internal/config"
)

func TestGenerateNonStreaming(t *testing.T) {
	t.Parallel()

	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-1","created":1710000000,"model":"test-model","choices":[{"text":"world","finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")

	req := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(`{
		"model":"test-model",
		"prompt":"hello ",
		"stream":false,
		"options":{"temperature":0.2,"top_p":0.8,"num_predict":16,"seed":7,"stop":["END"]}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := upstreamBody["model"]; got != "test-model" {
		t.Fatalf("model = %v", got)
	}
	if got := upstreamBody["prompt"]; got != "hello " {
		t.Fatalf("prompt = %v", got)
	}
	if got := upstreamBody["max_tokens"]; got != float64(16) {
		t.Fatalf("max_tokens = %v", got)
	}
	if got := upstreamBody["seed"]; got != float64(7) {
		t.Fatalf("seed = %v", got)
	}

	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["model"] != "test-model" {
		t.Fatalf("response model = %v", response["model"])
	}
	if response["response"] != "world" {
		t.Fatalf("response text = %v", response["response"])
	}
	if response["done"] != true {
		t.Fatalf("done = %v", response["done"])
	}
	if response["done_reason"] != "stop" {
		t.Fatalf("done_reason = %v", response["done_reason"])
	}
	if _, ok := response["created_at"].(string); !ok {
		t.Fatalf("created_at missing or invalid: %v", response["created_at"])
	}
	if _, ok := response["context"]; ok {
		t.Fatalf("unexpected context field")
	}
}

func TestChatStreaming(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chat-1\",\"created\":1710000001,\"model\":\"chat-model\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chat-1\",\"created\":1710000001,\"model\":\"chat-model\",\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chat-1\",\"created\":1710000001,\"model\":\"chat-model\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{
		"model":"chat-model",
		"stream":true,
		"messages":[{"role":"user","content":"Hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	lines := collectNDJSON(t, rr.Body.Bytes())
	if len(lines) != 3 {
		t.Fatalf("chunks = %d, body = %s", len(lines), rr.Body.String())
	}

	var first map[string]any
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("decode first chunk: %v", err)
	}
	message, ok := first["message"].(map[string]any)
	if !ok || message["content"] != "Hel" {
		t.Fatalf("first chunk message = %v", first["message"])
	}
	if first["done"] != false {
		t.Fatalf("first done = %v", first["done"])
	}

	var final map[string]any
	if err := json.Unmarshal(lines[2], &final); err != nil {
		t.Fatalf("decode final chunk: %v", err)
	}
	if final["done"] != true {
		t.Fatalf("final done = %v", final["done"])
	}
	if final["done_reason"] != "stop" {
		t.Fatalf("final done_reason = %v", final["done_reason"])
	}
	finalMessage, ok := final["message"].(map[string]any)
	if !ok || finalMessage["content"] != "" {
		t.Fatalf("final message = %v", final["message"])
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/x-ndjson") {
		t.Fatalf("unexpected content type: %s", ct)
	}
	if cacheControl := rr.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("unexpected cache-control: %s", cacheControl)
	}
	if conn := rr.Header().Get("Connection"); conn != "keep-alive" {
		t.Fatalf("unexpected connection header: %s", conn)
	}
}

func TestGenerateStreaming(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"cmpl-1\",\"created\":1710000002,\"model\":\"gen-model\",\"choices\":[{\"text\":\"foo\",\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"cmpl-1\",\"created\":1710000002,\"model\":\"gen-model\",\"choices\":[{\"text\":\"bar\",\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"cmpl-1\",\"created\":1710000002,\"model\":\"gen-model\",\"choices\":[{\"text\":\"\",\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
	req := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(`{
		"model":"gen-model",
		"prompt":"hi",
		"stream":true
	}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	lines := collectNDJSON(t, rr.Body.Bytes())
	if len(lines) != 3 {
		t.Fatalf("chunks = %d, body = %s", len(lines), rr.Body.String())
	}

	var first map[string]any
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("decode first chunk: %v", err)
	}
	if first["response"] != "foo" {
		t.Fatalf("first response = %v", first["response"])
	}
	if first["done"] != false {
		t.Fatalf("first done = %v", first["done"])
	}

	var final map[string]any
	if err := json.Unmarshal(lines[2], &final); err != nil {
		t.Fatalf("decode final chunk: %v", err)
	}
	if final["done"] != true {
		t.Fatalf("final done = %v", final["done"])
	}
	if final["done_reason"] != "stop" {
		t.Fatalf("final done_reason = %v", final["done_reason"])
	}
	if final["response"] != "" {
		t.Fatalf("final response = %v", final["response"])
	}
}

func TestChatNonStreaming(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-2","created":1710000003,"model":"chat-model","choices":[{"message":{"role":"assistant","content":"Hello back"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{
		"model":"chat-model",
		"stream":false,
		"messages":[{"role":"user","content":"Hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["done"] != true {
		t.Fatalf("done = %v", response["done"])
	}
	message, ok := response["message"].(map[string]any)
	if !ok {
		t.Fatalf("message = %v", response["message"])
	}
	if message["content"] != "Hello back" {
		t.Fatalf("message content = %v", message["content"])
	}
	if response["done_reason"] != "stop" {
		t.Fatalf("done_reason = %v", response["done_reason"])
	}
	if _, ok := response["created_at"].(string); !ok {
		t.Fatalf("created_at missing or invalid: %v", response["created_at"])
	}
}

func TestEmbedAndEmbeddingsNormalization(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"embed-model","data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}]}`))
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")

	for _, tc := range []struct {
		name string
		path string
		body string
		want string
	}{
		{name: "embed", path: "/api/embed", body: `{"model":"embed-model","input":["a","b"]}`, want: `{"model":"embed-model","embeddings":[[0.1,0.2],[0.3,0.4]]}`},
		{name: "embeddings", path: "/api/embeddings", body: `{"model":"embed-model","prompt":"a"}`, want: `{"embedding":[0.1,0.2]}`},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			assertJSONEqual(t, rr.Body.Bytes(), []byte(tc.want))
		})
	}
}

func TestTagsAndHealth(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"missing"}`))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"alpha","owned_by":"local","object":"model"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")

	t.Run("tags", func(t *testing.T) {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tags", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode tags: %v", err)
		}
		models, ok := response["models"].([]any)
		if !ok || len(models) != 1 {
			t.Fatalf("models = %v", response["models"])
		}
		model, ok := models[0].(map[string]any)
		if !ok {
			t.Fatalf("model entry = %v", models[0])
		}
		if model["name"] != "alpha" || model["model"] != "alpha" {
			t.Fatalf("model identifiers = %v", model)
		}
		if modifiedAt, ok := model["modified_at"].(string); !ok || modifiedAt != "" {
			t.Fatalf("modified_at = %#v", model["modified_at"])
		}
		if size, ok := model["size"].(float64); !ok || size != 0 {
			t.Fatalf("size = %#v", model["size"])
		}
		if digest, ok := model["digest"].(string); !ok || digest != "" {
			t.Fatalf("digest = %#v", model["digest"])
		}
		details, ok := model["details"].(map[string]any)
		if !ok {
			t.Fatalf("details = %v", model["details"])
		}
		if details["parent_model"] != "" || details["format"] != "" || details["family"] != "" || details["parameter_size"] != "" || details["quantization_level"] != "" {
			t.Fatalf("details strings = %v", details)
		}
		if families, exists := details["families"]; !exists || families != nil {
			t.Fatalf("families = %#v", families)
		}
	})

	t.Run("health", func(t *testing.T) {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode health: %v", err)
		}
		if response["status"] != "ok" || response["upstream"] != "ok" || response["version"] != "0.1.0" {
			t.Fatalf("health response = %v", response)
		}
	})
}

func TestTagsUsesRichMetadataWhenAvailable(t *testing.T) {
	t.Parallel()

	var restCalls int
	var openAICalls int

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			restCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"models":[
					{
						"type":"llm",
						"publisher":"lmstudio-community",
						"key":"gemma-3-270m-it-qat",
						"display_name":"Gemma 3 270m Instruct Qat",
						"architecture":"gemma3",
						"quantization":{"name":"Q4_0","bits_per_weight":4},
						"size_bytes":241410208,
						"params_string":"270M",
						"loaded_instances":[{"id":"gemma-3-270m-it-qat","config":{"context_length":4096}}],
						"max_context_length":32768,
						"format":"gguf",
						"capabilities":{"vision":false,"trained_for_tool_use":false},
						"description":null
					}
				]
			}`))
		case "/v1/models":
			openAICalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"should-not-be-used"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tags", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if restCalls != 1 {
		t.Fatalf("restCalls = %d", restCalls)
	}
	if openAICalls != 0 {
		t.Fatalf("openAICalls = %d", openAICalls)
	}

	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode tags: %v", err)
	}
	models, ok := response["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %v", response["models"])
	}
	model, ok := models[0].(map[string]any)
	if !ok {
		t.Fatalf("model entry = %v", models[0])
	}
	if model["name"] != "gemma-3-270m-it-qat" || model["model"] != "gemma-3-270m-it-qat" {
		t.Fatalf("model identifiers = %v", model)
	}
	if size, ok := model["size"].(float64); !ok || size != 241410208 {
		t.Fatalf("size = %#v", model["size"])
	}
	details, ok := model["details"].(map[string]any)
	if !ok {
		t.Fatalf("details = %v", model["details"])
	}
	if details["format"] != "gguf" {
		t.Fatalf("format = %v", details["format"])
	}
	if details["family"] != "gemma3" {
		t.Fatalf("family = %v", details["family"])
	}
	if details["parameter_size"] != "270M" {
		t.Fatalf("parameter_size = %v", details["parameter_size"])
	}
	if details["quantization_level"] != "Q4_0" {
		t.Fatalf("quantization_level = %v", details["quantization_level"])
	}
	if families, exists := details["families"]; !exists || families != nil {
		t.Fatalf("families = %#v", families)
	}
}

func TestMetadataFallbackToOpenAIModels(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"missing"}`))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"alpha"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, logger, "0.1.0")

	t.Run("tags fallback stays sparse", func(t *testing.T) {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tags", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		assertJSONEqual(t, rr.Body.Bytes(), []byte(`{
			"models":[{
				"name":"alpha",
				"model":"alpha",
				"modified_at":"",
				"size":0,
				"digest":"",
				"details":{
					"parent_model":"",
					"format":"",
					"family":"",
					"families":null,
					"parameter_size":"",
					"quantization_level":""
				}
			}]
		}`))
	})

	t.Run("show fallback still works", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"model":"alpha"}`))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode show: %v", err)
		}
		if response["model"] != "alpha" {
			t.Fatalf("response model = %v", response["model"])
		}
		assertCapabilities(t, response, "completion")
		if !strings.Contains(logs.String(), `"metadata_source":"openai"`) || !strings.Contains(logs.String(), `"fallback_used":true`) {
			t.Fatalf("logs = %s", logs.String())
		}
	})
}

func TestShowRoute(t *testing.T) {
	t.Parallel()

	t.Run("rich metadata yields tools capability without top-level vision claim", func(t *testing.T) {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/models":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"models":[
						{
							"type":"llm",
							"publisher":"lmstudio-community",
							"key":"vision-model",
							"display_name":"Vision Model",
							"architecture":"llama",
							"quantization":{"name":"Q8_0","bits_per_weight":8},
							"size_bytes":123456,
							"params_string":"7B",
							"loaded_instances":[{"id":"vision-model","config":{"context_length":8192}}],
							"max_context_length":16384,
							"format":"gguf",
							"capabilities":{"vision":true,"trained_for_tool_use":true},
							"description":"rich metadata"
						}
					]
				}`))
			case "/v1/models":
				t.Fatalf("unexpected fallback request")
			default:
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
		}))
		defer upstream.Close()

		handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, logger, "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"model":"vision-model","verbose":true}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}

		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response["model"] != "vision-model" {
			t.Fatalf("model = %v", response["model"])
		}
		if response["parameters"] != "7B" {
			t.Fatalf("parameters = %v", response["parameters"])
		}
		assertCapabilities(t, response, "completion", "tools", "vision")
		details, ok := response["details"].(map[string]any)
		if !ok {
			t.Fatalf("details = %v", response["details"])
		}
		if details["format"] != "gguf" || details["family"] != "llama" || details["parameter_size"] != "7B" || details["quantization_level"] != "Q8_0" {
			t.Fatalf("details = %v", details)
		}
		modelInfo, ok := response["model_info"].(map[string]any)
		if !ok {
			t.Fatalf("model_info = %v", response["model_info"])
		}
		if modelInfo["display_name"] != "Vision Model" {
			t.Fatalf("display_name = %v", modelInfo["display_name"])
		}
		if modelInfo["architecture"] != "llama" {
			t.Fatalf("architecture = %v", modelInfo["architecture"])
		}
		// Copilot reads general.architecture to build the context_length key
		if modelInfo["general.architecture"] != "llama" {
			t.Fatalf("general.architecture = %v", modelInfo["general.architecture"])
		}
		if modelInfo["general.basename"] != "Vision Model" {
			t.Fatalf("general.basename = %v", modelInfo["general.basename"])
		}
		if modelInfo["format"] != "gguf" {
			t.Fatalf("format = %v", modelInfo["format"])
		}
		if modelInfo["max_context_length"] != float64(16384) {
			t.Fatalf("max_context_length = %v", modelInfo["max_context_length"])
		}
		if modelInfo["loaded_context_length"] != float64(8192) {
			t.Fatalf("loaded_context_length = %v", modelInfo["loaded_context_length"])
		}
		// VS Code reads {arch}.context_length; loaded value (8192) should take priority over max (16384)
		if modelInfo["llama.context_length"] != float64(8192) {
			t.Fatalf("llama.context_length = %v", modelInfo["llama.context_length"])
		}
		capabilityInfo, ok := modelInfo["capabilities"].(map[string]any)
		if !ok {
			t.Fatalf("capabilities info = %v", modelInfo["capabilities"])
		}
		if capabilityInfo["vision"] != true || capabilityInfo["trained_for_tool_use"] != true {
			t.Fatalf("capabilities info = %v", capabilityInfo)
		}
		if !strings.Contains(logs.String(), `"metadata_source":"rest"`) || !strings.Contains(logs.String(), `"fallback_used":false`) {
			t.Fatalf("logs = %s", logs.String())
		}
	})

	t.Run("rich embedding metadata yields embedding capability", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/models":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"models":[
						{
							"type":"embedding",
							"publisher":"lmstudio-community",
							"key":"embed-model",
							"display_name":"Embedding Model",
							"architecture":"nomic",
							"size_bytes":654321,
							"loaded_instances":[{"id":"embed-model","config":{"context_length":2048}}],
							"max_context_length":8192,
							"format":"gguf",
							"capabilities":{"vision":true,"trained_for_tool_use":true},
							"description":"embedding metadata"
						}
					]
				}`))
			case "/v1/models":
				t.Fatalf("unexpected fallback request")
			default:
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
		}))
		defer upstream.Close()

		handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"model":"embed-model"}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}

		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		assertCapabilities(t, response, "embedding")

		modelInfo, ok := response["model_info"].(map[string]any)
		if !ok {
			t.Fatalf("model_info = %v", response["model_info"])
		}
		capabilityInfo, ok := modelInfo["capabilities"].(map[string]any)
		if !ok {
			t.Fatalf("capabilities info = %v", modelInfo["capabilities"])
		}
		if capabilityInfo["vision"] != true || capabilityInfo["trained_for_tool_use"] != true {
			t.Fatalf("capabilities info = %v", capabilityInfo)
		}
	})

	t.Run("existing model", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			switch r.URL.Path {
			case "/api/v1/models":
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"missing"}`))
			case "/v1/models":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"beta"}]}`))
			default:
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
		}))
		defer upstream.Close()

		handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"model":"beta","verbose":true}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		assertJSONEqual(t, rr.Body.Bytes(), []byte(`{
			"model":"beta",
			"remote_model":"beta",
			"license":"",
			"modelfile":"",
			"parameters":"",
			"template":"",
			"system":"",
			"modified_at":"",
			"capabilities":["completion"],
			"messages":[],
			"details":{
				"parent_model":"",
				"format":"",
				"family":"",
				"families":null,
				"parameter_size":"",
				"quantization_level":""
			},
			"model_info":{"general.basename":"beta"}
		}`))
	})

	t.Run("name only request succeeds and logs resolved model", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/models":
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"missing"}`))
			case "/v1/models":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"beta"}]}`))
			default:
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
		}))
		defer upstream.Close()

		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, logger, "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"name":" beta "}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response["model"] != "beta" {
			t.Fatalf("response model = %v", response["model"])
		}
		if !strings.Contains(logs.String(), `"model":"beta"`) {
			t.Fatalf("logs = %s", logs.String())
		}
	})

	t.Run("model and name mismatch returns bad request", func(t *testing.T) {
		handler := NewHandler(config.Config{UpstreamBaseURL: "http://127.0.0.1:1/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"model":"alpha","name":"beta"}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "model and name must match") {
			t.Fatalf("body = %s", rr.Body.String())
		}
	})

	t.Run("tolerated compatibility fields succeed", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/models":
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"missing"}`))
			case "/v1/models":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"beta"}]}`))
			default:
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
		}))
		defer upstream.Close()

		handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{
			"model":"beta",
			"name":"beta",
			"verbose":true,
			"system":"ignored",
			"template":{"kind":"ignored"},
			"options":{"temperature":0.2}
		}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if modifiedAt, ok := response["modified_at"].(string); !ok || modifiedAt != "" {
			t.Fatalf("modified_at = %#v", response["modified_at"])
		}
		assertCapabilities(t, response, "completion")
		if messages, ok := response["messages"].([]any); !ok || len(messages) != 0 {
			t.Fatalf("messages = %#v", response["messages"])
		}
	})

	t.Run("unknown model", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/models":
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"missing"}`))
			case "/v1/models":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"alpha"}]}`))
			default:
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
		}))
		defer upstream.Close()

		handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"model":"missing"}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "model not found") {
			t.Fatalf("body = %s", rr.Body.String())
		}
	})

	t.Run("missing model", func(t *testing.T) {
		handler := NewHandler(config.Config{UpstreamBaseURL: "http://127.0.0.1:1/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"verbose":false}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "model or name is required") {
			t.Fatalf("body = %s", rr.Body.String())
		}
	})

	t.Run("unknown field remains rejected", func(t *testing.T) {
		handler := NewHandler(config.Config{UpstreamBaseURL: "http://127.0.0.1:1/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"model":"beta","unexpected":true}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "unknown field") {
			t.Fatalf("body = %s", rr.Body.String())
		}
	})
}

func TestUnsupportedFieldsAndUpstreamErrors(t *testing.T) {
	t.Parallel()

	t.Run("unsupported field rejected", func(t *testing.T) {
		handler := NewHandler(config.Config{UpstreamBaseURL: "http://127.0.0.1:1/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(`{"model":"m","prompt":"x","raw":true}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "unsupported field") {
			t.Fatalf("body = %s", rr.Body.String())
		}
	})

	t.Run("upstream structured error passthrough", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found","type":"invalid_request_error"}}`))
		}))
		defer upstream.Close()

		handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"model":"missing","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "model not found") {
			t.Fatalf("body = %s", rr.Body.String())
		}
	})

	t.Run("unreachable upstream", func(t *testing.T) {
		handler := NewHandler(config.Config{UpstreamBaseURL: "http://127.0.0.1:1/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
		req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
	})
}

func TestOpenAIModelsPassthrough(t *testing.T) {
	t.Parallel()

	const upstreamBody = `{"object":"list","data":[{"id":"alpha","object":"model","owned_by":"local"}]}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	assertJSONEqual(t, rr.Body.Bytes(), []byte(upstreamBody))
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("unexpected content type: %s", ct)
	}
}

func TestOpenAIChatCompletionsNonStreamingPassthrough(t *testing.T) {
	t.Parallel()

	const requestBody = `{
		"model":"chat-model",
		"messages":[{"role":"user","content":"Hello"}],
		"stream":false,
		"tool_choice":"auto",
		"metadata":{"source":"test"}
	}`
	const responseBody = `{
		"id":"chatcmpl-1",
		"object":"chat.completion",
		"created":1710000003,
		"model":"chat-model",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello back"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
	}`

	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var err error
		upstreamBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	assertJSONEqual(t, upstreamBody, []byte(requestBody))
	assertJSONEqual(t, rr.Body.Bytes(), []byte(responseBody))
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("unexpected content type: %s", ct)
	}
}

func TestOpenAIChatCompletionsStreamingPassthrough(t *testing.T) {
	t.Parallel()

	const responseBody = "data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1710000004,\"model\":\"chat-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1710000004,\"model\":\"chat-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat-model","stream":true,"messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != responseBody {
		t.Fatalf("body = %q", rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("unexpected content type: %s", ct)
	}
	if cacheControl := rr.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("unexpected cache-control: %s", cacheControl)
	}
	if strings.Contains(rr.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("unexpected ndjson content type: %s", rr.Header().Get("Content-Type"))
	}
}

func TestOpenAIChatCompletionsLogging(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-3","object":"chat.completion","created":1710000005,"model":"chat-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	handler := NewHandler(config.Config{UpstreamBaseURL: upstream.URL + "/v1"}, logger, "0.1.0")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat-model","stream":true,"messages":[{"role":"user","content":"Hello"}],"tools":[{"type":"function"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	logOutput := logs.String()
	for _, fragment := range []string{
		`"route":"/v1/chat/completions"`,
		`"status_code":200`,
		`"request_id":"req-000001"`,
		`"upstream_endpoint":"/chat/completions"`,
		`"model":"chat-model"`,
		`"stream":true`,
		`"latency":"`,
	} {
		if !strings.Contains(logOutput, fragment) {
			t.Fatalf("missing log fragment %q in %s", fragment, logOutput)
		}
	}
}

func TestVersionRoute(t *testing.T) {
	t.Parallel()

	handler := NewHandler(config.Config{UpstreamBaseURL: "http://127.0.0.1:1/v1"}, slog.New(slog.NewJSONHandler(io.Discard, nil)), "0.1.0")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/version", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if response["version"] != "0.1.0" {
		t.Fatalf("version = %v", response["version"])
	}
	if response["service"] != "lmstudio-ollama-bridge" {
		t.Fatalf("service = %v", response["service"])
	}
}

func collectNDJSON(t *testing.T, body []byte) [][]byte {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(body))
	var lines [][]byte
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		copyLine := make([]byte, len(line))
		copy(copyLine, line)
		lines = append(lines, copyLine)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan ndjson: %v", err)
	}
	return lines
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue any
	var wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got json: %v", err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want json: %v", err)
	}
	gotNormalized, _ := json.Marshal(gotValue)
	wantNormalized, _ := json.Marshal(wantValue)
	if !bytes.Equal(gotNormalized, wantNormalized) {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", gotNormalized, wantNormalized)
	}
}

func assertCapabilities(t *testing.T, response map[string]any, want ...string) {
	t.Helper()
	capabilities, ok := response["capabilities"].([]any)
	if !ok {
		t.Fatalf("capabilities = %#v", response["capabilities"])
	}
	if len(capabilities) != len(want) {
		t.Fatalf("capability count = %d, want %d (%#v)", len(capabilities), len(want), response["capabilities"])
	}
	for index, capability := range want {
		if capabilities[index] != capability {
			t.Fatalf("capability[%d] = %v, want %s", index, capabilities[index], capability)
		}
	}
}
