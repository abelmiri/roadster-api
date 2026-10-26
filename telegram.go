package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *app) telegramConfigured() bool {
	if a.telegramToken == "" || len(a.telegramSecret) < 16 || len(a.telegramSecret) > 256 || a.telegramUsername == "" {
		return false
	}
	for _, c := range a.telegramSecret {
		if c != '_' && c != '-' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	for _, c := range a.telegramUsername {
		if c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func (a *app) getTelegram(w http.ResponseWriter, r *http.Request) {
	var linked bool
	err := a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM telegram_accounts WHERE player_id=$1)`, currentPlayer(r)).Scan(&linked)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	writeJSON(w, 200, map[string]any{"linked": linked, "botUsername": a.telegramUsername})
}

func (a *app) createTelegramLink(w http.ResponseWriter, r *http.Request) {
	if !a.telegramConfigured() {
		errorJSON(w, 503, "telegram_unavailable")
		return
	}
	var linked bool
	if err := a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM telegram_accounts WHERE player_id=$1)`, currentPlayer(r)).Scan(&linked); err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	if linked {
		errorJSON(w, 409, "telegram_already_linked")
		return
	}
	code, hash, err := newToken()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	_, err = a.db.Exec(r.Context(), `INSERT INTO telegram_link_challenges(player_id,code_hash,expires_at)
		VALUES($1,$2,now()+interval '10 minutes')
		ON CONFLICT(player_id) DO UPDATE SET code_hash=EXCLUDED.code_hash, expires_at=EXCLUDED.expires_at`, currentPlayer(r), hash[:])
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	writeJSON(w, 200, map[string]any{"url": "https://t.me/" + a.telegramUsername + "?start=" + code, "expiresIn": 600})
}

func (a *app) unlinkTelegram(w http.ResponseWriter, r *http.Request) {
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `DELETE FROM telegram_accounts WHERE player_id=$1`, currentPlayer(r)); err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM telegram_link_challenges WHERE player_id=$1`, currentPlayer(r))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
}

func (a *app) telegramWebhook(w http.ResponseWriter, r *http.Request) {
	if !a.telegramConfigured() {
		errorJSON(w, 503, "telegram_unavailable")
		return
	}
	provided := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if len(provided) != len(a.telegramSecret) || subtle.ConstantTimeCompare([]byte(provided), []byte(a.telegramSecret)) != 1 {
		errorJSON(w, 401, "unauthorized")
		return
	}
	var update telegramUpdate
	if !readJSON(w, r, &update) {
		return
	}
	if update.UpdateID < 0 || update.Message == nil || update.Message.Chat.Type != "private" ||
		update.Message.Chat.ID <= 0 || update.Message.From.ID != update.Message.Chat.ID {
		w.WriteHeader(204)
		return
	}
	response, err := a.processTelegramUpdate(r.Context(), update)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	if err := a.sendTelegramMessage(r.Context(), update.Message.Chat.ID, response); err != nil {
		errorJSON(w, 502, "telegram_send_failed")
		return
	}
	w.WriteHeader(204)
}

func (a *app) processTelegramUpdate(ctx context.Context, update telegramUpdate) (string, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	chatID := update.Message.Chat.ID
	var response string
	tag, err := tx.Exec(ctx, `INSERT INTO telegram_updates(update_id,chat_id,response) VALUES($1,$2,'') ON CONFLICT DO NOTHING`, update.UpdateID, chatID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		if err := tx.QueryRow(ctx, `SELECT response FROM telegram_updates WHERE update_id=$1 AND chat_id=$2`, update.UpdateID, chatID).Scan(&response); err != nil {
			return "", err
		}
		return response, nil
	}
	response, err = a.telegramCommand(ctx, tx, chatID, strings.TrimSpace(update.Message.Text))
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE telegram_updates SET response=$1 WHERE update_id=$2`, response, update.UpdateID); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return response, nil
}

func (a *app) telegramCommand(ctx context.Context, tx pgx.Tx, chatID int64, text string) (string, error) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return telegramHelp, nil
	}
	command := strings.SplitN(parts[0], "@", 2)[0]
	if command == "/start" && len(parts) == 2 {
		return linkTelegramAccount(ctx, tx, chatID, parts[1])
	}
	if command == "/help" || command == "/start" {
		return telegramHelp, nil
	}
	var playerID string
	err := tx.QueryRow(ctx, `SELECT player_id::text FROM telegram_accounts WHERE telegram_id=$1`, chatID).Scan(&playerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "حساب بازی متصل نیست. از داخل بازی، لینک اتصال تلگرام را باز کنید.", nil
	}
	if err != nil {
		return "", err
	}
	switch command {
	case "/balance", "/record":
		var coins, record int64
		err = tx.QueryRow(ctx, `SELECT COALESCE((progress->>'coins')::bigint,0), COALESCE((progress->'record'->>'distance')::bigint,0)
			FROM players WHERE id=$1`, playerID).Scan(&coins, &record)
		if err != nil {
			return "", err
		}
		if command == "/balance" {
			return fmt.Sprintf("موجودی: %d سکه", coins), nil
		}
		return fmt.Sprintf("رکورد: %d", record), nil
	case "/redeem":
		if len(parts) != 2 {
			return "روش استفاده: /redeem CODE", nil
		}
		result, redeemErr := redeemTx(ctx, tx, playerID, parts[1])
		if redeemErr != nil {
			_, code := redeemError(redeemErr)
			if code == "database_error" {
				return "", redeemErr
			}
			return "کد قابل استفاده نیست: " + code, nil
		}
		return fmt.Sprintf("%d سکه اضافه شد. موجودی: %d", result.CoinsAdded, result.Coins), nil
	default:
		return telegramHelp, nil
	}
}

const telegramHelp = "دستورها: /balance برای موجودی، /record برای رکورد، /redeem CODE برای دریافت سکه. اتصال حساب از داخل بازی انجام می‌شود."

func linkTelegramAccount(ctx context.Context, tx pgx.Tx, chatID int64, code string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(raw) != 32 {
		return "لینک اتصال نامعتبر یا منقضی شده است.", nil
	}
	hash := sha256.Sum256(raw)
	var playerID string
	err = tx.QueryRow(ctx, `SELECT player_id::text FROM telegram_link_challenges WHERE code_hash=$1 AND expires_at > now() FOR UPDATE`, hash[:]).Scan(&playerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "لینک اتصال نامعتبر یا منقضی شده است.", nil
	}
	if err != nil {
		return "", err
	}
	var linked bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_accounts WHERE player_id=$1 OR telegram_id=$2)`, playerID, chatID).Scan(&linked); err != nil {
		return "", err
	}
	if linked {
		return "این حساب بازی یا تلگرام قبلاً متصل شده است.", nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO telegram_accounts(player_id,telegram_id,chat_id) VALUES($1,$2,$2)`, playerID, chatID); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM telegram_link_challenges WHERE player_id=$1`, playerID); err != nil {
		return "", err
	}
	return "حساب Roadster به تلگرام متصل شد. /balance و /record را امتحان کنید.", nil
}

func (a *app) sendTelegramMessage(ctx context.Context, chatID int64, message string) error {
	base := a.telegramAPIURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	client := a.telegramClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	body, err := json.Marshal(map[string]any{"chat_id": chatID, "text": message})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/bot"+url.PathEscape(a.telegramToken)+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&result) != nil || !result.OK {
		return errors.New("telegram sendMessage failed")
	}
	return nil
}
