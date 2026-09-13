package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"vkurilke/internal/store"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (fn roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestClientSendEditAndRateLimit(t *testing.T) {
	var calls []string
	c := NewClient("private-secret")
	c.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		if _, ok := payload["reply_markup"]; ok {
			t.Fatal("empty keyboard sent instead of omitted")
		}
		if _, ok := payload["parse_mode"]; ok {
			t.Fatal("untrusted names are interpreted as markup")
		}
		body := `{"ok":true,"result":{"message_id":42}}`
		if strings.HasSuffix(r.URL.Path, "editMessageText") {
			body = `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	ctx := context.Background()
	id, err := c.Send(ctx, 1, "<b>Имя</b>", Keyboard{Rows: [][]Button{}})
	if err != nil || id != 42 {
		t.Fatalf("send: %d %v", id, err)
	}
	if err = c.Edit(ctx, 1, id, "same", Keyboard{Rows: [][]Button{}}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatal("wrong call count")
	}
}
func TestSessionCardsCloseAndCallbacksFitTelegram(t *testing.T) {
	b := &Bot{BaseURL: "https://smoke.test", AnswerMinutes: 3}
	d := store.Delivery{Job: store.Job{Kind: "card", SessionID: strings.Repeat("a", 24), GroupID: "room"}, GroupName: "Общага", Members: []store.Member{{User: store.User{ID: 1, FirstName: "Аня"}, Status: "smoking"}, {User: store.User{ID: 2, FirstName: "Никита"}, Status: "going"}},
		Visits: []store.Visit{{UserID: 1, Name: "Аня", Seconds: 90, Present: true}, {UserID: 3, Name: "Дима", Seconds: 420}}}
	text, k := b.Render(d)
	if !strings.Contains(text, "Аня — в курилке") || !strings.Contains(text, "Никита — спускается") || !strings.Contains(text, "Уже ушли:\nДима — 7 мин") || strings.Contains(text, "Аня — 1") {
		t.Fatal(text)
	}
	for _, row := range k.Rows {
		for _, button := range row {
			if button.WebApp != nil {
				t.Fatal("open button attached to message")
			}
		}
	}
	if k.Rows[0][0].CallbackData != "join:"+d.Job.SessionID {
		t.Fatal("join not tied to session")
	}
	for _, row := range k.Rows {
		for _, button := range row {
			if len(button.CallbackData) > 64 {
				t.Fatal("callback exceeds Telegram limit")
			}
		}
	}
	d.Closed = true
	d.Duration = 3900
	text, k = b.Render(d)
	if !strings.Contains(text, "Сеанс завершен") || !strings.Contains(text, "Длился 1 ч 5 мин") || !strings.Contains(text, "Кто был:\nАня — 1 мин\nДима — 7 мин") || strings.Contains(text, "спускается") {
		t.Fatal(text)
	}
	for _, row := range k.Rows {
		for _, button := range row {
			if button.CallbackData != "" {
				t.Fatal("closed session retained callback")
			}
		}
	}
	d.Revoked = true
	text, _ = b.Render(d)
	if strings.Contains(text, "Аня") {
		t.Fatal("revoked card leaks participants")
	}
	d.Job.Kind = "reminder"
	d.Job.Token = strings.Repeat("b", 24)
	_, k = b.Render(d)
	if k.Rows[0][0].CallbackData != "yes:"+d.Job.Token || k.Rows[0][1].CallbackData != "no:"+d.Job.Token {
		t.Fatal("reminder not tied to token")
	}
}
