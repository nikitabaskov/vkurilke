package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"vkurilke/internal/store"
)

func signedData(token string, id int64, at int64) string {
	user := `{"id":` + strconv.FormatInt(id, 10) + `,"first_name":"Аня & Дима","username":"test_user"}`
	date := strconv.FormatInt(at, 10)
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte("auth_date=" + date + "\nquery_id=test-query\nuser=" + user))
	return url.Values{"auth_date": {date}, "query_id": {"test-query"}, "user": {user}, "hash": {hex.EncodeToString(mac.Sum(nil))}}.Encode()
}
func TestInitDataIntegrityAndFreshness(t *testing.T) {
	now := time.Unix(1700000000, 0)
	token := "123:secret"
	raw := signedData(token, 12345, now.Unix())
	u, err := ValidateInitData(raw, token, now)
	if err != nil || u.ID != 12345 || u.FirstName != "Аня & Дима" {
		t.Fatalf("valid init data rejected: %+v %v", u, err)
	}
	cases := map[string]string{"empty": "", "wrong bot": signedData("other", 12345, now.Unix()), "expired": signedData(token, 12345, now.Unix()-3601), "future": signedData(token, 12345, now.Unix()+31), "duplicate": raw + "&auth_date=1700000000", "negative user": signedData(token, -1, now.Unix()), "malformed": raw + "&bad=%xx"}
	values, _ := url.ParseQuery(raw)
	values.Set("user", `{"id":999,"first_name":"Forged"}`)
	cases["forged user"] = values.Encode()
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateInitData(input, token, now); !errors.Is(err, store.ErrUnauthenticated) {
				t.Fatalf("accepted invalid init data: %v", err)
			}
		})
	}
}
func TestHTTPAuthRoomIsolationAndStatusFlow(t *testing.T) {
	now := time.Unix(1700000000, 0)
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "api.db"), store.Options{AdminIDs: []int64{1}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := &Server{Store: s, BotToken: "test-token", BaseURL: "https://smoke.test", Now: func() time.Time { return now }}
	h := a.Handler(http.NotFoundHandler())
	request := func(method, path, token string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	auth := func(id int64) string {
		t.Helper()
		w := request("POST", "/api/auth", "", map[string]string{"init_data": signedData(a.BotToken, id, now.Unix())})
		if w.Code != 200 {
			t.Fatalf("auth: %d %s", w.Code, w.Body)
		}
		var result map[string]string
		json.Unmarshal(w.Body.Bytes(), &result)
		return result["token"]
	}
	token := auth(1)
	outsider := auth(2)
	if w := request("GET", "/api/groups", "", nil); w.Code != 401 {
		t.Fatalf("anonymous rooms: %d", w.Code)
	}
	if w := request("POST", "/api/groups", outsider, map[string]string{"name": "Denied"}); w.Code != 403 {
		t.Fatalf("unauthorized room create: %d", w.Code)
	}
	w := request("POST", "/api/groups", token, map[string]string{"name": "Общага"})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var group store.Group
	json.Unmarshal(w.Body.Bytes(), &group)
	if w = request("GET", "/api/status?group_id="+group.ID, outsider, nil); w.Code != 403 {
		t.Fatal("nonmember read roster")
	}
	if w = request("POST", "/api/status/change", token, map[string]string{"group_id": group.ID, "status": "smoking"}); w.Code != 409 {
		t.Fatal("presence enabled without starting bot")
	}
	if err = s.UpsertUser(ctx, store.User{ID: 1, FirstName: "Аня"}, true); err != nil {
		t.Fatal(err)
	}
	if w = request("POST", "/api/status/change", token, map[string]string{"group_id": group.ID, "status": "smoking"}); w.Code != 200 {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if w = request("POST", "/api/status/change", token, map[string]any{"group_id": group.ID, "status": "smoking", "user_id": 2}); w.Code != 400 {
		t.Fatal("accepted injected user_id")
	}
	if w = request("POST", "/api/status/change", token, map[string]any{"group_id": group.ID, "status": "going", "duration_minutes": 999}); w.Code != 400 {
		t.Fatal("accepted arbitrary duration")
	}
	if w = request("PUT", "/api/settings/notifications", token, map[string]bool{"session": false}); w.Code != 400 {
		t.Fatal("partial preferences silently reset fields")
	}
	if w = request("PUT", "/api/settings/notifications", token, map[string]bool{"session": false, "arrival": true, "departure": true}); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("POST", "/api/groups", bytes.NewBufferString(`{"name":"Foreign"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Origin", "https://evil.test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("accepted foreign origin")
	}
	now = now.Add(time.Minute)
	for _, tc := range []struct {
		path, auth string
		code       int
	}{
		{"/api/me/statistics?period=all", "", 401},
		{"/api/groups/" + group.ID + "/statistics?period=all", outsider, 403},
		{"/api/groups/" + group.ID + "/statistics?period=year", token, 400},
		{"/api/me/statistics?period=all", token, 200},
		{"/api/me/statistics?period=all", outsider, 200},
		{"/api/groups/" + group.ID + "/statistics?period=all", token, 200},
	} {
		w = request("GET", tc.path, tc.auth, nil)
		if w.Code != tc.code {
			t.Fatalf("%s: got %d, want %d: %s", tc.path, w.Code, tc.code, w.Body)
		}
		if tc.code == 200 {
			var stats store.Statistics
			if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
				t.Fatal(err)
			}
			want := store.SmokingTotals{Outings: 1, Seconds: 60}
			if tc.auth == outsider {
				want = store.SmokingTotals{}
			}
			if stats.Totals != want {
				t.Fatalf("wrong statistics or leaked another user's data: %+v", stats)
			}
		}
	}
	now = now.Add(24 * time.Hour)
	if w = request("GET", "/api/me", token, nil); w.Code != 401 {
		t.Fatal("accepted expired app token")
	}
}
