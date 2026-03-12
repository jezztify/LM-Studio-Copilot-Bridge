#!/usr/bin/env bash
export PATH="$PATH:/c/Program Files/Go/bin"
set -euo pipefail

export BRIDGE_BIND_HOST=127.0.0.1
export BRIDGE_BIND_PORT=11434
export LMSTUDIO_BASE_URL=http://192.168.0.114:1234/v1
export BRIDGE_LOG_LEVEL=debug
export PWD="C:\Repositories\lmstudio-copilot-bridge\run.sh"

# Pretty-print JSON log lines while leaving non-JSON output untouched.
pretty_logs() {
	if command -v jq >/dev/null 2>&1; then
		while IFS= read -r line; do
			printf '%s\n' "$line" | jq . 2>/dev/null || printf '%s\n' "$line"
		done
		return
	fi

	local python_cmd=""
	if command -v python3 >/dev/null 2>&1; then
		python_cmd="python3"
	elif command -v python >/dev/null 2>&1; then
		python_cmd="python"
	else
		cat
		return
	fi

	"$python_cmd" -c 'import json, sys; [print(json.dumps(json.loads(line), indent=2)) if line.strip().startswith(("{", "[")) else print(line.rstrip("\n")) for line in sys.stdin]'
}

go run ./cmd/lmstudio-ollama-bridge | pretty_logs