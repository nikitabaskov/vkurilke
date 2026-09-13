package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	endpoint string
	http     *http.Client
}

func NewClient(token string) *Client {
	return &Client{endpoint: "https://api.telegram.org/bot" + token + "/", http: &http.Client{Timeout: 40 * time.Second}}
}

type APIError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string { return fmt.Sprintf("Telegram API %d: %s", e.Code, e.Description) }
func (c *Client) Call(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid Telegram request")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Telegram connection failed")
	}
	defer response.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Code        int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&envelope); err != nil {
		return errors.New("invalid Telegram response")
	}
	if !envelope.OK {
		return &APIError{Code: envelope.Code, Description: envelope.Description, RetryAfter: envelope.Parameters.RetryAfter}
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

type Button struct {
	Text         string  `json:"text"`
	CallbackData string  `json:"callback_data,omitempty"`
	WebApp       *WebApp `json:"web_app,omitempty"`
}
type WebApp struct {
	URL string `json:"url"`
}
type Keyboard struct {
	Rows [][]Button `json:"inline_keyboard"`
}

func (c *Client) Send(ctx context.Context, user int64, text string, keyboard Keyboard) (int64, error) {
	var message struct {
		ID int64 `json:"message_id"`
	}
	err := c.Call(ctx, "sendMessage", messagePayload(user, text, keyboard), &message)
	return message.ID, err
}
func (c *Client) Edit(ctx context.Context, user, message int64, text string, keyboard Keyboard) error {
	payload := messagePayload(user, text, keyboard)
	payload["message_id"] = message
	err := c.Call(ctx, "editMessageText", payload, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == 400 && strings.Contains(apiErr.Description, "message is not modified") {
		return nil
	}
	return err
}

// An edit without reply_markup removes the old inline keyboard.
func messagePayload(user int64, text string, keyboard Keyboard) map[string]any {
	payload := map[string]any{"chat_id": user, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}}
	if len(keyboard.Rows) > 0 {
		payload["reply_markup"] = keyboard
	}
	return payload
}
