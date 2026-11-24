package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLanDiscoveryInviteAndSignal(t *testing.T) {
	a, cleanup := testApp(t)
	defer cleanup()
	h := a.routes()
	create := func(name string) (string, string) {
		t.Helper()
		w := call(t, h, "POST", "/v1/lan/session", "", `{"name":"`+name+`"}`)
		if w.Code != 201 {
			t.Fatalf("create session: %d %s", w.Code, w.Body.String())
		}
		var session struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
		return session.ID, session.Token
	}
	hostID, hostToken := create("Laptop")
	guestID, guestToken := create("Phone")
	state := call(t, h, "GET", "/v1/lan/state", hostToken, "")
	if state.Code != 200 || !strings.Contains(state.Body.String(), guestID) {
		t.Fatalf("guest not discovered: %d %s", state.Code, state.Body.String())
	}
	w := call(t, h, "POST", "/v1/lan/invite", hostToken, `{"peerId":"`+guestID+`"}`)
	if w.Code != 201 {
		t.Fatalf("invite: %d %s", w.Code, w.Body.String())
	}
	var invitation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &invitation); err != nil {
		t.Fatal(err)
	}
	if w = call(t, h, "POST", "/v1/lan/respond", guestToken, `{"inviteId":"`+invitation.ID+`","accept":true}`); w.Code != 204 {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	sdp := "v=0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\na=candidate:1 1 udp 1 192.168.1.2 5000 typ host\r\n"
	signalID := newTestUUID(t)
	message, _ := json.Marshal(map[string]string{"inviteId": invitation.ID, "kind": "offer", "signalId": signalID, "code": "ABC234", "sdp": sdp})
	if w = call(t, h, "POST", "/v1/lan/signal", guestToken, string(message)); w.Code != 409 {
		t.Fatalf("guest could send host offer: %d", w.Code)
	}
	if w = call(t, h, "POST", "/v1/lan/signal", hostToken, string(message)); w.Code != 204 {
		t.Fatalf("host offer: %d %s", w.Code, w.Body.String())
	}
	state = call(t, h, "GET", "/v1/lan/state", guestToken, "")
	if state.Code != 200 || !strings.Contains(state.Body.String(), signalID) || !strings.Contains(state.Body.String(), hostID) {
		t.Fatalf("offer missing: %d %s", state.Code, state.Body.String())
	}
	message, _ = json.Marshal(map[string]string{"inviteId": invitation.ID, "kind": "answer", "signalId": signalID, "code": "ABC234", "sdp": sdp})
	if w = call(t, h, "POST", "/v1/lan/signal", guestToken, string(message)); w.Code != 204 {
		t.Fatalf("guest answer: %d %s", w.Code, w.Body.String())
	}
	state = call(t, h, "GET", "/v1/lan/state", hostToken, "")
	if state.Code != 200 || !strings.Contains(state.Body.String(), `"kind":"answer"`) {
		t.Fatalf("answer missing: %d %s", state.Code, state.Body.String())
	}
	if w = call(t, h, "POST", "/v1/lan/cancel", hostToken, `{"inviteId":"`+invitation.ID+`"}`); w.Code != 204 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	state = call(t, h, "GET", "/v1/lan/state", guestToken, "")
	if strings.Contains(state.Body.String(), invitation.ID) {
		t.Fatalf("cancelled invite still visible: %s", state.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/lan/state", nil)
	r.RemoteAddr = "198.51.100.2:5555"
	r.Header.Set("Authorization", "Bearer "+hostToken)
	denied := httptest.NewRecorder()
	h.ServeHTTP(denied, r)
	if denied.Code != 401 {
		t.Fatalf("session allowed from changed network: %d", denied.Code)
	}
}
