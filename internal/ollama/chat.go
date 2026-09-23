package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ChatMessage is one turn of a conversation.
type ChatMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// ChatChunk is one line of the streamed reply from POST /api/chat. Thinking
// models send their reasoning in Message.Thinking, apart from the answer.
// The last chunk has Done set and the timings (in nanoseconds).
type ChatChunk struct {
	Message struct {
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	Done               bool   `json:"done"`
	DoneReason         string `json:"done_reason"`
	TotalDuration      int64  `json:"total_duration"`
	LoadDuration       int64  `json:"load_duration"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	EvalCount          int    `json:"eval_count"`
	EvalDuration       int64  `json:"eval_duration"`
	Error              string `json:"error"`
}

// Chat sends a conversation to a model and calls fn with each chunk of the
// reply as it's generated, returning once it's done. Cancelling ctx stops
// the generation (Ollama stops when the request goes away).
func (c *Client) Chat(ctx context.Context, model string, messages []ChatMessage, fn func(ChatChunk)) error {
	body, _ := json.Marshal(map[string]any{"model": model, "messages": messages, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.stream.Do(req) // no overall timeout: loading a big model plus a long answer takes a while
	if err != nil {
		return fmt.Errorf("contact ollama at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		msg := resp.Status
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &StatusError{StatusCode: resp.StatusCode, Message: msg}
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ch ChatChunk
		if err := json.Unmarshal(line, &ch); err != nil {
			return fmt.Errorf("unexpected response from ollama: %q", line)
		}
		if ch.Error != "" {
			return errors.New(ch.Error)
		}
		fn(ch)
		if ch.Done {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("ollama closed the connection before the reply finished")
}
