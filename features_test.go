package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func testPlayer(t *testing.T, a *app) (string, string) {
	t.Helper()
	id := newTestUUID(t)
	token, hash, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(context.Background(), `INSERT INTO players(id,progress) VALUES($1,$2)`, id, json.RawMessage(sampleProgress)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(context.Background(), `INSERT INTO sessions(token_hash,player_id,expires_at) VALUES($1,$2,now()+interval '1 day')`, hash[:], id); err != nil {
		t.Fatal(err)
	}
	return id, token
}

func testCode(t *testing.T, a *app, code string, amount int64, maxUses int) {
	t.Helper()
	hash := sha256.Sum256([]byte(code))
	if _, err := a.db.Exec(context.Background(), `INSERT INTO promo_codes(id,code_hash,coins,max_redemptions,expires_at)
		VALUES($1,$2,$3,$4,now()+interval '1 day')`, newTestUUID(t), hash[:], amount, maxUses); err != nil {
		t.Fatal(err)
	}
}

func TestPromoRedemptionAndRevision(t *testing.T) {
	a, cleanup := testApp(t)
	defer cleanup()
	_, token := testPlayer(t, a)
	_, otherToken := testPlayer(t, a)
	testCode(t, a, "RDST-TESTCODE123", 50, 1)
	h := a.routes()
	request := `{"code":"rdst-testcode123"}`
	first := call(t, h, "POST", "/v1/me/redeem", token, request)
	if first.Code != 200 || !strings.Contains(first.Body.String(), `"coins":225`) || !strings.Contains(first.Body.String(), `"revision":1`) {
		t.Fatalf("redeem: %d %s", first.Code, first.Body.String())
	}
	if w := call(t, h, "POST", "/v1/me/redeem", token, request); w.Code != 409 || !strings.Contains(w.Body.String(), "code_already_redeemed") {
		t.Fatalf("duplicate credited: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, h, "POST", "/v1/me/redeem", otherToken, request); w.Code != 409 || !strings.Contains(w.Body.String(), "code_exhausted") {
		t.Fatalf("cap ignored: %d %s", w.Code, w.Body.String())
	}
	got := call(t, h, "GET", "/v1/me/progress", token, "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"coins":225`) || !strings.Contains(got.Body.String(), `"score":920`) {
		t.Fatalf("progress lost: %d %s", got.Code, got.Body.String())
	}
	stale := call(t, h, "PUT", "/v1/me/progress", token, `{"revision":0,"progress":`+sampleProgress+`,"xp":0}`)
	if stale.Code != 409 {
		t.Fatalf("stale save accepted: %d %s", stale.Code, stale.Body.String())
	}
}

func TestPromoExpiryAndMissingProgress(t *testing.T) {
	a, cleanup := testApp(t)
	defer cleanup()
	id, token := testPlayer(t, a)
	testCode(t, a, "RDST-EXPIRED123", 10, 2)
	hash := sha256.Sum256([]byte("RDST-EXPIRED123"))
	if _, err := a.db.Exec(context.Background(), `UPDATE promo_codes SET expires_at=now()-interval '1 second' WHERE code_hash=$1`, hash[:]); err != nil {
		t.Fatal(err)
	}
	h := a.routes()
	if w := call(t, h, "POST", "/v1/me/redeem", token, `{"code":"RDST-EXPIRED123"}`); w.Code != 410 {
		t.Fatalf("expired code accepted: %d", w.Code)
	}
	testCode(t, a, "RDST-NOPROGRESS", 10, 2)
	if _, err := a.db.Exec(context.Background(), `UPDATE players SET progress=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if w := call(t, h, "POST", "/v1/me/redeem", token, `{"code":"RDST-NOPROGRESS"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "progress_required") {
		t.Fatalf("missing progress accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestConcurrentCodeCap(t *testing.T) {
	a, cleanup := testApp(t)
	defer cleanup()
	_, first := testPlayer(t, a)
	_, second := testPlayer(t, a)
	testCode(t, a, "RDST-CONCURRENT", 10, 1)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, token := range []string{first, second} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			results <- call(t, a.routes(), "POST", "/v1/me/redeem", token, `{"code":"RDST-CONCURRENT"}`).Code
		}(token)
	}
	wg.Wait()
	close(results)
	var success, conflict int
	for status := range results {
		if status == 200 {
			success++
		} else if status == 409 {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("cap race: success=%d conflict=%d", success, conflict)
	}
}

func TestTelegramLinkAndCommands(t *testing.T) {
	a, cleanup := testApp(t)
	defer cleanup()
	_, token := testPlayer(t, a)
	_, otherToken := testPlayer(t, a)
	testCode(t, a, "RDST-TELEGRAM123", 25, 3)
	var messages []string
	var mu sync.Mutex
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botdummy/sendMessage" {
			t.Errorf("wrong bot route: %s", r.URL.Path)
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		messages = append(messages, body.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer bot.Close()
	a.telegramToken, a.telegramSecret, a.telegramUsername = "dummy", "test_secret_12345", "RoadsterTestBot"
	a.telegramAPIURL = bot.URL
	a.telegramClient = bot.Client()
	h := a.routes()
	link := call(t, h, "POST", "/v1/me/telegram/link", token, "")
	if link.Code != 200 {
		t.Fatalf("link creation: %d %s", link.Code, link.Body.String())
	}
	var linkBody struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(link.Body.Bytes(), &linkBody); err != nil {
		t.Fatal(err)
	}
	code := strings.Split(linkBody.URL, "start=")[1]
	webhook := func(id int, text string, secret string) int {
		t.Helper()
		body := fmt.Sprintf(`{"update_id":%d,"message":{"text":%q,"from":{"id":12345},"chat":{"id":12345,"type":"private"}}}`, id, text)
		r := httptest.NewRequest("POST", "/v1/telegram/webhook", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if status := webhook(1, "/start "+code, "wrong"); status != 401 {
		t.Fatalf("bad secret accepted: %d", status)
	}
	if status := webhook(1, "/start "+code, "test_secret_12345"); status != 204 {
		t.Fatalf("link failed: %d", status)
	}
	if w := call(t, h, "GET", "/v1/me/telegram", token, ""); !strings.Contains(w.Body.String(), `"linked":true`) {
		t.Fatalf("not linked: %s", w.Body.String())
	}
	if w := call(t, h, "DELETE", "/v1/me/telegram", otherToken, ""); w.Code != 204 {
		t.Fatalf("other player's unlink failed: %d", w.Code)
	}
	if w := call(t, h, "GET", "/v1/me/telegram", token, ""); !strings.Contains(w.Body.String(), `"linked":true`) {
		t.Fatalf("other player unlinked account: %s", w.Body.String())
	}
	if status := webhook(2, "/balance", "test_secret_12345"); status != 204 {
		t.Fatalf("balance failed: %d", status)
	}
	if status := webhook(3, "/redeem RDST-TELEGRAM123", "test_secret_12345"); status != 204 {
		t.Fatalf("redeem failed: %d", status)
	}
	if status := webhook(3, "/redeem RDST-TELEGRAM123", "test_secret_12345"); status != 204 {
		t.Fatalf("retry failed: %d", status)
	}
	if status := webhook(4, "/record", "test_secret_12345"); status != 204 {
		t.Fatalf("record failed: %d", status)
	}
	mu.Lock()
	if len(messages) != 5 || !strings.Contains(messages[1], "175") || !strings.Contains(messages[2], "200") || messages[2] != messages[3] || !strings.Contains(messages[4], "1200") {
		t.Errorf("unexpected bot replies: %v", messages)
	}
	mu.Unlock()
	got := call(t, h, "GET", "/v1/me/progress", token, "")
	if !strings.Contains(got.Body.String(), `"coins":200`) {
		t.Fatalf("telegram redemption duplicated or lost: %s", got.Body.String())
	}
	if w := call(t, h, "DELETE", "/v1/me/telegram", token, ""); w.Code != 204 {
		t.Fatalf("unlink failed: %d", w.Code)
	}
	if w := call(t, h, "GET", "/v1/me/telegram", token, ""); !strings.Contains(w.Body.String(), `"linked":false`) {
		t.Fatalf("still linked: %s", w.Body.String())
	}
	if status := webhook(5, "/balance", "test_secret_12345"); status != 204 {
		t.Fatalf("unlinked command failed: %d", status)
	}
}

func TestTelegramLinkExpiration(t *testing.T) {
	a, cleanup := testApp(t)
	defer cleanup()
	firstID, firstToken := testPlayer(t, a)
	a.telegramToken, a.telegramSecret, a.telegramUsername = "dummy", "test_secret_12345", "RoadsterTestBot"
	h := a.routes()
	link := call(t, h, "POST", "/v1/me/telegram/link", firstToken, "")
	if link.Code != 200 {
		t.Fatalf("link creation: %d", link.Code)
	}
	var value struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(link.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	code := strings.Split(value.URL, "start=")[1]
	if _, err := a.db.Exec(context.Background(), `UPDATE telegram_link_challenges SET expires_at=now()-interval '1 second' WHERE player_id=$1`, firstID); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"update_id":1,"message":{"text":%q,"from":{"id":12345},"chat":{"id":12345,"type":"private"}}}`, "/start "+code)
	var update telegramUpdate
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	response, err := a.processTelegramUpdate(context.Background(), update)
	if err != nil || !strings.Contains(response, "منقضی") {
		t.Fatalf("expired link accepted: %s %v", response, err)
	}
	if w := call(t, h, "GET", "/v1/me/telegram", firstToken, ""); !strings.Contains(w.Body.String(), `"linked":false`) {
		t.Fatalf("expired link connected: %s", w.Body.String())
	}
}
