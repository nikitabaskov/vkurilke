package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BotToken, Port, BaseURL, DBPath string
	AdminIDs                        []int64
	CheckInterval, AnswerTimeout    time.Duration
}

func Load() (Config, error) {
	c := Config{BotToken: os.Getenv("BOT_TOKEN"), Port: value("PORT", "8080"), BaseURL: strings.TrimRight(os.Getenv("BASE_URL"), "/"), DBPath: value("DB_PATH", "data/vkurilke.db")}
	if c.BotToken == "" {
		return c, fmt.Errorf("BOT_TOKEN is required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return c, fmt.Errorf("BASE_URL must be an HTTPS origin, e.g. https://smoke.example.com")
	}
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return c, fmt.Errorf("PORT must be 1..65535")
	}
	for _, part := range strings.Split(os.Getenv("ADMIN_IDS"), ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return c, fmt.Errorf("ADMIN_IDS must contain positive Telegram user IDs separated by commas")
		}
		c.AdminIDs = append(c.AdminIDs, id)
	}
	if c.CheckInterval, err = minutes("CHECK_INTERVAL_MINUTES", 15); err != nil {
		return c, err
	}
	if c.AnswerTimeout, err = minutes("ANSWER_TIMEOUT_MINUTES", 3); err != nil {
		return c, err
	}
	return c, nil
}

func value(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func minutes(key string, fallback int) (time.Duration, error) {
	n, err := strconv.Atoi(value(key, strconv.Itoa(fallback)))
	if err != nil || n < 1 || n > 1440 {
		return 0, fmt.Errorf("%s must be 1..1440", key)
	}
	return time.Duration(n) * time.Minute, nil
}
