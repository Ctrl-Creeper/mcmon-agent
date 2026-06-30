package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/Ctrl-Creeper/mcmon-agent/internal/mcping"
	"github.com/Ctrl-Creeper/mcmon-agent/internal/reporter"
)

type Config struct {
	HostURL      string   `json:"host_url"`
	AgentID      string   `json:"agent_id"`
	DiscoveryKey string   `json:"discovery_key"`
	AgentName    string   `json:"agent_name"`
	Token        string   `json:"token"`
	Targets      []Target `json:"targets"`
}

type Target struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	TimeoutMs       int      `json:"timeout_ms"`
	Monitors        Monitors `json:"monitors"`
	IntervalSec     int      `json:"interval_sec"`
	ProbesPerBurst  int      `json:"probes_per_burst"`
	ProbeGapMs      int      `json:"probe_gap_ms"`
	ProtocolVersion int      `json:"protocol_version"`
}

type Monitors struct {
	Online  SimpleMonitor `json:"online"`
	Players SimpleMonitor `json:"players"`
	Latency ProbeMonitor  `json:"latency"`
	Loss    ProbeMonitor  `json:"loss"`
}

type SimpleMonitor struct {
	Enabled     bool `json:"enabled"`
	IntervalSec int  `json:"interval_sec"`
}

type ProbeMonitor struct {
	Enabled         bool `json:"enabled"`
	IntervalSec     int  `json:"interval_sec"`
	ProbesPerBurst  int  `json:"probes_per_burst"`
	ProbeGapMs      int  `json:"probe_gap_ms"`
	ProtocolVersion int  `json:"protocol_version,omitempty"`
}

func main() {
	cfgPath := flag.String("config", "agent-config.json", "path to config file")
	configB64 := flag.String("config-base64", "", "base64 encoded immutable config")
	hostURLFlag := flag.String("host-url", "", "host URL override")
	tokenFlag := flag.String("token", "", "agent token override")
	agentIDFlag := flag.String("agent-id", "", "agent id override")
	flag.Parse()

	cfg := Config{
		HostURL:   "ws://localhost:9090",
		AgentName: "agent-" + randHex(3),
	}

	if *configB64 != "" {
		decoded, err := configFromBase64(*configB64)
		if err != nil {
			log.Fatalf("decode config-base64: %v", err)
		}
		cfg = decoded
	} else if data, err := os.ReadFile(*cfgPath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Fatalf("parse config %s: %v", *cfgPath, err)
		}
	} else {
		log.Printf("no config file found at %s, using defaults", *cfgPath)
	}
	if *hostURLFlag != "" {
		cfg.HostURL = *hostURLFlag
	}
	if *tokenFlag != "" {
		cfg.Token = *tokenFlag
	}
	if *agentIDFlag != "" {
		cfg.AgentID = *agentIDFlag
	}

	for i := range cfg.Targets {
		cfg.Targets[i] = normalizeTarget(cfg.Targets[i])
	}

	if cfg.Token == "" && cfg.DiscoveryKey != "" {
		log.Printf("no token — running auto-discovery against %s", cfg.HostURL)
		token, err := discover(cfg.HostURL, cfg.DiscoveryKey, cfg.AgentName)
		if err != nil {
			log.Fatalf("auto-discovery failed: %v", err)
		}
		cfg.Token = token
		saveConfig(*cfgPath, cfg)
		log.Printf("registered with host, token saved to %s", *cfgPath)
	}

	if cfg.Token == "" {
		log.Fatal("no token and no discovery_key — cannot connect to host")
	}

	saveConfig(*cfgPath, cfg)

	wsURL := strings.Replace(cfg.HostURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	warnIfInsecureRemote(wsURL)

	targets := make([]reporter.Target, len(cfg.Targets))
	for i, t := range cfg.Targets {
		targets[i] = reporter.Target{ID: t.ID, Name: t.Name, Host: t.Host, Port: t.Port}
	}

	rep := reporter.New(wsURL, cfg.Token, targets)
	go rep.Run()

	for _, t := range cfg.Targets {
		startMonitorLoops(t, rep)
	}

	fmt.Printf("mcmon-agent running (%d targets)\n", len(cfg.Targets))
	fmt.Printf("host: %s\n", cfg.HostURL)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	log.Println("shutting down")
	rep.Stop()
}

func configFromBase64(raw string) (Config, error) {
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	for i := range cfg.Targets {
		cfg.Targets[i] = normalizeTarget(cfg.Targets[i])
	}
	return cfg, nil
}

func normalizeTarget(t Target) Target {
	if t.ID == "" {
		t.ID = randHex(8)
	}
	if t.Port == 0 {
		t.Port = 25565
	}
	if t.IntervalSec == 0 {
		t.IntervalSec = 60
	}
	if t.TimeoutMs == 0 {
		t.TimeoutMs = 5000
	}
	if t.ProbesPerBurst == 0 {
		t.ProbesPerBurst = 3
	}
	if t.ProbeGapMs == 0 {
		t.ProbeGapMs = 200
	}
	if t.ProtocolVersion == 0 {
		t.ProtocolVersion = 760
	}
	t.Monitors = normalizeMonitors(t)
	return t
}

func normalizeMonitors(t Target) Monitors {
	m := t.Monitors
	if !hasExplicitMonitors(m) {
		m.Online.Enabled = true
		m.Players.Enabled = true
		m.Latency.Enabled = true
		m.Loss.Enabled = true
	}
	if m.Online.IntervalSec <= 0 {
		m.Online.IntervalSec = t.IntervalSec
	}
	if m.Players.IntervalSec <= 0 {
		m.Players.IntervalSec = t.IntervalSec
	}
	if m.Latency.IntervalSec <= 0 {
		m.Latency.IntervalSec = t.IntervalSec
	}
	if m.Latency.ProbesPerBurst <= 0 {
		m.Latency.ProbesPerBurst = t.ProbesPerBurst
	}
	if m.Latency.ProbeGapMs <= 0 {
		m.Latency.ProbeGapMs = t.ProbeGapMs
	}
	if m.Latency.ProtocolVersion <= 0 {
		m.Latency.ProtocolVersion = t.ProtocolVersion
	}
	if m.Loss.IntervalSec <= 0 {
		m.Loss.IntervalSec = t.IntervalSec
	}
	if m.Loss.ProbesPerBurst <= 0 {
		m.Loss.ProbesPerBurst = t.ProbesPerBurst
	}
	if m.Loss.ProbeGapMs <= 0 {
		m.Loss.ProbeGapMs = t.ProbeGapMs
	}
	return m
}

func hasExplicitMonitors(m Monitors) bool {
	return m.Online.Enabled || m.Players.Enabled || m.Latency.Enabled || m.Loss.Enabled ||
		m.Online.IntervalSec > 0 || m.Players.IntervalSec > 0 || m.Latency.IntervalSec > 0 || m.Loss.IntervalSec > 0
}

func startMonitorLoops(t Target, rep *reporter.Reporter) {
	if t.Monitors.Online.Enabled {
		go monitorLoop(t, "online", t.Monitors.Online.IntervalSec, rep, func() { runOnline(t, rep) })
	}
	if t.Monitors.Players.Enabled {
		go monitorLoop(t, "players", t.Monitors.Players.IntervalSec, rep, func() { runPlayers(t, rep) })
	}
	if t.Monitors.Latency.Enabled {
		go monitorLoop(t, "latency", t.Monitors.Latency.IntervalSec, rep, func() { runLatency(t, rep) })
	}
	if t.Monitors.Loss.Enabled {
		go monitorLoop(t, "loss", t.Monitors.Loss.IntervalSec, rep, func() { runLoss(t, rep) })
	}
}

func monitorLoop(t Target, metric string, intervalSec int, rep *reporter.Reporter, fn func()) {
	interval := time.Duration(intervalSec) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	fn()
	for range ticker.C {
		fn()
	}
}

func runOnline(t Target, rep *reporter.Reporter) {
	res := mcping.StatusRequest(t.Host, t.Port, time.Duration(t.TimeoutMs)*time.Millisecond, 760)
	value := 0.0
	if res.OK {
		value = 1
	}
	rep.SendMetricResult(reporter.MetricResult{TargetID: t.ID, Metric: "online", Ts: time.Now().Unix(), Value: &value})
}

func runPlayers(t Target, rep *reporter.Reporter) {
	res := mcping.StatusRequest(t.Host, t.Port, time.Duration(t.TimeoutMs)*time.Millisecond, 760)
	mr := reporter.MetricResult{TargetID: t.ID, Metric: "players", Ts: time.Now().Unix()}
	if res.OK && res.PlayersOnline != nil {
		value := float64(*res.PlayersOnline)
		mr.Value = &value
		if res.PlayersMax != nil {
			b, _ := json.Marshal(map[string]int{"max": *res.PlayersMax})
			mr.Extra = string(b)
		}
	}
	rep.SendMetricResult(mr)
}

func runLatency(t Target, rep *reporter.Reporter) {
	result := runProbeBurst(t, t.Monitors.Latency)
	ts := time.Now().Unix()
	mr := reporter.MetricResult{TargetID: t.ID, Metric: "latency", Ts: ts, Value: result.P50Ms}
	b, _ := json.Marshal(map[string]any{"min": result.MinMs, "p50": result.P50Ms, "max": result.MaxMs, "loss_pct": result.LossPct})
	mr.Extra = string(b)
	rep.SendMetricResult(mr)
	rep.SendPingResult(reporter.PingResult{TargetID: t.ID, Ts: ts, MinMs: result.MinMs, P50Ms: result.P50Ms, MaxMs: result.MaxMs, LossPct: result.LossPct})
}

func runLoss(t Target, rep *reporter.Reporter) {
	result := runProbeBurst(t, t.Monitors.Loss)
	value := result.LossPct
	rep.SendMetricResult(reporter.MetricResult{TargetID: t.ID, Metric: "loss", Ts: time.Now().Unix(), Value: &value})
}

type probeBurstResult struct {
	MinMs   *float64
	P50Ms   *float64
	MaxMs   *float64
	LossPct float64
}

func runProbeBurst(t Target, mon ProbeMonitor) probeBurstResult {
	timeout := time.Duration(t.TimeoutMs) * time.Millisecond
	gap := time.Duration(mon.ProbeGapMs) * time.Millisecond
	n := mon.ProbesPerBurst
	if n <= 0 {
		n = 3
	}
	proto := mon.ProtocolVersion
	if proto <= 0 {
		proto = 760
	}

	var latencies []float64
	var failures int

	for i := 0; i < n; i++ {
		if i > 0 {
			time.Sleep(gap)
		}
		res := mcping.Ping(t.Host, t.Port, timeout, proto)
		if res.OK {
			latencies = append(latencies, res.LatencyMs)
		} else {
			failures++
			log.Printf("[%s] probe %d/%d failed: %v", t.Name, i+1, n, res.Err)
		}
	}

	out := probeBurstResult{LossPct: float64(failures) / float64(n)}

	if len(latencies) > 0 {
		sort.Float64s(latencies)
		minV := latencies[0]
		maxV := latencies[len(latencies)-1]
		p50 := percentile(latencies, 0.5)
		out.MinMs = &minV
		out.MaxMs = &maxV
		out.P50Ms = &p50
		log.Printf("[%s] min=%.1f p50=%.1f max=%.1f loss=%.0f%%", t.Name, minV, p50, maxV, out.LossPct*100)
	} else {
		log.Printf("[%s] all %d probes failed", t.Name, n)
	}
	return out
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := p * float64(len(sorted)-1)
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper {
		return sorted[lower]
	}
	frac := idx - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}

type discoverResp struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`
}

func discover(hostURL, key, name string) (string, error) {
	endpoint := fmt.Sprintf("%s/api/discover?name=%s", strings.TrimSuffix(httpURL(hostURL), "/"), url.QueryEscape(name))
	req, _ := http.NewRequest("POST", endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("discovery returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var dr discoverResp
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return "", err
	}
	return dr.Token, nil
}

func saveConfig(path string, cfg Config) {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("save config: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		log.Printf("save config: rename: %v", err)
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// warnIfInsecureRemote logs a warning when the host URL uses an unencrypted
// scheme against a non-loopback host — the agent token would otherwise be
// sent over the network in cleartext.
func warnIfInsecureRemote(wsURL string) {
	u, err := url.Parse(wsURL)
	if err != nil || u.Host == "" {
		return
	}
	if u.Scheme == "wss" || u.Scheme == "https" {
		return
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return
	}
	log.Printf("WARNING: connecting to %s over unencrypted %s — agent token will be sent in cleartext. Use wss:// or a TLS-terminating proxy.", u.Host, u.Scheme)
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
