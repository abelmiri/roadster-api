package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func gameRead(t *testing.T, c *websocket.Conn, kind string) map[string]any {
	t.Helper()
	for {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var message map[string]any
		if err := c.ReadJSON(&message); err != nil {
			t.Fatalf("read %s: %v", kind, err)
		}
		if message["type"] == kind {
			return message
		}
	}
}

func gameSend(t *testing.T, c *websocket.Conn, message any) {
	t.Helper()
	if err := c.WriteJSON(message); err != nil {
		t.Fatal(err)
	}
}

func TestGameSocketAllowsSixPlayersAndBroadcastsRace(t *testing.T) {
	a := &app{origin: "https://roadster-game-ten.vercel.app", hub: newGameHub()}
	server := httptest.NewServer(a.routes())
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/game/socket"
	header := http.Header{"Origin": []string{a.origin}}
	clients := make([]*websocket.Conn, 6)
	ids := make([]int, len(clients))
	for i := range clients {
		conn, _, err := websocket.DefaultDialer.Dial(url, header)
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = conn
		defer conn.Close()
		ids[i] = int(gameRead(t, conn, "hello")["id"].(float64))
		gameSend(t, conn, map[string]any{"type": "identify", "name": fmt.Sprintf("Player %d", i)})
	}
	if players := gameRead(t, clients[0], "presence")["players"].([]any); len(players) == 0 {
		t.Fatal("online presence was empty")
	}
	var room string
	for i := 1; i < len(clients); i++ {
		gameSend(t, clients[0], map[string]any{"type": "invite", "target": ids[i]})
		invitation := gameRead(t, clients[i], "invitation")
		room = invitation["room"].(string)
		gameSend(t, clients[i], map[string]any{"type": "accept", "room": room})
		for {
			players := gameRead(t, clients[0], "room")["players"].([]any)
			if len(players) == i+1 {
				break
			}
			if len(players) > i+1 {
				t.Fatalf("after accept %d, room has %d players", i, len(players))
			}
		}
	}
	gameSend(t, clients[0], map[string]any{"type": "start"})
	started := gameRead(t, clients[0], "room")
	if started["phase"] != "countdown" || len(started["players"].([]any)) != 6 {
		t.Fatalf("unexpected start: %+v", started)
	}
	a.hub.tick(time.Now().Add(4 * time.Second))
	if racing := gameRead(t, clients[0], "room"); racing["phase"] != "racing" {
		t.Fatalf("expected racing: %+v", racing)
	}
	pose := map[string]float64{"x": 1000, "y": 0, "angle": 0}
	gameSend(t, clients[5], map[string]any{"type": "drive", "round": 1, "car": pose,
		"wheels": []any{pose, pose}, "distance": 1000, "crashed": false})
	if drive := gameRead(t, clients[0], "drive"); int(drive["id"].(float64)) != ids[5] {
		t.Fatalf("wrong driver's state: %+v", drive)
	}
	result := gameRead(t, clients[0], "room")
	if result["phase"] != "results" || int(result["winner"].(float64)) != ids[5] {
		t.Fatalf("unexpected result: %+v", result)
	}
}
