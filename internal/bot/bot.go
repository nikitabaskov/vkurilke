package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"vkurilke/internal/store"
)

type Bot struct {
	Client                      *Client
	Store                       *store.Store
	BaseURL                     string
	Username                    string
	CheckMinutes, AnswerMinutes int
}
type update struct {
	ID       int64     `json:"update_id"`
	Message  *message  `json:"message"`
	Callback *callback `json:"callback_query"`
}
type message struct {
	ID   int64      `json:"message_id"`
	From store.User `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text string `json:"text"`
}
type callback struct {
	ID      string     `json:"id"`
	From    store.User `json:"from"`
	Data    string     `json:"data"`
	Message *message   `json:"message"`
}

func (b *Bot) Setup(ctx context.Context) error {
	var me struct {
		Username string `json:"username"`
	}
	if err := b.Client.Call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return err
	}
	b.Username = me.Username
	// getUpdates requires no webhook. Preserve pending updates during deployment.
	if err := b.Client.Call(ctx, "deleteWebhook", map[string]bool{"drop_pending_updates": false}, nil); err != nil {
		return err
	}
	if err := b.Client.Call(ctx, "setChatMenuButton", map[string]any{"menu_button": map[string]any{"type": "web_app", "text": "ВКурилке 🚬", "web_app": WebApp{URL: b.BaseURL}}}, nil); err != nil {
		return err
	}
	return b.Client.Call(ctx, "setMyCommands", map[string]any{"commands": []map[string]string{{"command": "start", "description": "Начать / вступить по приглашению"}, {"command": "toggle_notify", "description": "Включить или выключить уведомления"}, {"command": "id", "description": "Мой Telegram ID"}}}, nil)
}
func (b *Bot) Run(ctx context.Context) {
	for ctx.Err() == nil {
		offset, err := b.Store.BotOffset(ctx)
		if err != nil {
			slog.Error("read bot offset", "error", err)
			pause(ctx, time.Second)
			continue
		}
		var updates []update
		err = b.Client.Call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"}}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("poll Telegram", "error", err)
			pause(ctx, 3*time.Second)
			continue
		}
		for _, u := range updates {
			if err = b.handle(ctx, u); err != nil {
				slog.Warn("handle Telegram update", "update_id", u.ID, "error", err)
				if retryUpdate(err) {
					pause(ctx, 3*time.Second)
					break
				}
			}
			// Callback/status operations are idempotent; never discard the persisted offset on restart.
			if err = b.Store.SetBotOffset(ctx, u.ID+1); err != nil {
				slog.Error("save bot offset", "error", err)
				pause(ctx, time.Second)
				break
			}
		}
	}
}
func (b *Bot) handle(ctx context.Context, u update) error {
	if c := u.Callback; c != nil {
		if c.Message == nil || c.Message.Chat.Type != "private" || c.Message.Chat.ID != c.From.ID {
			return b.answer(ctx, c.ID, "Кнопка доступна в личной переписке с ботом")
		}
		if err := b.Store.UpsertUser(ctx, c.From, true); err != nil {
			return err
		}
		parts := strings.SplitN(c.Data, ":", 2)
		if len(parts) != 2 {
			return b.answer(ctx, c.ID, "Кнопка больше не доступна")
		}
		var err error
		answer := "Готово 🚬"
		switch parts[0] {
		case "join":
			err = b.Store.JoinSession(ctx, c.From.ID, parts[1])
			answer = "Ждем через 5 минут 🚬"
		case "yes":
			err = b.Store.Confirm(ctx, c.From.ID, parts[1], true)
			answer = "Отлично, статус продлен 🚬"
		case "no":
			err = b.Store.Confirm(ctx, c.From.ID, parts[1], false)
			answer = "Отметили, что ты ушел"
		case "like", "dislike":
			value := 1
			if parts[0] == "dislike" {
				value = -1
			}
			err = b.Store.RateSession(ctx, c.From.ID, parts[1], value)
			answer = "Оценка сохранена. Можешь изменить ее кнопками под сеансом"
		default:
			err = store.ErrStale
		}
		if err != nil {
			answer = publicError(err)
		}
		if answerErr := b.answer(ctx, c.ID, answer); answerErr != nil {
			return answerErr
		}
		if (parts[0] == "yes" || parts[0] == "no") && (err == nil || errors.Is(err, store.ErrStale)) {
			return b.Client.Edit(ctx, c.From.ID, c.Message.ID, answer, Keyboard{})
		}
		return err
	}
	m := u.Message
	if m == nil || m.Chat.Type != "private" || m.Chat.ID != m.From.ID {
		return nil
	}
	if err := b.Store.UpsertUser(ctx, m.From, true); err != nil {
		return err
	}
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		return nil
	}
	command := strings.SplitN(fields[0], "@", 2)[0]
	var text string
	switch command {
	case "/start":
		text = "ВКурилке 🚬\n\nВключи статус, когда будешь на месте. Здесь появится карточка сеанса: кто пришел и сколько просидел.\n\nПриложение открывается кнопкой «ВКурилке 🚬» у поля ввода.\n\nДля входа в комнату нужна ссылка-приглашение или код от ее администратора."
		if len(fields) > 1 && fields[1] != "app" {
			g, err := b.Store.JoinGroup(ctx, m.From.ID, fields[1])
			if err != nil {
				text = publicError(err)
			} else {
				text = "Ты в комнате «" + g.Name + "» 🚬\nОткрывай приложение кнопкой «ВКурилке 🚬» у поля ввода и отмечайся, когда будешь на месте."
			}
		}
		if b.Store.IsAdmin(m.From.ID) {
			text += "\n\nТы администратор приложения: первую комнату можно создать в Mini App."
		}
	case "/toggle_notify":
		p, err := b.Store.ToggleNotificationsForUpdate(ctx, m.From.ID, u.ID)
		if err != nil {
			return err
		}
		text = "Карточки сеансов выключены. Проверки присутствия остаются."
		if p.Session {
			text = "Карточки сеансов включены: одно сообщение на сеанс, которое обновляется."
		}
	case "/id":
		text = "Твой Telegram ID: " + strconv.FormatInt(m.From.ID, 10)
	default:
		if len(fields) == 1 && len(fields[0]) == 24 {
			g, err := b.Store.JoinGroup(ctx, m.From.ID, fields[0])
			if err != nil {
				text = publicError(err)
			} else {
				text = "Ты в комнате «" + g.Name + "» 🚬"
			}
		} else {
			text = "Открой приложение кнопкой «ВКурилке 🚬» у поля ввода или пришли код приглашения. Команда /id покажет твой Telegram ID."
		}
	}
	_, err := b.Client.Send(ctx, m.From.ID, text, Keyboard{})
	return err
}
func (b *Bot) answer(ctx context.Context, id, text string) error {
	return b.Client.Call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}
func publicError(err error) string {
	for _, known := range []error{store.ErrForbidden, store.ErrInvalid, store.ErrStale, store.ErrBotUnavailable} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		return "Не найдено. Проверь код приглашения."
	}
	return "Не получилось. Попробуй еще раз."
}
func retryUpdate(err error) bool {
	for _, known := range []error{store.ErrForbidden, store.ErrInvalid, store.ErrNotFound, store.ErrStale, store.ErrBotUnavailable} {
		if errors.Is(err, known) {
			return false
		}
	}
	var telegramErr *APIError
	if errors.As(err, &telegramErr) && (telegramErr.Code == 400 || telegramErr.Code == 403) {
		return false
	}
	return true
}
func pause(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (b *Bot) Deliver(ctx context.Context, d store.Delivery) (int64, error) {
	text, keyboard := b.Render(d)
	if d.MessageID > 0 {
		return d.MessageID, b.Client.Edit(ctx, d.Job.UserID, d.MessageID, text, keyboard)
	}
	return b.Client.Send(ctx, d.Job.UserID, text, keyboard)
}
func (b *Bot) Render(d store.Delivery) (string, Keyboard) {
	if d.Job.Kind == "reminder" {
		k := Keyboard{Rows: [][]Button{{{Text: "Да 🚬", CallbackData: "yes:" + d.Job.Token}, {Text: "Уже ушел", CallbackData: "no:" + d.Job.Token}}}}
		return fmt.Sprintf("Все еще в курилке? 🚬\n\n«%s»\nЕсли не ответишь за %d мин, выключим твой статус.", d.GroupName, b.AnswerMinutes), k
	}
	if d.Revoked {
		return "Ты больше не участник комнаты «" + d.GroupName + "».", Keyboard{}
	}
	text := "🟢🟢🟢 СЕАНС ИДЁТ 🟢🟢🟢\n🚬 «" + d.GroupName + "»\n" + sessionTime(d.StartedAt, d.EndedAt) + "\n━━━━━━━━━━━━━━━━━━\n\n"
	if d.Closed {
		text = "Сеанс завершен · «" + d.GroupName + "»\n" + sessionTime(d.StartedAt, d.EndedAt) + "\nДлился " + duration(d.Duration) + "\n\n"
	}
	full := false
	add := func(line string) {
		if full {
			return
		}
		if utf8.RuneCountInString(text+line) > 3500 {
			text += "Остальные участники — в Mini App\n"
			full = true
			return
		}
		text += line
	}
	present := map[int64]bool{}
	for _, m := range d.Members {
		if d.Closed {
			break
		}
		switch m.Status {
		case "smoking":
			add("🚬 " + m.FirstName + " — в курилке\n")
		case "going":
			add("🏃 " + m.FirstName + " — спускается\n")
		default:
			continue
		}
		present[m.ID] = true
	}
	heading := "\nУже ушли:\n"
	if d.Closed {
		heading = fmt.Sprintf("Было %d человек:\n", len(d.Visits))
	}
	for _, v := range d.Visits {
		if present[v.UserID] {
			continue
		}
		if heading != "" {
			add(heading)
			heading = ""
		}
		add(v.Name + " — " + duration(v.Seconds) + "\n")
	}
	if d.Closed {
		// Reserve the footer even when the participant list is truncated.
		text += fmt.Sprintf("\nОценки сеанса: 👍 %d · 👎 %d", d.Likes, d.Dislikes)
		k := Keyboard{}
		if d.CanRate {
			text += "\nКак тебе этот сеанс?"
			like, dislike := "👍 Понравилось", "👎 Не понравилось"
			if d.Rating == 1 {
				like += " ✓"
			}
			if d.Rating == -1 {
				dislike += " ✓"
			}
			k.Rows = [][]Button{{{Text: like, CallbackData: "like:" + d.Job.SessionID}, {Text: dislike, CallbackData: "dislike:" + d.Job.SessionID}}}
		}
		return strings.TrimSpace(text), k
	}
	if len(present) == 0 {
		add("Все вернулись.\n")
	}
	return strings.TrimSpace(text), Keyboard{Rows: [][]Button{{{Text: "🏃 Спускаюсь (+1)", CallbackData: "join:" + d.Job.SessionID}}}}
}
func sessionTime(start, end int64) string {
	format := func(timestamp int64) string {
		return time.Unix(timestamp, 0).In(store.ReportLocation).Format("02.01.2006 15:04")
	}
	if end == 0 {
		return "С " + format(start) + " — сейчас (Красноярск)"
	}
	return "С " + format(start) + " до " + format(end) + " (Красноярск)"
}
func duration(seconds int64) string {
	minutes := seconds / 60
	switch {
	case minutes < 1:
		return "меньше минуты"
	case minutes < 60:
		return fmt.Sprintf("%d мин", minutes)
	}
	return fmt.Sprintf("%d ч %d мин", minutes/60, minutes%60)
}
