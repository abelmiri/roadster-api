package main

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const raceTarget = 1000.0
const raceLength = 3 * time.Minute
const raceCountdown = 3 * time.Second

var racePaints = [...]string{"blue", "orange", "pink", "lime"}

type gamePose struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Angle float64 `json:"angle"`
}
type gameDrive struct {
	Car      gamePose    `json:"car"`
	Wheels   [2]gamePose `json:"wheels"`
	Distance float64     `json:"distance"`
	Crashed  bool        `json:"crashed"`
}
type gameInput struct {
	Type     string     `json:"type"`
	Name     string     `json:"name"`
	Target   int        `json:"target"`
	Room     string     `json:"room"`
	Round    int        `json:"round"`
	Car      *gamePose  `json:"car"`
	Wheels   []gamePose `json:"wheels"`
	Distance float64    `json:"distance"`
	Crashed  bool       `json:"crashed"`
}
type gamePlayer struct {
	ID        int        `json:"id"`
	Name      string     `json:"name"`
	Paint     string     `json:"paint"`
	Drive     *gameDrive `json:"drive"`
	Connected bool       `json:"connected"`
	lastDrive time.Time
}
type gameRoom struct {
	code     string
	host     int
	phase    string
	round    int
	startsAt time.Time
	endsAt   time.Time
	winner   *int
	players  map[int]*gamePlayer
	order    []int
}
type gameInvite struct {
	room    string
	host    int
	expires time.Time
}
type gameClient struct {
	id   int
	name string
	room *gameRoom
	conn *websocket.Conn
	out  chan []byte
}
type gameHub struct {
	mu      sync.Mutex
	clients map[int]*gameClient
	rooms   map[string]*gameRoom
	invites map[int]gameInvite
	nextID  int
}

func newGameHub() *gameHub {
	h := &gameHub{clients: make(map[int]*gameClient), rooms: make(map[string]*gameRoom), invites: make(map[int]gameInvite)}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for now := range ticker.C {
			h.tick(now)
		}
	}()
	return h
}

func gameName(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" || len([]rune(name)) > 24 || strings.ContainsAny(name, "\x00\r\n\t") {
		return ""
	}
	return name
}

func (a *app) gameSocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{HandshakeTimeout: 5 * time.Second,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			return origin == a.origin || (a.devWA != nil && origin == a.devOrigin)
		}}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &gameClient{conn: conn, out: make(chan []byte, 64)}
	a.hub.mu.Lock()
	a.hub.nextID++
	client.id = a.hub.nextID
	a.hub.clients[client.id] = client
	a.hub.send(client, map[string]any{"type": "hello", "id": client.id})
	a.hub.mu.Unlock()
	go client.writePump()
	client.readPump(a.hub)
}

func (c *gameClient) writePump() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	defer c.conn.Close()
	for {
		select {
		case message, ok := <-c.out:
			if !ok {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *gameClient) readPump(h *gameHub) {
	defer h.disconnect(c)
	defer c.conn.Close()
	c.conn.SetReadLimit(2048)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error { return c.conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	window, count := time.Now(), 0
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if time.Since(window) >= time.Second {
			window, count = time.Now(), 0
		}
		count++
		if count > 35 {
			return
		}
		var input gameInput
		if json.Unmarshal(data, &input) != nil {
			continue
		}
		h.handle(c, input, time.Now())
	}
}

// Every hub operation runs while holding mu. Each client has one writer goroutine.
func (h *gameHub) send(c *gameClient, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	select {
	case c.out <- data:
	default:
		_ = c.conn.Close()
	}
}

func (h *gameHub) sendBytes(c *gameClient, data []byte) {
	select {
	case c.out <- data:
	default:
		_ = c.conn.Close()
	}
}

func (h *gameHub) presence() {
	available := make([]map[string]any, 0)
	for _, c := range h.clients {
		if c.name != "" && c.room == nil {
			available = append(available, map[string]any{"id": c.id, "name": c.name})
		}
	}
	data, _ := json.Marshal(map[string]any{"type": "presence", "players": available})
	for _, c := range h.clients {
		if c.name != "" {
			h.sendBytes(c, data)
		}
	}
}

func (r *gameRoom) snapshot(now time.Time) map[string]any {
	players := make([]*gamePlayer, 0, len(r.order))
	for _, id := range r.order {
		if p := r.players[id]; p != nil {
			players = append(players, p)
		}
	}
	return map[string]any{"type": "room", "code": r.code, "host": r.host, "phase": r.phase,
		"round": r.round, "serverTime": now.UnixMilli(), "startsAt": r.startsAt.UnixMilli(),
		"endsAt": r.endsAt.UnixMilli(), "target": raceTarget, "winner": r.winner, "players": players}
}

func (h *gameHub) broadcastRoom(r *gameRoom, now time.Time) {
	data, err := json.Marshal(r.snapshot(now))
	if err != nil {
		return
	}
	for id := range r.players {
		if c := h.clients[id]; c != nil {
			h.sendBytes(c, data)
		}
	}
}

func (h *gameHub) notice(c *gameClient, code string) {
	h.send(c, map[string]any{"type": "notice", "code": code})
}

func (h *gameHub) handle(c *gameClient, input gameInput, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.id] != c {
		return
	}
	if input.Type == "identify" {
		if c.name != "" {
			return
		}
		name := gameName(input.Name)
		if name == "" {
			h.notice(c, "invalid_name")
			return
		}
		c.name = name
		h.presence()
		return
	}
	if c.name == "" {
		return
	}
	switch input.Type {
	case "invite":
		h.invite(c, input.Target, now)
	case "accept":
		h.respond(c, input.Room, true, now)
	case "decline":
		h.respond(c, input.Room, false, now)
	case "start":
		h.start(c, now)
	case "rematch":
		h.rematch(c, now)
	case "leave":
		h.leave(c, now)
	case "drive":
		h.drive(c, input, now)
	}
}

func (h *gameHub) invite(host *gameClient, targetID int, now time.Time) {
	target := h.clients[targetID]
	if target == nil || target == host || target.name == "" || target.room != nil {
		h.notice(host, "player_unavailable")
		return
	}
	if pending, ok := h.invites[targetID]; ok && now.Before(pending.expires) {
		h.notice(host, "player_unavailable")
		return
	}
	r := host.room
	if r != nil && (r.host != host.id || r.phase != "lobby") {
		h.notice(host, "room_closed")
		return
	}
	if r == nil {
		id, err := newUUID()
		if err != nil {
			h.notice(host, "server_error")
			return
		}
		r = &gameRoom{code: id, host: host.id, phase: "lobby", players: make(map[int]*gamePlayer)}
		h.rooms[id] = r
		h.addPlayer(r, host)
		h.broadcastRoom(r, now)
		h.presence()
	}
	h.invites[targetID] = gameInvite{room: r.code, host: host.id, expires: now.Add(45 * time.Second)}
	h.send(target, map[string]any{"type": "invitation", "room": r.code, "host": host.id, "name": host.name})
	h.notice(host, "invitation_sent")
}

func (h *gameHub) addPlayer(r *gameRoom, c *gameClient) {
	paint := racePaints[len(r.order)%len(racePaints)]
	r.players[c.id] = &gamePlayer{ID: c.id, Name: c.name, Paint: paint, Connected: true}
	r.order = append(r.order, c.id)
	c.room = r
}

func (h *gameHub) respond(c *gameClient, roomID string, accept bool, now time.Time) {
	pending, ok := h.invites[c.id]
	if !ok || pending.room != roomID || now.After(pending.expires) {
		h.notice(c, "invitation_expired")
		return
	}
	delete(h.invites, c.id)
	r := h.rooms[roomID]
	if r == nil || r.phase != "lobby" || r.host != pending.host || c.room != nil {
		h.notice(c, "room_closed")
		return
	}
	if !accept {
		if host := h.clients[pending.host]; host != nil {
			h.notice(host, "invitation_declined")
		}
		return
	}
	h.addPlayer(r, c)
	h.broadcastRoom(r, now)
	h.presence()
}

func (h *gameHub) start(c *gameClient, now time.Time) {
	r := c.room
	if r == nil || r.host != c.id || r.phase != "lobby" || len(r.players) < 2 {
		return
	}
	r.round++
	r.phase = "countdown"
	r.startsAt = now.Add(raceCountdown)
	r.endsAt = r.startsAt.Add(raceLength)
	r.winner = nil
	for _, p := range r.players {
		p.Drive = nil
		p.lastDrive = time.Time{}
	}
	h.broadcastRoom(r, now)
}

func (h *gameHub) rematch(c *gameClient, now time.Time) {
	r := c.room
	if r == nil || r.host != c.id || r.phase != "results" {
		return
	}
	r.phase = "lobby"
	r.winner = nil
	for _, p := range r.players {
		p.Drive = nil
	}
	h.broadcastRoom(r, now)
}

func validPose(p gamePose) bool {
	return !math.IsNaN(p.X) && !math.IsNaN(p.Y) && !math.IsNaN(p.Angle) &&
		!math.IsInf(p.X, 0) && !math.IsInf(p.Y, 0) && !math.IsInf(p.Angle, 0) &&
		math.Abs(p.X) <= raceTarget+50 && math.Abs(p.Y) <= 300 && math.Abs(p.Angle) <= 10000
}

func (h *gameHub) drive(c *gameClient, input gameInput, now time.Time) {
	r := c.room
	if r == nil || r.phase != "racing" || input.Round != r.round {
		return
	}
	p := r.players[c.id]
	if p == nil || p.Drive != nil && p.Drive.Crashed ||
		input.Car == nil || len(input.Wheels) != 2 || !validPose(*input.Car) ||
		!validPose(input.Wheels[0]) || !validPose(input.Wheels[1]) ||
		math.IsNaN(input.Distance) || math.IsInf(input.Distance, 0) || input.Distance < 0 {
		return
	}
	if !input.Crashed && now.Sub(p.lastDrive) < 40*time.Millisecond {
		return
	}
	distance := math.Min(raceTarget, input.Distance)
	if p.Drive != nil {
		distance = math.Max(distance, p.Drive.Distance)
	}
	drive := &gameDrive{Car: *input.Car, Wheels: [2]gamePose{input.Wheels[0], input.Wheels[1]}, Distance: distance, Crashed: input.Crashed}
	p.Drive = drive
	p.lastDrive = now
	message, _ := json.Marshal(map[string]any{"type": "drive", "id": c.id, "round": r.round, "drive": drive})
	for id := range r.players {
		if member := h.clients[id]; member != nil {
			h.sendBytes(member, message)
		}
	}
	if distance >= raceTarget {
		winner := c.id
		h.finish(r, now, &winner)
		return
	}
	allCrashed := true
	for _, member := range r.players {
		if member.Drive == nil || !member.Drive.Crashed {
			allCrashed = false
			break
		}
	}
	if allCrashed {
		h.finish(r, now, nil)
	}
}

func (h *gameHub) finish(r *gameRoom, now time.Time, winner *int) {
	if r.phase == "results" {
		return
	}
	if winner == nil {
		best := -1.0
		tie := false
		for _, p := range r.players {
			distance := 0.0
			if p.Drive != nil {
				distance = p.Drive.Distance
			}
			if distance > best {
				best = distance
				id := p.ID
				winner = &id
				tie = false
			} else if distance == best {
				tie = true
			}
		}
		if tie {
			winner = nil
		}
	}
	r.phase = "results"
	r.winner = winner
	h.broadcastRoom(r, now)
}

func (h *gameHub) leave(c *gameClient, now time.Time) {
	r := c.room
	if r == nil {
		return
	}
	delete(r.players, c.id)
	c.room = nil
	for i, id := range r.order {
		if id == c.id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	if len(r.players) == 0 {
		delete(h.rooms, r.code)
		h.presence()
		return
	}
	if r.host == c.id {
		r.host = r.order[0]
	}
	if r.phase == "countdown" {
		r.phase = "lobby"
	}
	if r.phase == "racing" && len(r.players) < 2 {
		h.finish(r, now, nil)
	} else {
		h.broadcastRoom(r, now)
	}
	h.presence()
}

func (h *gameHub) disconnect(c *gameClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.id] != c {
		return
	}
	delete(h.clients, c.id)
	close(c.out)
	delete(h.invites, c.id)
	for id, invite := range h.invites {
		if invite.host == c.id {
			delete(h.invites, id)
			if guest := h.clients[id]; guest != nil {
				h.notice(guest, "invitation_expired")
			}
		}
	}
	h.leave(c, time.Now())
	h.presence()
}

func (h *gameHub) tick(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, invite := range h.invites {
		if now.After(invite.expires) {
			delete(h.invites, id)
			if c := h.clients[id]; c != nil {
				h.notice(c, "invitation_expired")
			}
		}
	}
	for _, r := range h.rooms {
		if r.phase == "countdown" && !now.Before(r.startsAt) {
			r.phase = "racing"
			h.broadcastRoom(r, now)
		}
		if r.phase == "racing" && !now.Before(r.endsAt) {
			h.finish(r, now, nil)
		}
	}
}
