package main

import "testing"

func TestDecodeBase64Config(t *testing.T) {
	cfg, err := configFromBase64("eyJob3N0X3VybCI6Imh0dHBzOi8vaG9zdC5leGFtcGxlLmNvbSIsImFnZW50X2lkIjoibm9kZS0xIiwidG9rZW4iOiJ0b2tlbiIsInRhcmdldHMiOlt7ImlkIjoic3J2LTEiLCJuYW1lIjoiU3Vydml2YWwiLCJob3N0IjoibWMuZXhhbXBsZS5jb20iLCJwb3J0IjoyNTU2NSwidGltZW91dF9tcyI6MTUwMCwibW9uaXRvcnMiOnsib25saW5lIjp7ImVuYWJsZWQiOnRydWUsImludGVydmFsX3NlYyI6MzB9LCJwbGF5ZXJzIjp7ImVuYWJsZWQiOnRydWUsImludGVydmFsX3NlYyI6NDV9LCJsYXRlbmN5Ijp7ImVuYWJsZWQiOnRydWUsImludGVydmFsX3NlYyI6NjAsInByb2Jlc19wZXJfYnVyc3QiOjUsInByb2JlX2dhcF9tcyI6MjUwLCJwcm90b2NvbF92ZXJzaW9uIjo3NjB9LCJsb3NzIjp7ImVuYWJsZWQiOmZhbHNlLCJpbnRlcnZhbF9zZWMiOjEyMCwicHJvYmVzX3Blcl9idXJzdCI6MywicHJvYmVfZ2FwX21zIjoyMDB9fX1dfQ==")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HostURL != "https://host.example.com" || cfg.AgentID != "node-1" || cfg.Token != "token" {
		t.Fatalf("cfg identity = %#v", cfg)
	}
	if len(cfg.Targets) != 1 || !cfg.Targets[0].Monitors.Online.Enabled || cfg.Targets[0].Monitors.Latency.ProtocolVersion != 760 || cfg.Targets[0].Monitors.Loss.Enabled {
		t.Fatalf("cfg targets = %#v", cfg.Targets)
	}
}

func TestCanShareLatencyLossProbeBurst(t *testing.T) {
	tgt := normalizeTarget(Target{
		ID: "srv", Host: "mc.example.com",
		Monitors: Monitors{
			Latency: ProbeMonitor{Enabled: true},
			Loss:    ProbeMonitor{Enabled: true},
		},
	})

	if !canShareLatencyLoss(tgt) {
		t.Fatalf("matching latency/loss monitors should share a probe burst: %#v", tgt.Monitors)
	}

	tgt.Monitors.Loss.IntervalSec++
	if canShareLatencyLoss(tgt) {
		t.Fatal("different probe intervals must not share a probe burst")
	}
}

func TestCanShareLatencyLossUsesEffectiveProtocol(t *testing.T) {
	tgt := normalizeTarget(Target{
		ID: "srv", Host: "mc.example.com", ProtocolVersion: 765,
		Monitors: Monitors{
			Latency: ProbeMonitor{Enabled: true, ProtocolVersion: 765},
			Loss:    ProbeMonitor{Enabled: true},
		},
	})

	if !canShareLatencyLoss(tgt) {
		t.Fatalf("loss monitor without explicit protocol should inherit target protocol: %#v", tgt.Monitors)
	}

	tgt.Monitors.Loss.ProtocolVersion = 760
	if canShareLatencyLoss(tgt) {
		t.Fatal("different effective protocol versions must not share a probe burst")
	}
}
