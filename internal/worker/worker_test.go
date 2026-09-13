package worker

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vkurilke/internal/bot"
	"vkurilke/internal/store"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (fn transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestDeliveryRetriesAndThenEditsExistingCard(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	s, err := store.Open(filepath.Join(t.TempDir(), "worker.db"), store.Options{AdminIDs: []int64{1}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(s.UpsertUser(ctx, store.User{ID: 1, FirstName: "Никита"}, true))
	check(s.UpsertUser(ctx, store.User{ID: 2, FirstName: "Аня"}, true))
	g, err := s.CreateGroup(ctx, 1, "Общага")
	check(err)
	_, err = s.JoinGroup(ctx, 2, g.InviteCode)
	check(err)
	var paths []string
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		body := `{"ok":true,"result":{"message_id":42}}`
		if len(paths) == 1 {
			body = `{"ok":false,"error_code":500,"description":"Temporary failure"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	w := &Worker{Store: s, Bot: &bot.Bot{Store: s, Client: bot.NewClient("test"), BaseURL: "https://smoke.test", AnswerMinutes: 3}}
	check(s.ChangeStatus(ctx, 1, g.ID, "smoking", 0))
	check(w.DeliverOne(ctx))
	j, err := s.NextJob(ctx)
	check(err)
	if j != nil {
		t.Fatal("failed job retried without backoff")
	}
	now = now.Add(2 * time.Second)
	check(w.DeliverOne(ctx))
	if len(paths) != 2 {
		t.Fatal("queued delivery not retried")
	}
	check(s.ChangeStatus(ctx, 2, g.ID, "smoking", 0))
	now = now.Add(2 * time.Second)
	check(w.DeliverOne(ctx))
	check(w.DeliverOne(ctx))
	// The joiner's card is edited; the founder gets a first card now that someone joined.
	if len(paths) != 4 || strings.Count(strings.Join(paths[2:], " "), "editMessageText") != 1 {
		t.Fatalf("card was duplicated instead of edited: %v", paths)
	}
	j, err = s.NextJob(ctx)
	check(err)
	if j != nil {
		t.Fatal("successful delivery remained in queue")
	}
}
