package reporter

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	reconnectDelay = 5 * time.Second
	pongWait       = 90 * time.Second
)

type Target struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

type PingResult struct {
	TargetID string   `json:"target_id"`
	Ts       int64    `json:"ts"`
	MinMs    *float64 `json:"min_ms"`
	P50Ms    *float64 `json:"p50_ms"`
	MaxMs    *float64 `json:"max_ms"`
	LossPct  float64  `json:"loss_pct"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type agentHello struct {
	Version string   `json:"version"`
	Targets []Target `json:"targets"`
}

type Reporter struct {
	hostURL string
	token   string
	targets []Target

	mu   sync.Mutex
	conn *websocket.Conn
	done chan struct{}
}

func New(hostURL, token string, targets []Target) *Reporter {
	return &Reporter{
		hostURL: hostURL,
		token:   token,
		targets: targets,
		done:    make(chan struct{}),
	}
}

func (r *Reporter) Run() {
	for {
		select {
		case <-r.done:
			return
		default:
		}

		err := r.connect()
		if err != nil {
			log.Printf("ws connect failed: %v — retrying in %v", err, reconnectDelay)
			time.Sleep(reconnectDelay)
			continue
		}

		r.readLoop()
		log.Printf("ws disconnected — reconnecting in %v", reconnectDelay)
		time.Sleep(reconnectDelay)
	}
}

func (r *Reporter) Stop() {
	close(r.done)
	r.mu.Lock()
	if r.conn != nil {
		r.conn.Close()
	}
	r.mu.Unlock()
}

func (r *Reporter) SendPingResult(pr PingResult) {
	r.mu.Lock()
	c := r.conn
	r.mu.Unlock()

	if c == nil {
		return
	}

	msg := rpcRequest{JSONRPC: "2.0", Method: "agent.pingResult", Params: pr}
	data, _ := json.Marshal(msg)
	if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("send pingResult: %v", err)
	}
}

func (r *Reporter) connect() error {
	url := fmt.Sprintf("%s/api/ws?token=%s", r.hostURL, r.token)
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.conn = ws
	r.mu.Unlock()

	hello := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "agent.hello",
		Params:  agentHello{Version: "1.0", Targets: r.targets},
	}
	data, _ := json.Marshal(hello)
	if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
		ws.Close()
		return err
	}

	log.Printf("connected to host, sent hello with %d targets", len(r.targets))
	return nil
}

func (r *Reporter) readLoop() {
	r.mu.Lock()
	ws := r.conn
	r.mu.Unlock()

	ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			return
		}
	}
}
