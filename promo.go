package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

type redeemRequest struct {
	Code string `json:"code"`
}

type redeemResult struct {
	CoinsAdded int64 `json:"coinsAdded"`
	Coins      int64 `json:"coins"`
	Revision   int64 `json:"revision"`
}

var (
	errInvalidCode      = errors.New("invalid_code")
	errCodeExpired      = errors.New("code_expired")
	errCodeExhausted    = errors.New("code_exhausted")
	errCodeUsed         = errors.New("code_already_redeemed")
	errProgressRequired = errors.New("progress_required")
	errBalanceOverflow  = errors.New("balance_overflow")
)

func normalizeCode(value string) (string, bool) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) < 8 || len(value) > 64 {
		return "", false
	}
	for _, c := range value {
		if c != '-' && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return "", false
		}
	}
	return value, true
}

// redeem performs every balance and usage change in one transaction. Locking the
// code row serializes redemptions even when Vercel invokes several functions.
func (a *app) redeem(ctx context.Context, playerID, code string) (redeemResult, error) {
	var result redeemResult
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	result, err = redeemTx(ctx, tx, playerID, code)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

func redeemTx(ctx context.Context, tx pgx.Tx, playerID, code string) (redeemResult, error) {
	var result redeemResult
	canonical, ok := normalizeCode(code)
	if !ok {
		return result, errInvalidCode
	}
	hash := sha256.Sum256([]byte(canonical))
	var codeID string
	var amount int64
	var maxUses, used int
	var expired bool
	err := tx.QueryRow(ctx, `SELECT id::text, coins, max_redemptions, redeemed_count,
		expires_at <= now() FROM promo_codes WHERE code_hash=$1 FOR UPDATE`, hash[:]).
		Scan(&codeID, &amount, &maxUses, &used, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, errInvalidCode
	}
	if err != nil {
		return result, err
	}
	var alreadyUsed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM promo_redemptions WHERE code_id=$1 AND player_id=$2)`, codeID, playerID).Scan(&alreadyUsed); err != nil {
		return result, err
	}
	if alreadyUsed {
		return result, errCodeUsed
	}
	if expired {
		return result, errCodeExpired
	}
	if used >= maxUses {
		return result, errCodeExhausted
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT progress, revision FROM players WHERE id=$1 FOR UPDATE`, playerID).Scan(&raw, &result.Revision)
	if err != nil {
		return result, err
	}
	if len(raw) == 0 {
		return result, errProgressRequired
	}
	var progress map[string]json.RawMessage
	if err = json.Unmarshal(raw, &progress); err != nil {
		return result, err
	}
	var balance int64
	if err = json.Unmarshal(progress["coins"], &balance); err != nil || !validCount(balance) {
		return result, errors.New("invalid stored balance")
	}
	if amount > maxSafeInteger-balance {
		return result, errBalanceOverflow
	}
	result.CoinsAdded, result.Coins = amount, balance+amount
	progress["coins"], err = json.Marshal(result.Coins)
	if err != nil {
		return result, err
	}
	updated, err := json.Marshal(progress)
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE players SET progress=$1, revision=revision+1 WHERE id=$2`, json.RawMessage(updated), playerID); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO promo_redemptions(code_id,player_id) VALUES($1,$2)`, codeID, playerID); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE promo_codes SET redeemed_count=redeemed_count+1 WHERE id=$1`, codeID); err != nil {
		return result, err
	}
	result.Revision++
	return result, nil
}

func redeemError(err error) (int, string) {
	switch err {
	case errInvalidCode:
		return 400, "invalid_code"
	case errCodeExpired:
		return 410, "code_expired"
	case errCodeExhausted:
		return 409, "code_exhausted"
	case errCodeUsed:
		return 409, "code_already_redeemed"
	case errProgressRequired:
		return 409, "progress_required"
	case errBalanceOverflow:
		return 409, "balance_overflow"
	default:
		return 500, "database_error"
	}
}

func (a *app) redeemCode(w http.ResponseWriter, r *http.Request) {
	var input redeemRequest
	if !readJSON(w, r, &input) {
		return
	}
	result, err := a.redeem(r.Context(), currentPlayer(r), input.Code)
	if err != nil {
		status, code := redeemError(err)
		errorJSON(w, status, code)
		return
	}
	writeJSON(w, 200, result)
}
