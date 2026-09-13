package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRatingCallbackUpdatesCompletedSession(t *testing.T) {
	now := time.Unix(1700000000, 0)
	s, err := store.Open(filepath.Join(t.TempDir(), "bot.db"), store.Options{AdminIDs: []int64{1}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := store.User{ID: 1, FirstName: "Аня"}
	if err = s.UpsertUser(ctx, user, true); err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateGroup(ctx, 1, "Комната")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeStatus(ctx, 1, g.ID, "smoking", 0); err != nil {
		t.Fatal(err)
	}
	j, err := s.NextJob(ctx)
	if err != nil || j == nil {
		t.Fatalf("job: %+v %v", j, err)
	}
	if err = s.CompleteJob(ctx, *j, 42, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err = s.ChangeStatus(ctx, 1, g.ID, "idle", 0); err != nil {
		t.Fatal(err)
	}
	c := NewClient("test-token")
	var answers int
	c.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		answers++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`)), Header: make(http.Header)}, nil
	})
	b := &Bot{Store: s, Client: c}
	m := &message{ID: 42}
	m.Chat.ID, m.Chat.Type = 1, "private"
	for _, action := range []string{"like", "like", "dislike"} {
		if err = b.handle(ctx, update{Callback: &callback{ID: "callback", From: user, Message: m, Data: action + ":" + j.SessionID}}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := s.Prepare(ctx, *j)
	if err != nil {
		t.Fatal(err)
	}
	if answers != 3 || d.Rating != -1 || d.Likes != 0 || d.Dislikes != 1 {
		t.Fatalf("callback result: %+v, answers %d", d, answers)
	}
}
func TestSessionCardsCloseAndCallbacksFitTelegram(t *testing.T) {
	b := &Bot{BaseURL: "https://smoke.test", AnswerMinutes: 3}
	d := store.Delivery{Job: store.Job{Kind: "card", SessionID: strings.Repeat("a", 24), GroupID: "room"}, GroupName: "Общага", Members: []store.Member{{User: store.User{ID: 1, FirstName: "Аня"}, Status: "smoking"}, {User: store.User{ID: 2, FirstName: "Никита"}, Status: "going"}},
		Visits: []store.Visit{{UserID: 1, Name: "Аня", Seconds: 90, Present: true}, {UserID: 3, Name: "Дима", Seconds: 420}}}
	text, k := b.Render(d)
	if !strings.Contains(text, "🟢🟢🟢 СЕАНС ИДЁТ") || !strings.Contains(text, "— сейчас") {
		t.Fatal(text)
	}
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
	d.StartedAt = time.Date(2026, 9, 13, 23, 0, 0, 0, store.ReportLocation).Unix()
	d.EndedAt = d.StartedAt + d.Duration
	d.CanRate, d.Rating, d.Likes, d.Dislikes = true, 1, 2, 1
	text, k = b.Render(d)
	if !strings.Contains(text, "Сеанс завершен") || !strings.Contains(text, "Длился 1 ч 5 мин") || !strings.Contains(text, "Было 2 человек:\nАня — 1 мин\nДима — 7 мин") || strings.Contains(text, "спускается") || strings.Contains(text, "СЕАНС ИДЁТ") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "С 13.09.2026 23:00 до 14.09.2026 00:05") || !strings.Contains(text, "👍 2 · 👎 1") {
		t.Fatal(text)
	}
	if len(k.Rows) != 1 || len(k.Rows[0]) != 2 || !strings.Contains(k.Rows[0][0].Text, "✓") {
		t.Fatalf("rating keyboard: %+v", k)
	}
	for _, row := range k.Rows {
		for _, button := range row {
			if len(button.CallbackData) > 64 || (button.CallbackData != "like:"+d.Job.SessionID && button.CallbackData != "dislike:"+d.Job.SessionID) {
				t.Fatal("closed session has wrong callback")
			}
		}
	}
	d.CanRate = false
	_, k = b.Render(d)
	if len(k.Rows) != 0 {
		t.Fatal("nonparticipant can rate")
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
