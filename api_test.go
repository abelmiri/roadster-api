package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sampleProgress = `{"version":1,"coins":175,"score":920,"playerName":"Ada","record":{"distance":1200,"name":"Ada","needsName":false},"ownedWeapons":["machine-gun"],"weaponUpgrades":{},"carUpgrades":{},"ownedPaints":["white"],"selectedPaint":"white","selectedVehicle":"roadster","ownedVehicles":["roadster"],"vehiclePaints":{},"missions":{},"futureField":{"retained":true}}`

func TestProgressValidationAndScoreReset(t *testing.T) {
	p, err := validProgress(json.RawMessage(sampleProgress))
	if err != nil || p.Coins != 175 || p.Record.Name != "Ada" {
		t.Fatalf("valid progress rejected: %v", err)
	}
	reset, err := resetScore(json.RawMessage(sampleProgress))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(reset, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["score"] != float64(0) || decoded["coins"] != float64(175) || decoded["futureField"] == nil {
		t.Fatalf("reset discarded progress: %s", reset)
	}
	bad := []string{
		strings.Replace(sampleProgress, `"coins":175`, `"coins":-1`, 1),
		strings.Replace(sampleProgress, `"coins":175`, `"coins":9007199254740992`, 1),
		strings.Replace(sampleProgress, `"name":"Ada"`, `"name":"Bad\nName"`, 1),
		strings.Replace(sampleProgress, `"ownedWeapons":["machine-gun"]`, `"ownedWeapons":["machine-gun","machine-gun"]`, 1),
	}
	for _, value := range bad {
		if _, err := validProgress(json.RawMessage(value)); err == nil {
			t.Errorf("accepted invalid progress: %s", value)
		}
	}
}

func TestJSONReaderRejectsTrailingInput(t *testing.T) {
	for _, body := range []string{`{"deviceId":"abc"} {}`, `{"deviceId":"abc"} broken`} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var input optionRequest
		if readJSON(w, r, &input) || w.Code != 400 {
			t.Fatalf("trailing data accepted: %q, status %d", body, w.Code)
		}
	}
}

func TestUUID(t *testing.T) {
	a, err := newUUID()
	if err != nil || !validUUID(a) {
		t.Fatalf("invalid generated UUID: %s, %v", a, err)
	}
	b, _ := newUUID()
	if a == b || validUUID("not-a-uuid") {
		t.Fatal("UUID generation or validation failed")
	}
}

func testApp(t *testing.T) (*app, func()) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ReplaceAll(strings.Split(newTestUUID(t), "-")[0], "-", "")
	if _, err := root.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	wa, err := webauthn.New(&webauthn.Config{RPDisplayName: "Roadster", RPID: "localhost", RPOrigins: []string{"http://localhost:5173"}})
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		db.Close()
		_, _ = root.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		root.Close()
	}
	return &app{db: db, wa: wa, origin: "http://localhost:5173"}, cleanup
}

func newTestUUID(t *testing.T) string {
	t.Helper()
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func call(t *testing.T, handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestDatabaseFlow(t *testing.T) {
	a, cleanup := testApp(t)
	defer cleanup()
	ctx := context.Background()
	playerID := newTestUUID(t)
	token, hash, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec(ctx, `INSERT INTO players(id) VALUES($1)`, playerID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec(ctx, `INSERT INTO sessions(token_hash, player_id, expires_at) VALUES($1,$2,now()+interval '1 day')`, hash[:], playerID)
	if err != nil {
		t.Fatal(err)
	}
	h := a.routes()
	put := `{"revision":0,"progress":` + sampleProgress + `,"xp":200}`
	if w := call(t, h, "PUT", "/v1/me/progress", token, put); w.Code != 200 {
		t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
	}
	key := newTestUUID(t)
	request := `{"idempotencyKey":"` + key + `"}`
	first := call(t, h, "POST", "/v1/runs", token, request)
	if first.Code != 201 {
		t.Fatalf("new run failed: %d %s", first.Code, first.Body.String())
	}
	var run struct {
		RunID    string `json:"runId"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &run); err != nil || run.Revision != 2 {
		t.Fatalf("unexpected run: %s %v", first.Body.String(), err)
	}
	retry := call(t, h, "POST", "/v1/runs", token, request)
	if retry.Code != 200 || !strings.Contains(retry.Body.String(), run.RunID) {
		t.Fatalf("idempotent retry failed: %d %s", retry.Code, retry.Body.String())
	}
	got := call(t, h, "GET", "/v1/me/progress", token, "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"score":0`) || !strings.Contains(got.Body.String(), `"coins":175`) {
		t.Fatalf("new run lost progress: %d %s", got.Code, got.Body.String())
	}
	stale := call(t, h, "PUT", "/v1/me/progress", token, put)
	if stale.Code != 409 || !strings.Contains(stale.Body.String(), "revision_conflict") {
		t.Fatalf("stale write accepted: %d %s", stale.Code, stale.Body.String())
	}
	otherPlayer := newTestUUID(t)
	_, _ = a.db.Exec(ctx, `INSERT INTO players(id) VALUES($1)`, otherPlayer)
	otherRun := newTestUUID(t)
	_, _ = a.db.Exec(ctx, `INSERT INTO runs(id,player_id,idempotency_key) VALUES($1,$2,$3)`, otherRun, otherPlayer, newTestUUID(t))
	stolen := `{"revision":2,"progress":` + sampleProgress + `,"xp":200,"runId":"` + otherRun + `","runStatus":"active"}`
	if w := call(t, h, "PUT", "/v1/me/progress", token, stolen); w.Code != 404 {
		t.Fatalf("foreign run accepted: %d %s", w.Code, w.Body.String())
	}
	_, _ = a.db.Exec(ctx, `UPDATE sessions SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, hash[:])
	if w := call(t, h, "GET", "/v1/me/progress", token, ""); w.Code != 401 {
		t.Fatalf("expired session accepted: %d", w.Code)
	}
}

func TestCeremonyExpiresAndIsSingleUse(t *testing.T) {
	a, cleanup := testApp(t)
	defer cleanup()
	ctx := context.Background()
	id := newTestUUID(t)
	_, err := a.db.Exec(ctx, `INSERT INTO ceremonies(id,kind,session_data,expires_at) VALUES($1,'login','{}',now()-interval '1 second')`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.takeCeremony(ctx, id, "login"); err == nil {
		t.Fatal("expired ceremony accepted")
	}
	id = newTestUUID(t)
	_, err = a.db.Exec(ctx, `INSERT INTO ceremonies(id,kind,session_data,expires_at) VALUES($1,'login','{}',now()+interval '1 minute')`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.takeCeremony(ctx, id, "login"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.takeCeremony(ctx, id, "login"); err == nil {
		t.Fatal("ceremony reused")
	}
}

func TestPasskeyOptionsPersistChallenge(t *testing.T) {
	a, cleanup := testApp(t)
	defer cleanup()
	deviceID := newTestUUID(t)
	w := call(t, a.routes(), "POST", "/v1/auth/register/options", "", `{"deviceId":"`+deviceID+`"}`)
	if w.Code != 200 {
		t.Fatalf("registration options failed: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		CeremonyID string          `json:"ceremonyId"`
		Options    json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !validUUID(response.CeremonyID) || !bytes.Contains(response.Options, []byte(`"publicKey"`)) {
		t.Fatalf("bad registration options: %s %v", w.Body.String(), err)
	}
	c, err := a.takeCeremony(context.Background(), response.CeremonyID, "register")
	if err != nil || c.deviceID != deviceID || !validUUID(c.playerID) {
		t.Fatalf("stored ceremony invalid: %#v %v", c, err)
	}
	if _, err := a.takeCeremony(context.Background(), response.CeremonyID, "register"); err == nil {
		t.Fatal("registration ceremony reused")
	}
}
