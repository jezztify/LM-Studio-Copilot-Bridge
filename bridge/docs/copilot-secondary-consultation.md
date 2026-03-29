# Copilot Secondary Consultation Requirement

## Goal

Use the bridge-backed model selected in Copilot Chat as the primary model, allow it to trigger consultation with a Copilot-hosted secondary model by emitting `<consult>...</consult>`, and keep the final visible answer authored by the primary model.

## Architecture

1. The Go bridge remains responsible only for LM Studio compatibility and the primary model transport.
2. The `@model2` VS Code chat participant owns the full consultation turn.
3. The extension uses supported VS Code language model APIs for both model selection and per-hop requests.
4. The bridge does not claim to orchestrate Copilot-hosted consultation by itself.

## Approved V1 Protocol

1. User sends a prompt through `@model2`.
2. The extension builds the primary request from the latest user prompt, selected recent history, and explicit consultation instructions.
3. The primary model returns either final answer text or only `<consult>message</consult>`.
4. If the primary returns final text, that text becomes the final visible answer.
5. If the primary returns `<consult>message</consult>`, the extension records a primary-to-secondary consultation turn, emits a labeled status line, and sends the consult message plus allowed context to the secondary model.
6. The secondary model returns either plain guidance or only `<consult>message</consult>`.
7. Any secondary output is recorded as consultation transcript and fed back to the primary in a new primary request.
8. The loop repeats until the primary returns final non-consult text, the user cancels, timeout is reached, the token budget is exhausted, or the consultation round cap is hit.
9. If consultation cannot continue, the extension forces a primary-only wrap-up attempt and explicitly tells the primary that consultation is unavailable or exhausted.

## UX Contract

1. `@model2` is the explicit opt-in consultation entry point.
2. The UI shows short speaker-labeled operational status lines such as `Primary model consulting secondary model` and `Secondary model returned guidance`.
3. Raw `<consult>...</consult>` tags are never shown to the user.
4. The final visible answer is always streamed only after the extension determines that the primary model returned final text.
5. The participant appends an optional collapsed markdown details block containing a summarized speaker-labeled consultation transcript.

## Data Scope

Forwarded to the secondary model in V1:

1. Latest user prompt.
2. Selected recent chat turns serialized by the participant.
3. Summarized prior consultation transcript when continuity is needed.

Excluded from the secondary model in V1:

1. Workspace context not explicitly serialized by the participant.
2. Tool invocations.
3. Attachments.
4. Hidden prompts unrelated to consultation orchestration.

## Limits And Settings

1. Maximum consultation round-trips: `2`.
2. Secondary selection policy: fixed preferred Copilot family from settings.
3. Shared timeout: extension setting.
4. Shared approximate token budget: extension setting.

## Implementation Notes

The extension uses:

1. `request.model` for the active primary model.
2. `vscode.lm.selectChatModels(...)` for the fixed secondary selector.
3. `model.sendRequest(...)` for buffered primary and secondary hops.
4. Explicit context rebuilding on every hop because there is no supported pause-resume generation handle.