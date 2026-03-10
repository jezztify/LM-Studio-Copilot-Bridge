# LM Studio Copilot Bridge

Use your local LM Studio models inside **GitHub Copilot Chat** in VS Code.

GitHub Copilot's "Bring Your Own" Ollama feature (`github.copilot.chat.byok.ollamaEndpoint`) lets you point Copilot at any Ollama-compatible server. This bridge impersonates that server so Copilot talks to your LM Studio instance instead.

```
VS Code Copilot Chat
        │
        │  Ollama API  (port 11434)
        ▼
  lmstudio-copilot-bridge      ← this project
        │
        │  OpenAI-compatible API  (port 1234)
        ▼
    LM Studio
```

The bridge runs as a small local HTTP server that exposes a documented subset of the Ollama HTTP API and translates those requests into LM Studio's OpenAI-compatible API. It also exposes pass-through `/v1/*` routes so clients that speak OpenAI natively can share the same port.

The goal is practical Copilot + LM Studio compatibility, not full Ollama parity.

## Copilot Setup

1. Start LM Studio and load a model.
2. Enable LM Studio's local server (default `http://localhost:1234`).
3. Start this bridge (see [Quick Start](#quick-start)).
4. In VS Code, `CTRL + SHIFT + P` > Manage Language Model > Add Model > Choose Ollama > `http://localhost:11434`
5. Once it is done, you should be able to see your models.

> **Tip**: The bridge exposes the `tools` capability for models that LM Studio marks as trained for tool use. Models without that flag will not show a Tools badge in the Copilot model picker.

## Supported Routes

Route families:

- `/api/*` stays Ollama-compatible. These routes translate requests into LM Studio's OpenAI-compatible upstream API and normalize responses back into Ollama-like shapes.
- `/v1/*` is a minimal OpenAI-compatible proxy surface. These routes forward OpenAI-shaped requests to LM Studio and preserve OpenAI-shaped responses.

- `POST /api/generate`
- `POST /api/chat`
- `POST /api/embed`
- `POST /api/embeddings`
- `POST /api/show`
- `GET /api/tags`
- `GET /v1/models`
- `POST /v1/chat/completions`
- `GET /api/version`
- `GET /healthz`

Streaming is supported for both route families:

- `/api/generate` and `/api/chat` emit newline-delimited JSON in an Ollama-like shape.
- `/v1/chat/completions` preserves upstream server-sent events and OpenAI chunk shapes.

## Quick Start

Prerequisites:

- Go 1.24+
- LM Studio running with its local server enabled (default `http://localhost:1234`)

Run the bridge:

```
./run.sh
```

Or manually:

```
go run ./cmd/lmstudio-ollama-bridge
```

By default the bridge listens on `127.0.0.1:11434` (the standard Ollama port) and forwards to `http://localhost:1234/v1`.

If LM Studio is on another machine on your network, set `LMSTUDIO_BASE_URL`:

```
LMSTUDIO_BASE_URL=http://192.168.1.100:1234/v1 ./run.sh
```

Check health:

```
curl http://127.0.0.1:11434/healthz
```

## Configuration

Environment variables:

- `BRIDGE_BIND_HOST`: bind host, default `127.0.0.1`
- `BRIDGE_BIND_PORT`: bind port, default `11434`
- `LMSTUDIO_BASE_URL`: upstream base URL, default `http://localhost:1234/v1`
- `BRIDGE_LOG_LEVEL`: `debug`, `info`, `warn`, or `error`, default `info`

Flags override environment defaults:

```
go run ./cmd/lmstudio-ollama-bridge --host 127.0.0.1 --port 11434 --upstream http://localhost:1234/v1 --log-level debug
```

## Compatibility Behavior

Dual-route behavior:

- `/api/*` remains the compatibility layer for Ollama clients.
- `/v1/*` is intentionally thin and does not reuse the Ollama response translators.
- `GET /v1/models` proxies LM Studio `GET /v1/models` and returns an OpenAI-compatible model list shape.
- `POST /v1/chat/completions` proxies LM Studio `POST /v1/chat/completions` and keeps both non-streaming JSON and streaming SSE in OpenAI-compatible shapes.
- Model identifiers are passed through as provided so a model discovered via `/api/tags`, `/api/show`, or `/v1/models` can be reused directly when LM Studio accepts that identifier.

Model resolution:

- Incoming `model` values are passed through to LM Studio exactly as received.
- There is no alias mapping in v1.
- If LM Studio cannot resolve the model, the bridge returns the upstream error instead of silently falling back.

Supported field mappings:

- `model`
- `stream`
- `temperature`
- `top_p`
- `stop`
- `seed`
- `max_tokens`
- `options.temperature`
- `options.top_p`
- `options.stop`
- `options.seed`
- `options.max_tokens`
- `options.num_predict` mapped to `max_tokens`

`/api/show` behavior:

- The bridge accepts both `model` and `name` on input. If both are present, they must match after trimming.
- The bridge resolves metadata from LM Studio `GET /api/v1/models` first and falls back to `GET /v1/models` when richer metadata is unavailable, fails, or cannot be used.
- The endpoint is a degraded compatibility surface for Ollama clients that expect `POST /api/show` to exist.
- `system`, `template`, `options`, and `verbose` are accepted for compatibility but do not change upstream metadata lookup behavior.
- When richer REST metadata is available, the bridge fills truthful `details`, `parameters`, and `model_info` fields from LM Studio without inventing Ollama metadata.
- Top-level `capabilities` remain conservative, but the bridge now exposes `tools` on `/api/show` when richer LM Studio metadata explicitly marks a text model as trained for tool use. Upstream traits such as `vision` still remain under `model_info.capabilities` instead.
- `modified_at` and `messages` remain placeholder compatibility fields with stable empty values.

`/api/tags` behavior:

- The bridge resolves model metadata from LM Studio `GET /api/v1/models` first and falls back to `GET /v1/models` when richer metadata is unavailable, fails, or cannot be used.
- The response stays Ollama-like for client compatibility while truthfully enriching `size` and `details` from richer LM Studio metadata when available.
- Unavailable string metadata is returned as `""`, unavailable numeric size is returned as `0`, and `details.families` is returned as `null` instead of fabricated metadata.

Unsupported-field policy:

- The bridge rejects semantic fields that cannot be mapped safely with `400`.
- The bridge ignores and debug-logs operational hint fields such as `keep_alive`.

Rejected with `400` in v1:

- `raw`
- `template`
- deprecated `context`
- image payloads
- `tools`
- `think`

Error behavior:

- `400` for invalid JSON or unsupported semantic fields
- `502` when LM Studio is unreachable
- upstream JSON errors are passed through with the upstream status code when available
- upstream `/v1/*` responses, including streaming chat completions, are proxied through without Ollama-shape normalization

Health behavior:

- `GET /healthz` reports whether the bridge process is running and whether LM Studio is reachable
- `200` with `{"status":"ok","upstream":"ok"}` when both are healthy
- `503` with `{"status":"degraded","upstream":"error"}` when the bridge is up but LM Studio is unavailable

## Route Examples

### Generate

Request:

```json
{
  "model": "qwen2.5-coder-7b-instruct",
  "prompt": "Write a haiku about logs.",
  "stream": false,
  "options": {
    "temperature": 0.2,
    "num_predict": 64
  }
}
```

Example:

```bash
curl -X POST http://127.0.0.1:11434/api/generate -H "Content-Type: application/json" -d '{"model":"qwen2.5-coder-7b-instruct","prompt":"Write a haiku about logs.","stream":false}'
```

Response shape:

```json
{
  "model": "qwen2.5-coder-7b-instruct",
  "created_at": "2026-03-10T12:00:00Z",
  "response": "Quiet JSON rivers...",
  "done": true,
  "done_reason": "stop"
}
```

Streaming example:

```bash
curl -N -X POST http://127.0.0.1:11434/api/generate -H "Content-Type: application/json" -d '{"model":"qwen2.5-coder-7b-instruct","prompt":"Count to three.","stream":true}'
```

Streaming chunk shape:

```json
{"model":"qwen2.5-coder-7b-instruct","created_at":"2026-03-10T12:00:00Z","response":"One","done":false}
{"model":"qwen2.5-coder-7b-instruct","created_at":"2026-03-10T12:00:00Z","response":"","done":true,"done_reason":"stop"}
```

### Chat

Request:

```json
{
  "model": "qwen2.5-coder-7b-instruct",
  "messages": [
    {
      "role": "user",
      "content": "Summarize the purpose of this bridge in one sentence."
    }
  ],
  "stream": false
}
```

Example:

```bash
curl -X POST http://127.0.0.1:11434/api/chat -H "Content-Type: application/json" -d '{"model":"qwen2.5-coder-7b-instruct","messages":[{"role":"user","content":"Say hello"}],"stream":false}'
```

Response shape:

```json
{
  "model": "qwen2.5-coder-7b-instruct",
  "created_at": "2026-03-10T12:00:00Z",
  "message": {
    "role": "assistant",
    "content": "Hello."
  },
  "done": true,
  "done_reason": "stop"
}
```

Streaming example:

```bash
curl -N -X POST http://127.0.0.1:11434/api/chat -H "Content-Type: application/json" -d '{"model":"qwen2.5-coder-7b-instruct","messages":[{"role":"user","content":"Say hello"}],"stream":true}'
```

Streaming chunk shape:

```json
{"model":"qwen2.5-coder-7b-instruct","created_at":"2026-03-10T12:00:00Z","message":{"role":"assistant","content":"Hel"},"done":false}
{"model":"qwen2.5-coder-7b-instruct","created_at":"2026-03-10T12:00:00Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}
```

### Embed

Example:

```bash
curl -X POST http://127.0.0.1:11434/api/embed -H "Content-Type: application/json" -d '{"model":"text-embedding-nomic-embed","input":["alpha","beta"]}'
```

Response shape:

```json
{
  "model": "text-embedding-nomic-embed",
  "embeddings": [
    [0.1, 0.2],
    [0.3, 0.4]
  ]
}
```

### Embeddings

Example:

```bash
curl -X POST http://127.0.0.1:11434/api/embeddings -H "Content-Type: application/json" -d '{"model":"text-embedding-nomic-embed","prompt":"alpha"}'
```

Response shape:

```json
{
  "embedding": [0.1, 0.2]
}
```

### Tags

Example:

```bash
curl http://127.0.0.1:11434/api/tags
```

Response shape:

```json
{
  "models": [
    {
      "name": "qwen2.5-coder-7b-instruct",
    "model": "qwen2.5-coder-7b-instruct",
    "modified_at": "",
    "size": 0,
    "digest": "",
    "details": {
      "parent_model": "",
      "format": "",
      "family": "",
      "families": null,
      "parameter_size": "",
      "quantization_level": ""
    }
    }
  ]
}
```

### OpenAI Models

Example:

```bash
curl http://127.0.0.1:11434/v1/models
```

Response shape:

```json
{
  "object": "list",
  "data": [
    {
      "id": "qwen2.5-coder-7b-instruct",
      "object": "model",
      "owned_by": "local"
    }
  ]
}
```

### OpenAI Chat Completions

Example:

```bash
curl -X POST http://127.0.0.1:11434/v1/chat/completions -H "Content-Type: application/json" -d '{"model":"qwen2.5-coder-7b-instruct","messages":[{"role":"user","content":"Say hello"}],"stream":false}'
```

Response shape:

```json
{
  "id": "chatcmpl-1",
  "object": "chat.completion",
  "created": 1710000003,
  "model": "qwen2.5-coder-7b-instruct",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello."
      },
      "finish_reason": "stop"
    }
  ]
}
```

Streaming example:

```bash
curl -N -X POST http://127.0.0.1:11434/v1/chat/completions -H "Content-Type: application/json" -d '{"model":"qwen2.5-coder-7b-instruct","messages":[{"role":"user","content":"Say hello"}],"stream":true}'
```

Streaming chunk shape:

```text
data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000004,"model":"qwen2.5-coder-7b-instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}

data: [DONE]
```

### Version

Example:

```bash
curl http://127.0.0.1:11434/api/version
```

Response shape:

```json
{
  "service": "lmstudio-ollama-bridge",
  "version": "dev"
}
```

## Logging

The bridge uses structured JSON logs with:

- route
- model
- stream mode
- upstream endpoint
- status code
- latency
- request ID

Set `BRIDGE_LOG_LEVEL=debug` to see ignored operational hint fields such as `keep_alive`.

## Known Limitations

- This is not a full Ollama implementation.
- Ollama lifecycle routes such as pull, push, create, copy, delete, and ps are intentionally not implemented.
- Ollama-only metadata is omitted unless it can be derived truthfully from LM Studio.
- Multimodal payloads (images) are rejected.
- Tool-calling payloads are rejected on `/api/chat` (Ollama route); they pass through on `/v1/chat/completions` (OpenAI route, which Copilot uses).
- Unsupported semantic fields fail clearly rather than being emulated.
- `/api/show` and `/api/tags` prefer LM Studio `GET /api/v1/models` metadata, then fall back to `GET /v1/models` so sparse compatibility responses still work when richer metadata is unavailable.
- `/api/show` keeps top-level `capabilities` conservative to match bridge-supported request semantics; richer upstream traits may appear under `model_info` instead.

### Show

Example:

```bash
curl -X POST http://127.0.0.1:11434/api/show -H "Content-Type: application/json" -d '{"name":"qwen2.5-coder-7b-instruct","verbose":true,"system":"ignored","template":"ignored","options":{"temperature":0.2}}'
```

Response shape:

```json
{
  "model": "qwen2.5-coder-7b-instruct",
  "license": "",
  "modelfile": "",
  "parameters": "",
  "template": "",
  "system": "",
  "modified_at": "",
  "capabilities": ["completion"],
  "messages": [],
  "details": {
    "parent_model": "",
    "format": "",
    "family": "",
    "families": null,
    "parameter_size": "",
    "quantization_level": ""
  },
  "model_info": {}
}
```