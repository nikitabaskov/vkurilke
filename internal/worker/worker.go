package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"vkurilke/internal/bot"
	"vkurilke/internal/store"
)

type Worker struct {
	Store *store.Store
	Bot   *bot.Bot
}

func (w *Worker) RunTimers(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		if err := w.Store.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Error("process timers", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
func (w *Worker) RunDelivery(ctx context.Context) {
	t := time.NewTicker(60 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := w.DeliverOne(ctx); err != nil && ctx.Err() == nil {
			slog.Error("delivery worker", "error", err)
		}
	}
}
func (w *Worker) DeliverOne(ctx context.Context) error {
	j, err := w.Store.NextJob(ctx)
	if err != nil || j == nil {
		return err
	}
	d, err := w.Store.Prepare(ctx, *j)
	if err != nil {
		return err
	}
	if d.Skip {
		return w.Store.CompleteJob(ctx, *j, 0, false)
	}
	id, err := w.Bot.Deliver(ctx, d)
	if err == nil {
		return w.Store.CompleteJob(ctx, *j, id, true)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var apiErr *bot.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code == 403 || (apiErr.Code == 400 && strings.Contains(apiErr.Description, "chat not found")) {
			return w.Store.BlockBot(ctx, j.UserID)
		}
		if apiErr.Code == 400 && j.Kind == "card" {
			// A deleted/uneditable card should not poison the delivery queue.
			slog.Warn("session card unavailable", "job_id", j.ID, "error", err)
			return w.Store.CompleteJob(ctx, *j, 0, false)
		}
		if apiErr.RetryAfter > 0 {
			if retryErr := w.Store.RetryJob(ctx, *j, time.Duration(apiErr.RetryAfter)*time.Second); retryErr != nil {
				return retryErr
			}
			t := time.NewTimer(time.Duration(apiErr.RetryAfter) * time.Second)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
			return nil
		}
	}
	slog.Warn("retry Telegram delivery", "job_id", j.ID, "attempt", j.Attempts+1, "error", err)
	delay := time.Duration(1<<min(j.Attempts, 8)) * time.Second
	return w.Store.RetryJob(ctx, *j, delay)
}
