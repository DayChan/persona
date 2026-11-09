package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// chatCompletions sends an ordinary system message, which is supported by local servers as well as hosted APIs.
func (c *Client) chatCompletions(ctx context.Context, hc *http.Client, key string, r Request) (Reply, bool, error) {
	turns := make([]Message, 0, len(r.Messages)+1)
	if r.System != "" {
		turns = append(turns, Message{Role: "system", Content: r.System})
	}
	turns = append(turns, r.Messages...)
	body := map[string]any{"model": c.cfg.Model, "messages": turns, "stream": true}
	if r.MaxTokens > 0 {
		body["max_tokens"] = r.MaxTokens
	}
	for attempt := 0; ; attempt++ {
		encoded, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiEndpoint(c.cfg.BaseURL, "chat/completions"), bytes.NewReader(encoded))
		if err != nil {
			return Reply{}, false, err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("accept", "text/event-stream")
		req.Header.Set("authorization", "Bearer "+key)
		resp, err := hc.Do(req)
		if err != nil {
			return Reply{}, false, fmt.Errorf("ai: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			// Some models reject max_tokens and explicitly ask for the newer name. Retry only that
			// rejected request; arbitrary 400s and already-started streams must never be replayed.
			if attempt == 0 && r.MaxTokens > 0 && resp.StatusCode == http.StatusBadRequest && ChatRequiresCompletionTokens(raw) {
				delete(body, "max_tokens")
				body["max_completion_tokens"] = r.MaxTokens
				continue
			}
			return Reply{}, false, newHTTPError(resp, raw)
		}
		reply, err := readCompletions(resp.Body)
		resp.Body.Close()
		return reply, true, err
	}
}

// ChatRequiresCompletionTokens recognizes a rejected token field that explicitly requests its replacement.
func ChatRequiresCompletionTokens(raw []byte) bool {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	message := strings.ToLower(envelope.Error.Message)
	return strings.Contains(message, "max_tokens") && strings.Contains(message, "max_completion_tokens") &&
		(strings.Contains(message, "unsupported") || strings.Contains(message, "not supported"))
}

// readCompletions consumes SSE events, including usage-only events after the finish reason.
func readCompletions(body io.Reader) (Reply, error) {
	var reply Reply
	var text strings.Builder
	var data []string
	finished, done, refused := false, false, false
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		if payload == "[DONE]" {
			done = true
			return nil
		}
		var chunk struct {
			Model string `json:"model"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content string `json:"content"`
					Refusal string `json:"refusal"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				PromptDetails    struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("ai: decode stream: %w", err)
		}
		if chunk.Error != nil {
			return fmt.Errorf("ai: stream error: %s", chunk.Error.Message)
		}
		if chunk.Model != "" {
			reply.Model = chunk.Model
		}
		if u := chunk.Usage; u != nil {
			cached := max(0, min(u.PromptTokens, u.PromptDetails.CachedTokens))
			reply.Usage = Usage{InputTokens: u.PromptTokens - cached, OutputTokens: u.CompletionTokens, CacheReadInputTokens: cached}
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			text.WriteString(choice.Delta.Content)
			refused = refused || choice.Delta.Refusal != "" || choice.FinishReason == "content_filter"
			if choice.FinishReason != "" {
				finished = true
				reply.Truncated = choice.FinishReason == "length"
			}
		}
		return nil
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return Reply{}, err
			}
			if done {
				break
			}
		} else if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return Reply{}, fmt.Errorf("ai: read stream: %w", err)
	}
	if err := consume(); err != nil {
		return Reply{}, err
	}
	if !done && !finished {
		return Reply{}, errors.New("ai: stream ended without a completed reply")
	}
	if refused {
		return reply, ErrRefused
	}
	reply.Text = text.String()
	if strings.TrimSpace(reply.Text) == "" && !reply.Truncated {
		return Reply{}, errors.New("ai: stream ended without a reply")
	}
	return reply, nil
}
