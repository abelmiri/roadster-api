package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	api := flag.String("api", "https://roadster-api.vercel.app", "public HTTPS API origin")
	flag.Parse()
	token, secret := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_WEBHOOK_SECRET")
	parsed, err := url.Parse(*api)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || token == "" || len(secret) < 16 || len(secret) > 256 {
		log.Fatal("set -api to a public HTTPS origin and provide TELEGRAM_BOT_TOKEN plus a 16-256 character TELEGRAM_WEBHOOK_SECRET")
	}
	for _, c := range secret {
		if c != '_' && c != '-' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			log.Fatal("TELEGRAM_WEBHOOK_SECRET must contain only letters, digits, _ or -")
		}
	}
	body, err := json.Marshal(map[string]any{
		"url":             strings.TrimRight(*api, "/") + "/v1/telegram/webhook",
		"secret_token":    secret,
		"allowed_updates": []string{"message"},
		"max_connections": 4,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+url.PathEscape(token)+"/setWebhook", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Fatal(err)
	}
	if resp.StatusCode != 200 || !result.OK {
		log.Fatalf("Telegram rejected webhook: %s", result.Description)
	}
	fmt.Println("Webhook registered:", strings.TrimRight(*api, "/")+"/v1/telegram/webhook")
}
