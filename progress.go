package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const maxSafeInteger int64 = 1<<53 - 1

type recordSave struct {
	Distance  int64  `json:"distance"`
	Name      string `json:"name"`
	NeedsName bool   `json:"needsName"`
}

type progressSave struct {
	Version         int               `json:"version"`
	Coins           int64             `json:"coins"`
	Score           int64             `json:"score"`
	PlayerName      string            `json:"playerName"`
	Record          recordSave        `json:"record"`
	OwnedWeapons    []string          `json:"ownedWeapons"`
	OwnedPaints     []string          `json:"ownedPaints"`
	OwnedVehicles   []string          `json:"ownedVehicles"`
	WeaponUpgrades  map[string]int64  `json:"weaponUpgrades"`
	CarUpgrades     map[string]int64  `json:"carUpgrades"`
	VehiclePaints   map[string]string `json:"vehiclePaints"`
	SelectedPaint   string            `json:"selectedPaint"`
	SelectedVehicle string            `json:"selectedVehicle"`
	Missions        json.RawMessage   `json:"missions"`
}

func validCount(n int64) bool { return n >= 0 && n <= maxSafeInteger }

func validName(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > 32 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func validID(s string) bool {
	if s == "" || len(s) > 48 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

func validIDs(ids []string) bool {
	if len(ids) > 64 {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func validProgress(raw json.RawMessage) (progressSave, error) {
	var p progressSave
	if len(raw) == 0 || len(raw) > 64<<10 || !json.Valid(raw) {
		return p, errors.New("invalid progress JSON")
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.Version != 1 || !validCount(p.Coins) || !validCount(p.Score) || !validCount(p.Record.Distance) ||
		!validName(p.PlayerName) || !validName(p.Record.Name) ||
		(p.Record.Name != "" && (p.Record.NeedsName || p.PlayerName != p.Record.Name)) ||
		(p.PlayerName != "" && p.Record.Distance == 0) ||
		!validIDs(p.OwnedWeapons) || !validIDs(p.OwnedPaints) || !validIDs(p.OwnedVehicles) ||
		len(p.WeaponUpgrades) > 64 || len(p.CarUpgrades) > 64 || len(p.VehiclePaints) > 64 {
		return p, errors.New("invalid progress fields")
	}
	for id, level := range p.WeaponUpgrades {
		if !validID(id) || level < 0 || level > 100 {
			return p, errors.New("invalid weapon upgrade")
		}
	}
	for id, level := range p.CarUpgrades {
		if !validID(id) || level < 0 || level > 100 {
			return p, errors.New("invalid car upgrade")
		}
	}
	for id, paint := range p.VehiclePaints {
		if !validID(id) || !validID(paint) {
			return p, errors.New("invalid vehicle paint")
		}
	}
	if (p.SelectedPaint != "" && !validID(p.SelectedPaint)) ||
		(p.SelectedVehicle != "" && !validID(p.SelectedVehicle)) || len(p.Missions) > 32<<10 {
		return p, errors.New("invalid selection or missions")
	}
	return p, nil
}

type progressResponse struct {
	Revision int64           `json:"revision"`
	Progress json.RawMessage `json:"progress"`
	XP       int64           `json:"xp"`
}

func (a *app) readProgress(ctx *http.Request, playerID string) (progressResponse, error) {
	var result progressResponse
	err := a.db.QueryRow(ctx.Context(), `SELECT revision, progress, xp FROM players WHERE id=$1`, playerID).
		Scan(&result.Revision, &result.Progress, &result.XP)
	return result, err
}

func (a *app) getProgress(w http.ResponseWriter, r *http.Request) {
	result, err := a.readProgress(r, currentPlayer(r))
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	writeJSON(w, 200, result)
}

type putProgressRequest struct {
	Revision  int64           `json:"revision"`
	Progress  json.RawMessage `json:"progress"`
	XP        int64           `json:"xp"`
	RunID     string          `json:"runId,omitempty"`
	RunStatus string          `json:"runStatus,omitempty"`
}

func (a *app) putProgress(w http.ResponseWriter, r *http.Request) {
	var input putProgressRequest
	if !readJSON(w, r, &input) {
		return
	}
	p, err := validProgress(input.Progress)
	if err != nil || input.Revision < 0 || !validCount(input.XP) ||
		(input.RunID == "" && input.RunStatus != "") ||
		(input.RunID != "" && (!validUUID(input.RunID) || (input.RunStatus != "active" && input.RunStatus != "finished"))) {
		errorJSON(w, 400, "invalid_progress")
		return
	}
	playerID := currentPlayer(r)
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	defer tx.Rollback(r.Context())
	var revision int64
	err = tx.QueryRow(r.Context(), `SELECT revision FROM players WHERE id=$1 FOR UPDATE`, playerID).Scan(&revision)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	if revision != input.Revision {
		var current progressResponse
		if err = tx.QueryRow(r.Context(), `SELECT revision, progress, xp FROM players WHERE id=$1`, playerID).
			Scan(&current.Revision, &current.Progress, &current.XP); err != nil {
			errorJSON(w, 500, "database_error")
			return
		}
		writeJSON(w, 409, map[string]any{"error": "revision_conflict", "current": current})
		return
	}
	if input.RunID != "" {
		var previousScore int64
		var previousStatus string
		err = tx.QueryRow(r.Context(), `SELECT score, status FROM runs WHERE id=$1 AND player_id=$2 FOR UPDATE`, input.RunID, playerID).
			Scan(&previousScore, &previousStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			errorJSON(w, 404, "run_not_found")
			return
		}
		if err != nil {
			errorJSON(w, 500, "database_error")
			return
		}
		if previousStatus != "active" || p.Score < previousScore {
			errorJSON(w, 409, "run_not_active_or_score_reduced")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE runs SET score=$1, status=$2, updated_at=now() WHERE id=$3`, p.Score, input.RunStatus, input.RunID)
		if err != nil {
			errorJSON(w, 500, "database_error")
			return
		}
	}
	var name any
	if p.Record.Name != "" && !p.Record.NeedsName {
		name = p.Record.Name
	}
	_, err = tx.Exec(r.Context(), `UPDATE players SET progress=$1, xp=$2, revision=revision+1,
		name=COALESCE($3,name) WHERE id=$4`, input.Progress, input.XP, name, playerID)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	writeJSON(w, 200, map[string]int64{"revision": revision + 1})
}

type createRunRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

func resetScore(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	object["score"] = json.RawMessage("0")
	return json.Marshal(object)
}

func (a *app) createRun(w http.ResponseWriter, r *http.Request) {
	var input createRunRequest
	if !readJSON(w, r, &input) {
		return
	}
	if !validUUID(input.IdempotencyKey) {
		errorJSON(w, 400, "invalid_idempotency_key")
		return
	}
	playerID := currentPlayer(r)
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	defer tx.Rollback(r.Context())
	var revision int64
	var raw json.RawMessage
	err = tx.QueryRow(r.Context(), `SELECT revision, progress FROM players WHERE id=$1 FOR UPDATE`, playerID).Scan(&revision, &raw)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	var runID, status string
	err = tx.QueryRow(r.Context(), `SELECT id::text, status FROM runs WHERE player_id=$1 AND idempotency_key=$2`, playerID, input.IdempotencyKey).
		Scan(&runID, &status)
	if err == nil {
		writeJSON(w, 200, map[string]any{"runId": runID, "status": status, "revision": revision})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		errorJSON(w, 500, "database_error")
		return
	}
	runID, err = newUUID()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE runs SET status='abandoned', updated_at=now() WHERE player_id=$1 AND status='active'`, playerID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO runs(id, player_id, idempotency_key) VALUES($1,$2,$3)`, runID, playerID, input.IdempotencyKey)
	}
	if err == nil {
		raw, err = resetScore(raw)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE players SET progress=$1, revision=revision+1 WHERE id=$2`, raw, playerID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	writeJSON(w, 201, map[string]any{"runId": runID, "status": "active", "revision": revision + 1})
}
