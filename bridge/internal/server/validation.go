package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

func validateGenerateRequest(logger *slog.Logger, req generateRequest) error {
	if strings.TrimSpace(req.Model) == "" {
		return errors.New("model is required")
	}
	if unsupportedFieldPresent(req.Raw) {
		return unsupportedFieldError("raw")
	}
	if unsupportedFieldPresent(req.Template) {
		return unsupportedFieldError("template")
	}
	if unsupportedFieldPresent(req.Context) {
		return unsupportedFieldError("context")
	}
	if unsupportedFieldPresent(req.Images) {
		return unsupportedFieldError("images")
	}
	if unsupportedFieldPresent(req.Tools) {
		return unsupportedFieldError("tools")
	}
	if unsupportedFieldPresent(req.Think) {
		return unsupportedFieldError("think")
	}
	logIgnoredField(logger, "keep_alive", req.KeepAlive)
	return nil
}

func validateChatRequest(logger *slog.Logger, req chatRequest) error {
	if strings.TrimSpace(req.Model) == "" {
		return errors.New("model is required")
	}
	if len(req.Messages) == 0 {
		return errors.New("messages is required")
	}
	if unsupportedFieldPresent(req.Raw) {
		return unsupportedFieldError("raw")
	}
	if unsupportedFieldPresent(req.Template) {
		return unsupportedFieldError("template")
	}
	if unsupportedFieldPresent(req.Context) {
		return unsupportedFieldError("context")
	}
	if unsupportedFieldPresent(req.Images) {
		return unsupportedFieldError("images")
	}
	if unsupportedFieldPresent(req.Tools) {
		return unsupportedFieldError("tools")
	}
	if unsupportedFieldPresent(req.Think) {
		return unsupportedFieldError("think")
	}
	for _, message := range req.Messages {
		if unsupportedFieldPresent(message.Images) {
			return unsupportedFieldError("messages.images")
		}
	}
	logIgnoredField(logger, "keep_alive", req.KeepAlive)
	return nil
}

func validateEmbedRequest(logger *slog.Logger, req embedRequest, legacy bool) error {
	if strings.TrimSpace(req.Model) == "" {
		return errors.New("model is required")
	}
	if unsupportedFieldPresent(req.Images) {
		return unsupportedFieldError("images")
	}
	if unsupportedFieldPresent(req.Think) {
		return unsupportedFieldError("think")
	}
	if legacy {
		if !unsupportedFieldPresent(req.Prompt) && !unsupportedFieldPresent(req.Input) {
			return errors.New("prompt is required")
		}
	} else if !unsupportedFieldPresent(req.Input) && !unsupportedFieldPresent(req.Prompt) {
		return errors.New("input is required")
	}
	logIgnoredField(logger, "keep_alive", req.KeepAlive)
	return nil
}

func validateShowRequest(req showRequest) error {
	_, err := resolveShowModel(req)
	return err
}

func resolveShowModel(req showRequest) (string, error) {
	model := strings.TrimSpace(req.Model)
	name := strings.TrimSpace(req.Name)

	if model != "" && name != "" && model != name {
		return "", errors.New("model and name must match")
	}
	if model != "" {
		return model, nil
	}
	if name != "" {
		return name, nil
	}
	return "", errors.New("model or name is required")
}

func unsupportedFieldError(name string) error {
	return fmt.Errorf("unsupported field: %s", name)
}

func unsupportedFieldPresent(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func logIgnoredField(logger *slog.Logger, field string, value json.RawMessage) {
	if logger == nil || !unsupportedFieldPresent(value) {
		return
	}
	logger.Debug("ignoring field", "field", field)
}