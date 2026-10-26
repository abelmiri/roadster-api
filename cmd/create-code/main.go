package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func main() {
	coins := flag.Int64("coins", 0, "coins granted per player")
	maxUses := flag.Int("max-redemptions", 0, "maximum number of players")
	expires := flag.String("expires", "", "expiry in RFC3339 format")
	flag.Parse()
	if *coins <= 0 || *coins > 1<<53-1 || *maxUses <= 0 || *expires == "" || flag.NArg() != 0 {
		log.Fatal("usage: go run ./cmd/create-code -coins 500 -max-redemptions 100 -expires 2027-01-01T00:00:00Z")
	}
	expiry, err := time.Parse(time.RFC3339, *expires)
	if err != nil || !expiry.After(time.Now()) {
		log.Fatal("expires must be a future RFC3339 timestamp")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	var secret [15]byte
	if _, err := rand.Read(secret[:]); err != nil {
		log.Fatal(err)
	}
	code := "RDST-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret[:])
	hash := sha256.Sum256([]byte(strings.ToUpper(code)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close(context.Background())
	_, err = db.Exec(ctx, `INSERT INTO promo_codes(id,code_hash,coins,max_redemptions,expires_at)
		VALUES($1,$2,$3,$4,$5)`, uuid.NewString(), hash[:], *coins, *maxUses, expiry)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(code)
}
