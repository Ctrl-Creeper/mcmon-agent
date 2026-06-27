package reporter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestConnectUsesBearerHeaderOnV2Endpoint(t *testing.T) {
	var gotAuth string
	var gotPath string
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read hello: %v", err)
			return
		}
		var req rpcRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			t.Errorf("bad hello json: %v", err)
		}
		if req.Method != "agent.hello" {
			t.Errorf("hello method = %q, want agent.hello", req.Method)
		}
		_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": "ok"})
	}))
	defer server.Close()

	r := New(strings.Replace(server.URL, "http://", "ws://", 1), "secret-token", []Target{{ID: "target-1", Name: "Target", Host: "mc.example.com", Port: 25565}})
	if err := r.connect(); err != nil {
		t.Fatal(err)
	}
	r.Stop()

	if gotPath != "/api/agents/v2/rpc" {
		t.Fatalf("ws path = %q, want /api/agents/v2/rpc", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want Bearer token", gotAuth)
	}
}

func TestSendPingResultPostsWhenWebSocketIsDisconnected(t *testing.T) {
	var gotAuth string
	var gotMethod string
	var gotTarget string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotMethod = req.Method
		b, _ := json.Marshal(req.Params)
		var pr PingResult
		if err := json.Unmarshal(b, &pr); err != nil {
			t.Errorf("params: %v", err)
		}
		gotTarget = pr.TargetID
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"post","result":"ok"}`))
	}))
	defer server.Close()

	r := New(strings.Replace(server.URL, "http://", "ws://", 1), "secret-token", nil)
	r.SendPingResult(PingResult{TargetID: "target-1", Ts: 123, LossPct: 1})

	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want Bearer token", gotAuth)
	}
	if gotMethod != "agent.pingResult" || gotTarget != "target-1" {
		t.Fatalf("posted method/target = %q/%q, want agent.pingResult/target-1", gotMethod, gotTarget)
	}
}
