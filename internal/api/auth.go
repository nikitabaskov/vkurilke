package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"vkurilke/internal/store"
)

// ValidateInitData implements Telegram's bot-token HMAC validation (not the Ed25519 variant).
func ValidateInitData(raw, botToken string, now time.Time) (store.User, error) {
	var user store.User
	if raw == "" || len(raw) > 16384 {
		return user, store.ErrUnauthenticated
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return user, store.ErrUnauthenticated
	}
	parts := make([]string, 0, len(values))
	for key, v := range values {
		if len(v) != 1 {
			return user, store.ErrUnauthenticated
		}
		if key != "hash" {
			parts = append(parts, key+"="+v[0])
		}
	}
	sort.Strings(parts)
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(botToken))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(parts, "\n")))
	received, err := hex.DecodeString(values.Get("hash"))
	if err != nil || !hmac.Equal(received, mac.Sum(nil)) {
		return user, store.ErrUnauthenticated
	}
	date, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || date < now.Unix()-3600 || date > now.Unix()+30 {
		return user, store.ErrUnauthenticated
	}
	if err = json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 || user.FirstName == "" {
		return user, store.ErrUnauthenticated
	}
	if user.PhotoURL != "" {
		u, err := url.Parse(user.PhotoURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			user.PhotoURL = ""
		}
	}
	user.BotStarted = false // This is our server-side property, never part of Telegram identity.
	return user, nil
}
