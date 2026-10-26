package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type app struct {
	db               *pgxpool.Pool
	wa               *webauthn.WebAuthn
	origin           string
	telegramToken    string
	telegramSecret   string
	telegramUsername string
	telegramClient   *http.Client
	telegramAPIURL   string
}

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		// Neon Marketplace prefixes its variables when a custom integration prefix is set.
		databaseURL = os.Getenv("DATABASE_URL_DATABASE_URL")
	}
	origin := strings.TrimRight(os.Getenv("GAME_ORIGIN"), "/")
	rpID := os.Getenv("RP_ID")
	if databaseURL == "" || origin == "" || rpID == "" {
		log.Fatal("DATABASE_URL, GAME_ORIGIN and RP_ID are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	config.MaxConns = 4
	config.MinConns = 0
	config.MaxConnIdleTime = time.Minute
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatal(err)
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "Roadster",
		RPID:          rpID,
		RPOrigins:     []string{origin},
	})
	if err != nil {
		log.Fatal(err)
	}
	a := &app{db: db, wa: wa, origin: origin,
		telegramToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		telegramSecret:   os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		telegramUsername: os.Getenv("TELEGRAM_BOT_USERNAME"),
		telegramClient:   &http.Client{Timeout: 5 * time.Second},
		telegramAPIURL:   "https://api.telegram.org"}
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("Roadster API listening on :%s", port)
	log.Fatal(server.ListenAndServe())
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)
	mux.HandleFunc("POST /v1/auth/register/options", a.registerOptions)
	mux.HandleFunc("POST /v1/auth/register/verify", a.registerVerify)
	mux.HandleFunc("POST /v1/auth/login/options", a.loginOptions)
	mux.HandleFunc("POST /v1/auth/login/verify", a.loginVerify)
	mux.Handle("POST /v1/auth/logout", a.authorize(http.HandlerFunc(a.logout)))
	mux.Handle("GET /v1/me/progress", a.authorize(http.HandlerFunc(a.getProgress)))
	mux.Handle("PUT /v1/me/progress", a.authorize(http.HandlerFunc(a.putProgress)))
	mux.Handle("POST /v1/runs", a.authorize(http.HandlerFunc(a.createRun)))
	mux.Handle("POST /v1/me/redeem", a.authorize(http.HandlerFunc(a.redeemCode)))
	mux.Handle("GET /v1/me/telegram", a.authorize(http.HandlerFunc(a.getTelegram)))
	mux.Handle("POST /v1/me/telegram/link", a.authorize(http.HandlerFunc(a.createTelegramLink)))
	mux.Handle("DELETE /v1/me/telegram", a.authorize(http.HandlerFunc(a.unlinkTelegram)))
	mux.HandleFunc("POST /v1/telegram/webhook", a.telegramWebhook)
	return a.cors(mux)
}

func (a *app) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Origin")
		if r.Header.Get("Origin") == a.origin {
			w.Header().Set("Access-Control-Allow-Origin", a.origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		}
		if r.Method == http.MethodOptions {
			if r.Header.Get("Origin") != a.origin {
				errorJSON(w, http.StatusForbidden, "origin_not_allowed")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != a.origin {
			errorJSON(w, http.StatusForbidden, "origin_not_allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		errorJSON(w, http.StatusServiceUnavailable, "database_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		errorJSON(w, http.StatusUnsupportedMediaType, "json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		errorJSON(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func errorJSON(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
