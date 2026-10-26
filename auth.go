package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const sessionLifetime = 30 * 24 * time.Hour

type passkeyUser struct {
	id          string
	credentials []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return []byte(u.id) }
func (u passkeyUser) WebAuthnName() string                       { return "roadster-" + u.id[:8] }
func (u passkeyUser) WebAuthnDisplayName() string                { return "Roadster Player" }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

type ceremony struct {
	id       string
	kind     string
	playerID string
	deviceID string
	session  webauthn.SessionData
}

type optionRequest struct {
	DeviceID string `json:"deviceId"`
}

type verifyRequest struct {
	CeremonyID string          `json:"ceremonyId"`
	Credential json.RawMessage `json:"credential"`
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (a *app) saveCeremony(ctx context.Context, c ceremony) error {
	data, err := json.Marshal(c.session)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(ctx, `INSERT INTO ceremonies (id, kind, player_id, device_id, session_data, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + interval '5 minutes')`, c.id, c.kind, nullableID(c.playerID), nullableID(c.deviceID), json.RawMessage(data))
	return err
}

func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func (a *app) takeCeremony(ctx context.Context, id, kind string) (ceremony, error) {
	var c ceremony
	var playerID, deviceID *string
	var raw []byte
	err := a.db.QueryRow(ctx, `DELETE FROM ceremonies WHERE id=$1 AND kind=$2 AND expires_at > now()
		RETURNING player_id::text, device_id::text, session_data`, id, kind).Scan(&playerID, &deviceID, &raw)
	if err != nil {
		return c, err
	}
	c.id, c.kind = id, kind
	if playerID != nil {
		c.playerID = *playerID
	}
	if deviceID != nil {
		c.deviceID = *deviceID
	}
	err = json.Unmarshal(raw, &c.session)
	return c, err
}

func (a *app) registerOptions(w http.ResponseWriter, r *http.Request) {
	var input optionRequest
	if !readJSON(w, r, &input) {
		return
	}
	if !validUUID(input.DeviceID) {
		errorJSON(w, http.StatusBadRequest, "invalid_device_id")
		return
	}
	playerID, err := newUUID()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	ceremonyID, err := newUUID()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	options, session, err := a.wa.BeginRegistration(passkeyUser{id: playerID},
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired, ResidentKey: protocol.ResidentKeyRequirementRequired}))
	if err != nil {
		errorJSON(w, 500, "passkey_unavailable")
		return
	}
	if err := a.saveCeremony(r.Context(), ceremony{id: ceremonyID, kind: "register", playerID: playerID, deviceID: input.DeviceID, session: *session}); err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremonyId": ceremonyID, "options": options})
}

func credentialRequest(r *http.Request, raw json.RawMessage) *http.Request {
	copy := r.Clone(r.Context())
	copy.Body = io.NopCloser(bytes.NewReader(raw))
	copy.ContentLength = int64(len(raw))
	return copy
}

func (a *app) registerVerify(w http.ResponseWriter, r *http.Request) {
	var input verifyRequest
	if !readJSON(w, r, &input) {
		return
	}
	if !validUUID(input.CeremonyID) || !json.Valid(input.Credential) {
		errorJSON(w, 400, "invalid_request")
		return
	}
	c, err := a.takeCeremony(r.Context(), input.CeremonyID, "register")
	if err != nil {
		errorJSON(w, 400, "ceremony_expired_or_used")
		return
	}
	credential, err := a.wa.FinishRegistration(passkeyUser{id: c.playerID}, c.session, credentialRequest(r, input.Credential))
	if err != nil {
		errorJSON(w, 400, "invalid_passkey_response")
		return
	}
	data, err := json.Marshal(credential)
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	token, hash, err := newToken()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `INSERT INTO players(id) VALUES($1)`, c.playerID); err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO credentials(id, player_id, data) VALUES($1,$2,$3)`, credential.ID, c.playerID, json.RawMessage(data))
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO devices(id, player_id) VALUES($1,$2)`, c.deviceID, c.playerID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO sessions(token_hash, player_id, expires_at) VALUES($1,$2,now()+interval '30 days')`, hash[:], c.playerID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		errorJSON(w, 409, "account_or_device_conflict")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"playerId": c.playerID, "token": token, "expiresIn": int(sessionLifetime.Seconds())})
}

func (a *app) loginOptions(w http.ResponseWriter, r *http.Request) {
	var input optionRequest
	if !readJSON(w, r, &input) {
		return
	}
	if !validUUID(input.DeviceID) {
		errorJSON(w, 400, "invalid_device_id")
		return
	}
	ceremonyID, err := newUUID()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	options, session, err := a.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		errorJSON(w, 500, "passkey_unavailable")
		return
	}
	if err := a.saveCeremony(r.Context(), ceremony{id: ceremonyID, kind: "login", deviceID: input.DeviceID, session: *session}); err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	writeJSON(w, 200, map[string]any{"ceremonyId": ceremonyID, "options": options})
}

func (a *app) loadPasskeyUser(ctx context.Context, credentialID, handle []byte) (webauthn.User, error) {
	id := string(handle)
	if !validUUID(id) {
		return nil, errors.New("invalid user handle")
	}
	var exists bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM credentials WHERE id=$1 AND player_id=$2)`, credentialID, id).Scan(&exists); err != nil || !exists {
		return nil, errors.New("credential not found")
	}
	rows, err := a.db.Query(ctx, `SELECT data FROM credentials WHERE player_id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	u := passkeyUser{id: id}
	for rows.Next() {
		var raw []byte
		var credential webauthn.Credential
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &credential); err != nil {
			return nil, err
		}
		u.credentials = append(u.credentials, credential)
	}
	return u, rows.Err()
}

func (a *app) loginVerify(w http.ResponseWriter, r *http.Request) {
	var input verifyRequest
	if !readJSON(w, r, &input) {
		return
	}
	if !validUUID(input.CeremonyID) || !json.Valid(input.Credential) {
		errorJSON(w, 400, "invalid_request")
		return
	}
	c, err := a.takeCeremony(r.Context(), input.CeremonyID, "login")
	if err != nil {
		errorJSON(w, 400, "ceremony_expired_or_used")
		return
	}
	user, credential, err := a.wa.FinishPasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		return a.loadPasskeyUser(r.Context(), rawID, userHandle)
	}, c.session, credentialRequest(r, input.Credential))
	if err != nil {
		errorJSON(w, 401, "invalid_passkey_response")
		return
	}
	playerID := string(user.WebAuthnID())
	data, err := json.Marshal(credential)
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	token, hash, err := newToken()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `UPDATE credentials SET data=$1 WHERE id=$2 AND player_id=$3`, json.RawMessage(data), credential.ID, playerID)
	if err == nil {
		tag, execErr := tx.Exec(r.Context(), `INSERT INTO devices(id, player_id) VALUES($1,$2)
			ON CONFLICT (id) DO UPDATE SET last_seen_at=now() WHERE devices.player_id=EXCLUDED.player_id`, c.deviceID, playerID)
		err = execErr
		if err == nil && tag.RowsAffected() != 1 {
			err = errors.New("device belongs to another player")
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO sessions(token_hash, player_id, expires_at) VALUES($1,$2,now()+interval '30 days')`, hash[:], playerID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		errorJSON(w, 409, "device_conflict")
		return
	}
	writeJSON(w, 200, map[string]any{"playerId": playerID, "token": token, "expiresIn": int(sessionLifetime.Seconds())})
}

func newToken() (string, [32]byte, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", [32]byte{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	return token, sha256.Sum256(random[:]), nil
}

type playerContextKey struct{}

func (a *app) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			errorJSON(w, 401, "unauthorized")
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(header, prefix))
		if err != nil || len(raw) != 32 {
			errorJSON(w, 401, "unauthorized")
			return
		}
		hash := sha256.Sum256(raw)
		var playerID string
		err = a.db.QueryRow(r.Context(), `SELECT player_id::text FROM sessions WHERE token_hash=$1 AND expires_at > now()`, hash[:]).Scan(&playerID)
		if err != nil {
			errorJSON(w, 401, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), playerContextKey{}, playerID)))
	})
}

func currentPlayer(r *http.Request) string {
	id, _ := r.Context().Value(playerContextKey{}).(string)
	return id
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	hash := sha256.Sum256(raw)
	if _, err := a.db.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, hash[:]); err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
