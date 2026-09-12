package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"vkurilke/internal/api"
	"vkurilke/internal/bot"
	"vkurilke/internal/config"
	"vkurilke/internal/store"
	"vkurilke/internal/worker"
	"vkurilke/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	db, err := store.Open(c.DBPath, store.Options{AdminIDs: c.AdminIDs, CheckInterval: c.CheckInterval, AnswerTimeout: c.AnswerTimeout})
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	b := &bot.Bot{Client: bot.NewClient(c.BotToken), Store: db, BaseURL: c.BaseURL, CheckMinutes: int(c.CheckInterval / time.Minute), AnswerMinutes: int(c.AnswerTimeout / time.Minute)}
	setupCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	err = b.Setup(setupCtx)
	cancel()
	if err != nil {
		return err
	}
	a := &api.Server{Store: db, BotToken: c.BotToken, BaseURL: c.BaseURL, BotUsername: b.Username, CheckMinutes: b.CheckMinutes, AnswerMinutes: b.AnswerMinutes}
	server := &http.Server{Addr: ":" + c.Port, Handler: a.Handler(web.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	w := &worker.Worker{Store: db, Bot: b}
	var wg sync.WaitGroup
	for _, fn := range []func(context.Context){b.Run, w.RunTimers, w.RunDelivery} {
		wg.Add(1)
		go func(fn func(context.Context)) { defer wg.Done(); fn(ctx) }(fn)
	}
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("ВКурилке started", "port", c.Port, "bot", b.Username)
		serverErr <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err = <-serverErr:
		stop()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	wg.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return shutdownErr
}
