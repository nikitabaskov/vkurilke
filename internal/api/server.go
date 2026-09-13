package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"vkurilke/internal/store"
)

type Server struct {
	Store                          *store.Store
	BotToken, BaseURL, BotUsername string
	CheckMinutes, AnswerMinutes    int
	Now                            func() time.Time
}
type userKey struct{}

func userID(r *http.Request) int64 { return r.Context().Value(userKey{}).(int64) }

func (s *Server) Handler(static http.Handler) http.Handler {
	if s.Now == nil {
		s.Now = time.Now
	}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Cache-Control", "no-store")
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.Store.Ping(ctx); err != nil {
			http.Error(w, "unhealthy", 503)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	r.Route("/api", func(r chi.Router) {
		r.Use(middleware.Timeout(15 * time.Second))
		r.Use(middleware.Throttle(64))
		r.Use(s.sameOrigin)
		r.Get("/meta", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]any{"bot_username": s.BotUsername, "check_minutes": s.CheckMinutes, "answer_minutes": s.AnswerMinutes})
		})
		r.With(authLimit()).Post("/auth", s.auth)
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate)
			r.Get("/me", s.me)
			r.Get("/me/statistics", func(w http.ResponseWriter, r *http.Request) {
				stats, err := s.Store.Statistics(r.Context(), userID(r), "", r.URL.Query().Get("period"))
				respond(w, stats, err)
			})
			r.Get("/groups/{group}/statistics", func(w http.ResponseWriter, r *http.Request) {
				stats, err := s.Store.Statistics(r.Context(), userID(r), chi.URLParam(r, "group"), r.URL.Query().Get("period"))
				respond(w, stats, err)
			})
			r.Get("/groups", func(w http.ResponseWriter, r *http.Request) {
				g, err := s.Store.Groups(r.Context(), userID(r))
				respond(w, g, err)
			})
			r.Post("/groups", func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Name string `json:"name"`
				}
				if !decode(w, r, &p) {
					return
				}
				g, err := s.Store.CreateGroup(r.Context(), userID(r), p.Name)
				respond(w, g, err)
			})
			r.Post("/groups/join", func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Code string `json:"code"`
				}
				if !decode(w, r, &p) {
					return
				}
				g, err := s.Store.JoinGroup(r.Context(), userID(r), strings.TrimSpace(p.Code))
				respond(w, g, err)
			})
			r.Post("/groups/{group}/invite", func(w http.ResponseWriter, r *http.Request) {
				code, err := s.Store.RotateInvite(r.Context(), userID(r), chi.URLParam(r, "group"))
				respond(w, map[string]string{"invite_code": code}, err)
			})
			r.Post("/groups/{group}/members", s.manageMember)
			r.Delete("/groups/{group}/membership", func(w http.ResponseWriter, r *http.Request) {
				respond(w, map[string]bool{"ok": true}, s.Store.LeaveGroup(r.Context(), userID(r), chi.URLParam(r, "group")))
			})
			r.Get("/status", func(w http.ResponseWriter, r *http.Request) {
				state, err := s.Store.Snapshot(r.Context(), userID(r), r.URL.Query().Get("group_id"))
				respond(w, state, err)
			})
			r.Post("/status/change", s.changeStatus)
			r.Post("/status/confirm", func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Token string `json:"token"`
					Here  *bool  `json:"here"`
				}
				if !decode(w, r, &p) {
					return
				}
				if p.Here == nil {
					respond(w, nil, store.ErrInvalid)
					return
				}
				respond(w, map[string]bool{"ok": true}, s.Store.Confirm(r.Context(), userID(r), p.Token, *p.Here))
			})
			r.Put("/settings/notifications", func(w http.ResponseWriter, r *http.Request) {
				var p struct{ Session, Arrival, Departure *bool }
				if !decode(w, r, &p) {
					return
				}
				if p.Session == nil || p.Arrival == nil || p.Departure == nil {
					respond(w, nil, store.ErrInvalid)
					return
				}
				prefs := store.Preferences{Session: *p.Session, Arrival: *p.Arrival, Departure: *p.Departure}
				respond(w, prefs, s.Store.SetPreferences(r.Context(), userID(r), prefs))
			})
			r.Post("/settings/toggle-notifications", func(w http.ResponseWriter, r *http.Request) {
				p, err := s.Store.ToggleNotifications(r.Context(), userID(r))
				respond(w, p, err)
			})
		})
	})
	r.Handle("/*", static)
	return r
}
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.BaseURL {
			respond(w, nil, store.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			respond(w, nil, store.ErrUnauthenticated)
			return
		}
		id, err := s.Store.Authenticate(r.Context(), strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			respond(w, nil, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, id)))
	})
}
func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	var p struct {
		InitData string `json:"init_data"`
	}
	if !decode(w, r, &p) {
		return
	}
	u, err := ValidateInitData(p.InitData, s.BotToken, s.Now())
	if err != nil {
		respond(w, nil, err)
		return
	}
	if err = s.Store.UpsertUser(r.Context(), u, false); err != nil {
		respond(w, nil, err)
		return
	}
	token, err := s.Store.NewAuthSession(r.Context(), u.ID)
	respond(w, map[string]string{"token": token}, err)
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id := userID(r)
	u, err := s.Store.User(r.Context(), id)
	if err != nil {
		respond(w, nil, err)
		return
	}
	p, err := s.Store.Preferences(r.Context(), id)
	respond(w, map[string]any{"user": u, "preferences": p, "is_admin": s.Store.IsAdmin(id)}, err)
}
func (s *Server) changeStatus(w http.ResponseWriter, r *http.Request) {
	var p struct {
		GroupID  string `json:"group_id"`
		Status   string `json:"status"`
		Duration int    `json:"duration_minutes"`
	}
	if !decode(w, r, &p) {
		return
	}
	if p.Duration == 0 {
		p.Duration = 5
	}
	respond(w, map[string]bool{"ok": true}, s.Store.ChangeStatus(r.Context(), userID(r), p.GroupID, p.Status, p.Duration))
}
func (s *Server) manageMember(w http.ResponseWriter, r *http.Request) {
	var p struct {
		UserID int64  `json:"user_id"`
		Action string `json:"action"`
	}
	if !decode(w, r, &p) {
		return
	}
	respond(w, map[string]bool{"ok": true}, s.Store.ManageMember(r.Context(), userID(r), chi.URLParam(r, "group"), p.UserID, p.Action))
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 20<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		respond(w, nil, store.ErrInvalid)
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		respond(w, nil, store.ErrInvalid)
		return false
	}
	return true
}
func respond(w http.ResponseWriter, data any, err error) {
	if err == nil {
		jsonResponse(w, 200, data)
		return
	}
	status := 500
	message := "Не удалось выполнить запрос. Попробуйте еще раз."
	switch {
	case errors.Is(err, store.ErrInvalid):
		status = 400
		message = err.Error()
	case errors.Is(err, store.ErrUnauthenticated):
		status = 401
		message = err.Error()
	case errors.Is(err, store.ErrForbidden):
		status = 403
		message = err.Error()
	case errors.Is(err, store.ErrNotFound):
		status = 404
		message = "Не найдено. Проверьте приглашение или ID пользователя."
	case errors.Is(err, store.ErrBotUnavailable):
		status = 409
		message = err.Error()
	case errors.Is(err, store.ErrStale):
		status = 409
		message = err.Error()
	default:
		slog.Error("API request failed", "error", err)
	}
	jsonResponse(w, status, map[string]string{"error": message})
}
func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// Global bounded auth budget avoids storing untrusted IPs or trusting proxy headers.
func authLimit() func(http.Handler) http.Handler {
	var mu sync.Mutex
	tokens := 120
	last := time.Now()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			now := time.Now()
			refill := int(now.Sub(last)/time.Second) * 20
			if refill > 0 {
				tokens = min(120, tokens+refill)
				last = now
			}
			allowed := tokens > 0
			if allowed {
				tokens--
			}
			mu.Unlock()
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(1))
				jsonResponse(w, 429, map[string]string{"error": "Слишком много запросов. Попробуйте через секунду."})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
