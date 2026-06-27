package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/lewiswu/mcmon-agent/internal/mcping"
	"github.com/lewiswu/mcmon-agent/internal/reporter"
)

type Config struct {
	HostURL      string   `json:"host_url"`
	DiscoveryKey string   `json:"discovery_key"`
	AgentName    string   `json:"agent_name"`
	Token        string   `json:"token"`
	Targets      []Target `json:"targets"`
}

type Target struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	IntervalSec     int    `json:"interval_sec"`
	TimeoutMs       int    `json:"timeout_ms"`
	ProbesPerBurst  int    `json:"probes_per_burst"`
	ProbeGapMs      int    `json:"probe_gap_ms"`
	ProtocolVersion int    `json:"protocol_version"`
}

func main() {
	cfgPath := flag.String("config", "agent-config.json", "path to config file")
	flag.Parse()

	cfg := Config{
		HostURL:   "ws://localhost:9090",
		AgentName: "agent-" + randHex(3),
	}

	if data, err := os.ReadFile(*cfgPath); err == nil {
		json.Unmarshal(data, &cfg)
	} else {
		log.Printf("no config file found at %s, using defaults", *cfgPath)
	}

	for i := range cfg.Targets {
		t := &cfg.Targets[i]
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
			t.ProtocolVersion = 767
		}
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

	targets := make([]reporter.Target, len(cfg.Targets))
	for i, t := range cfg.Targets {
		targets[i] = reporter.Target{ID: t.ID, Name: t.Name, Host: t.Host, Port: t.Port}
	}

	rep := reporter.New(wsURL, cfg.Token, targets)
	go rep.Run()

	for _, t := range cfg.Targets {
		go probeLoop(t, rep)
	}

	fmt.Printf("mcmon-agent running (%d targets)\n", len(cfg.Targets))
	fmt.Printf("host: %s\n", cfg.HostURL)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	log.Println("shutting down")
	rep.Stop()
}

func probeLoop(t Target, rep *reporter.Reporter) {
	ticker := time.NewTicker(time.Duration(t.IntervalSec) * time.Second)
	defer ticker.Stop()

	runProbe(t, rep)
	for range ticker.C {
		runProbe(t, rep)
	}
}

func runProbe(t Target, rep *reporter.Reporter) {
	timeout := time.Duration(t.TimeoutMs) * time.Millisecond
	gap := time.Duration(t.ProbeGapMs) * time.Millisecond
	n := t.ProbesPerBurst

	var latencies []float64
	var failures int

	for i := 0; i < n; i++ {
		if i > 0 {
			time.Sleep(gap)
		}
		res := mcping.Ping(t.Host, t.Port, timeout, t.ProtocolVersion)
		if res.OK {
			latencies = append(latencies, res.LatencyMs)
		} else {
			failures++
			log.Printf("[%s] probe %d/%d failed: %v", t.Name, i+1, n, res.Err)
		}
	}

	lossPct := float64(failures) / float64(n)
	pr := reporter.PingResult{
		TargetID: t.ID,
		Ts:       time.Now().Unix(),
		LossPct:  lossPct,
	}

	if len(latencies) > 0 {
		sort.Float64s(latencies)
		minV := latencies[0]
		maxV := latencies[len(latencies)-1]
		p50 := percentile(latencies, 0.5)
		pr.MinMs = &minV
		pr.MaxMs = &maxV
		pr.P50Ms = &p50
		log.Printf("[%s] min=%.1f p50=%.1f max=%.1f loss=%.0f%%", t.Name, minV, p50, maxV, lossPct*100)
	} else {
		log.Printf("[%s] all %d probes failed", t.Name, n)
	}

	rep.SendPingResult(pr)
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
	url := fmt.Sprintf("%s/api/discover?name=%s", hostURL, name)
	req, _ := http.NewRequest("POST", url, nil)
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("discovery returned %d", resp.StatusCode)
	}
	var dr discoverResp
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return "", err
	}
	return dr.Token, nil
}

func saveConfig(path string, cfg Config) {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(path, data, 0600)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
