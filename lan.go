package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/003_lan.sql
var lanSchema string

func ensureLanSchema(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(74910303)`); err != nil {
		return err
	}
	var installed bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass('lan_sessions') IS NOT NULL`).Scan(&installed); err != nil {
		return err
	}
	if !installed {
		if _, err = tx.Exec(ctx, lanSchema); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type lanSession struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type lanInvite struct {
	ID        string `json:"id"`
	HostID    string `json:"hostId"`
	GuestID   string `json:"guestId"`
	HostName  string `json:"hostName"`
	GuestName string `json:"guestName"`
	State     string `json:"state"`
}

type lanSignal struct {
	Kind        string `json:"kind"`
	SignalID    string `json:"signalId"`
	Code        string `json:"code"`
	SDP         string `json:"sdp"`
	WifiAddress string `json:"wifiAddress,omitempty"`
}

func clientIP(r *http.Request) net.IP {
	address := ""
	if os.Getenv("VERCEL") != "" {
		address = strings.TrimSpace(r.Header.Get("X-Vercel-Forwarded-For"))
		if strings.Contains(address, ",") {
			return nil
		}
	} else {
		address, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	return net.ParseIP(address)
}

func (a *app) createLanSession(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len([]rune(input.Name)) > 24 || strings.ContainsAny(input.Name, "\r\n\x00") {
		errorJSON(w, 400, "invalid_name")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		errorJSON(w, 400, "network_unavailable")
		return
	}
	ctx := r.Context()
	_, _ = a.db.Exec(ctx, `DELETE FROM lan_sessions WHERE expires_at < now() OR active_at < now() - interval '2 minutes'`)
	var active int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM lan_sessions WHERE public_ip=$1 AND (active_at > now() - interval '20 seconds' OR created_at > now() - interval '1 minute')`, ip.String()).Scan(&active); err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	if active >= 32 {
		errorJSON(w, 429, "too_many_players")
		return
	}
	id, err := newUUID()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	token, hash, err := newToken()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	_, err = a.db.Exec(ctx, `INSERT INTO lan_sessions(id,token_hash,public_ip,name) VALUES($1,$2,$3,$4)`, id, hash[:], ip.String(), input.Name)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "token": token})
}

func (a *app) lanCaller(w http.ResponseWriter, r *http.Request) (string, bool) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		errorJSON(w, 401, "unauthorized")
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil || len(raw) != 32 {
		errorJSON(w, 401, "unauthorized")
		return "", false
	}
	ip := clientIP(r)
	if ip == nil {
		errorJSON(w, 401, "unauthorized")
		return "", false
	}
	hash := sha256.Sum256(raw)
	var id string
	err = a.db.QueryRow(r.Context(), `SELECT id::text FROM lan_sessions WHERE token_hash=$1 AND public_ip=$2 AND expires_at>now()`, hash[:], ip.String()).Scan(&id)
	if err != nil {
		errorJSON(w, 401, "unauthorized")
		return "", false
	}
	return id, true
}

func (a *app) lanState(w http.ResponseWriter, r *http.Request) {
	id, ok := a.lanCaller(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	_, err := a.db.Exec(ctx, `UPDATE lan_sessions SET active_at=now(), expires_at=now()+interval '30 minutes' WHERE id=$1`, id)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	rows, err := a.db.Query(ctx, `SELECT other.id::text,other.name FROM lan_sessions self JOIN lan_sessions other ON other.public_ip=self.public_ip
		WHERE self.id=$1 AND other.id<>$1 AND other.active_at>now()-interval '15 seconds' AND other.expires_at>now()
		AND NOT EXISTS (SELECT 1 FROM lan_invites busy WHERE (busy.host_id=other.id OR busy.guest_id=other.id)
			AND busy.state IN ('pending','accepted') AND busy.expires_at>now())
		ORDER BY other.active_at DESC LIMIT 15`, id)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	peers := []lanSession{}
	for rows.Next() {
		var peer lanSession
		if err = rows.Scan(&peer.ID, &peer.Name); err != nil {
			break
		}
		peers = append(peers, peer)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	rows, err = a.db.Query(ctx, `SELECT i.id::text,i.host_id::text,i.guest_id::text,h.name,g.name,i.state
		FROM lan_invites i JOIN lan_sessions h ON h.id=i.host_id JOIN lan_sessions g ON g.id=i.guest_id
		WHERE (i.host_id=$1 OR i.guest_id=$1) AND i.expires_at>now() AND i.state IN ('pending','accepted')
		ORDER BY i.created_at DESC LIMIT 8`, id)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	invites := []lanInvite{}
	for rows.Next() {
		var invite lanInvite
		if err = rows.Scan(&invite.ID, &invite.HostID, &invite.GuestID, &invite.HostName, &invite.GuestName, &invite.State); err != nil {
			break
		}
		invites = append(invites, invite)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	var signal *lanSignal
	if len(invites) > 0 && invites[0].State == "accepted" {
		var item lanSignal
		err = a.db.QueryRow(ctx, `SELECT kind,signal_id::text,room_code,sdp,coalesce(wifi_address,'') FROM lan_signals WHERE invite_id=$1 AND sender_id<>$2 ORDER BY id DESC LIMIT 1`, invites[0].ID, id).Scan(&item.Kind, &item.SignalID, &item.Code, &item.SDP, &item.WifiAddress)
		if err == nil {
			signal = &item
		} else if !errors.Is(err, pgx.ErrNoRows) {
			errorJSON(w, 500, "database_error")
			return
		}
	}
	writeJSON(w, 200, map[string]any{"id": id, "peers": peers, "invites": invites, "signal": signal})
}

func (a *app) createLanInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := a.lanCaller(w, r)
	if !ok {
		return
	}
	var input struct {
		PeerID string `json:"peerId"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if !validUUID(input.PeerID) || input.PeerID == id {
		errorJSON(w, 400, "invalid_peer")
		return
	}
	inviteID, err := newUUID()
	if err != nil {
		errorJSON(w, 500, "internal_error")
		return
	}
	var recent int
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM lan_invites WHERE host_id=$1 AND created_at>now()-interval '1 minute'`, id).Scan(&recent); err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	if recent >= 6 {
		errorJSON(w, 429, "too_many_invites")
		return
	}
	tag, err := a.db.Exec(r.Context(), `INSERT INTO lan_invites(id,host_id,guest_id)
		SELECT $1,self.id,other.id FROM lan_sessions self JOIN lan_sessions other ON other.id=$3 AND other.public_ip=self.public_ip
		WHERE self.id=$2 AND other.active_at>now()-interval '15 seconds' AND other.expires_at>now()
		AND NOT EXISTS (SELECT 1 FROM lan_invites i WHERE (i.host_id IN ($2,$3) OR i.guest_id IN ($2,$3)) AND i.state IN ('pending','accepted') AND i.expires_at>now())`, inviteID, id, input.PeerID)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	if tag.RowsAffected() != 1 {
		errorJSON(w, 409, "peer_busy_or_unavailable")
		return
	}
	writeJSON(w, 201, map[string]string{"id": inviteID})
}

func (a *app) respondLanInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := a.lanCaller(w, r)
	if !ok {
		return
	}
	var input struct {
		InviteID string `json:"inviteId"`
		Accept   bool   `json:"accept"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if !validUUID(input.InviteID) {
		errorJSON(w, 400, "invalid_invite")
		return
	}
	state := "declined"
	if input.Accept {
		state = "accepted"
	}
	tag, err := a.db.Exec(r.Context(), `UPDATE lan_invites SET state=$3 WHERE id=$1 AND guest_id=$2 AND state='pending' AND expires_at>now()`, input.InviteID, id, state)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	if tag.RowsAffected() != 1 {
		errorJSON(w, 409, "invite_unavailable")
		return
	}
	w.WriteHeader(204)
}

func (a *app) cancelLanInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := a.lanCaller(w, r)
	if !ok {
		return
	}
	var input struct {
		InviteID string `json:"inviteId"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if !validUUID(input.InviteID) {
		errorJSON(w, 400, "invalid_invite")
		return
	}
	_, err := a.db.Exec(r.Context(), `UPDATE lan_invites SET state='cancelled'
		WHERE id=$1 AND (host_id=$2 OR guest_id=$2) AND state IN ('pending','accepted')`, input.InviteID, id)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	w.WriteHeader(204)
}

func (a *app) writeLanSignal(w http.ResponseWriter, r *http.Request) {
	id, ok := a.lanCaller(w, r)
	if !ok {
		return
	}
	var input struct {
		InviteID    string `json:"inviteId"`
		Kind        string `json:"kind"`
		SignalID    string `json:"signalId"`
		Code        string `json:"code"`
		SDP         string `json:"sdp"`
		WifiAddress string `json:"wifiAddress"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if !validUUID(input.InviteID) || !validUUID(input.SignalID) || (input.Kind != "offer" && input.Kind != "answer") || len(input.Code) != 6 || len(input.SDP) > 12000 ||
		!strings.Contains(input.SDP, "m=application ") || !strings.Contains(input.SDP, " typ host") {
		errorJSON(w, 400, "invalid_signal")
		return
	}
	for _, c := range input.Code {
		if !strings.ContainsRune("23456789ABCDEFGHJKLMNPQRSTUVWXYZ", c) {
			errorJSON(w, 400, "invalid_signal")
			return
		}
	}
	if input.WifiAddress != "" {
		ip := net.ParseIP(input.WifiAddress)
		if ip == nil || ip.To4() == nil || !ip.IsPrivate() {
			errorJSON(w, 400, "invalid_signal")
			return
		}
	}
	for _, line := range strings.Split(input.SDP, "\n") {
		if strings.HasPrefix(line, "a=candidate:") && !strings.Contains(line, " typ host") {
			errorJSON(w, 400, "invalid_signal")
			return
		}
	}
	tag, err := a.db.Exec(r.Context(), `INSERT INTO lan_signals(invite_id,sender_id,kind,signal_id,room_code,sdp,wifi_address)
		SELECT i.id,$2,$3,$4,$5,$6,$7 FROM lan_invites i WHERE i.id=$1 AND i.state='accepted' AND i.expires_at>now()
		AND (($3='offer' AND i.host_id=$2) OR ($3='answer' AND i.guest_id=$2))
		ON CONFLICT (invite_id,sender_id,kind) DO NOTHING`, input.InviteID, id, input.Kind, input.SignalID, input.Code, input.SDP, input.WifiAddress)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	if tag.RowsAffected() != 1 {
		errorJSON(w, 409, "signal_unavailable")
		return
	}
	w.WriteHeader(204)
}

func (a *app) closeLanSession(w http.ResponseWriter, r *http.Request) {
	id, ok := a.lanCaller(w, r)
	if !ok {
		return
	}
	_, err := a.db.Exec(r.Context(), `DELETE FROM lan_sessions WHERE id=$1`, id)
	if err != nil {
		errorJSON(w, 500, "database_error")
		return
	}
	w.WriteHeader(204)
}
