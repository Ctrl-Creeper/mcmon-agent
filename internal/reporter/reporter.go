package reporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
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

type MetricResult struct {
	TargetID string   `json:"target_id"`
	Metric   string   `json:"metric"`
	Ts       int64    `json:"ts"`
	Value    *float64 `json:"value"`
	Extra    string   `json:"extra,omitempty"`
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

	mu         sync.Mutex
	conn       *websocket.Conn
	writeMu    sync.Mutex // serializes WriteMessage on conn; gorilla forbids concurrent writers
	done       chan struct{}
	httpClient *http.Client
}

// writeWS serializes writes to the active conn. Callers must NOT hold r.mu.
// Returns an error if the conn is nil or the write fails — the caller
// decides whether to fall back to HTTP.
func (r *Reporter) writeWS(c *websocket.Conn, data []byte) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if c == nil {
		return fmt.Errorf("ws not connected")
	}
	return c.WriteMessage(websocket.TextMessage, data)
}

func New(hostURL, token string, targets []Target) *Reporter {
	return &Reporter{
		hostURL: hostURL,
		token:   token,
		targets: targets,
		done:    make(chan struct{}),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
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
		if err := r.postRPC("post", "agent.pingResult", pr); err != nil {
			log.Printf("post pingResult: %v", err)
		}
		return
	}

	msg := rpcRequest{JSONRPC: "2.0", Method: "agent.pingResult", Params: pr}
	data, _ := json.Marshal(msg)
	if err := r.writeWS(c, data); err != nil {
		log.Printf("send pingResult: %v", err)
		if err := r.postRPC("post", "agent.pingResult", pr); err != nil {
			log.Printf("post pingResult: %v", err)
		}
	}
}

func (r *Reporter) SendMetricResult(mr MetricResult) {
	r.mu.Lock()
	c := r.conn
	r.mu.Unlock()

	if c == nil {
		if err := r.postRPC("post", "agent.metricResult", mr); err != nil {
			log.Printf("post metricResult: %v", err)
		}
		return
	}

	msg := rpcRequest{JSONRPC: "2.0", Method: "agent.metricResult", Params: mr}
	data, _ := json.Marshal(msg)
	if err := r.writeWS(c, data); err != nil {
		log.Printf("send metricResult: %v", err)
		if err := r.postRPC("post", "agent.metricResult", mr); err != nil {
			log.Printf("post metricResult: %v", err)
		}
	}
}

func (r *Reporter) connect() error {
	url := strings.TrimSuffix(r.hostURL, "/") + "/api/agents/v2/rpc"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+r.token)
	ws, _, err := websocket.DefaultDialer.Dial(url, headers)
	if err != nil {
		return err
	}

	// Send agent.hello BEFORE publishing the conn to other goroutines.
	// Otherwise a concurrent SendPingResult could grab writeMu first and
	// arrive at the host before hello — leaving orphan samples on the
	// very first connect (before targets are registered).
	hello := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "agent.hello",
		Params:  agentHello{Version: "1.0", Targets: r.targets},
	}
	data, _ := json.Marshal(hello)
	r.writeMu.Lock()
	err = ws.WriteMessage(websocket.TextMessage, data)
	r.writeMu.Unlock()
	if err != nil {
		ws.Close()
		return err
	}

	r.mu.Lock()
	r.conn = ws
	r.mu.Unlock()

	log.Printf("connected to host, sent hello with %d targets", len(r.targets))
	return nil
}

func (r *Reporter) postRPC(id any, method string, params any) error {
	msg := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	url := strings.TrimSuffix(httpURL(r.hostURL), "/") + "/api/agents/v2/rpc"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("host returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func httpURL(raw string) string {
	if strings.HasPrefix(raw, "ws://") {
		return "http://" + strings.TrimPrefix(raw, "ws://")
	}
	if strings.HasPrefix(raw, "wss://") {
		return "https://" + strings.TrimPrefix(raw, "wss://")
	}
	return raw
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
