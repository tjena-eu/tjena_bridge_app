package main

import "testing"

func TestNetGuard(t *testing.T) {
	g, err := parseAllow("")
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{
		"127.0.0.1:5000": true, "[::1]:5000": true, "192.168.1.20:5000": true,
		"10.1.2.3:1": true, "172.20.0.1:1": true, "100.101.102.103:1": true,
		"[fd12::1]:1": true, "[fe80::1%eth0]:1": true, "[::ffff:192.168.0.5]:1": true,
		"8.8.8.8:1": false, "[2001:db8::1]:1": false, "172.32.0.1:1": false, "junk": false,
	} {
		if got := g.allowed(addr); got != want {
			t.Errorf("private %s: got %v", addr, got)
		}
	}
	g, _ = parseAllow("192.168.1.0/24, 203.0.113.7")
	for addr, want := range map[string]bool{
		"192.168.1.9:1": true, "203.0.113.7:1": true, "127.0.0.1:1": true, "192.168.2.9:1": false,
	} {
		if got := g.allowed(addr); got != want {
			t.Errorf("list %s: got %v", addr, got)
		}
	}
	if g, _ = parseAllow("any"); !g.allowed("8.8.8.8:1") {
		t.Error("any")
	}
	if _, err := parseAllow("192.168.1.0/99"); err == nil {
		t.Error("bad CIDR accepted")
	}
}

func TestLocalHost(t *testing.T) {
	for h, want := range map[string]bool{
		"192.168.1.20:8080": true, "[fd00::5]:8080": true, "localhost:8080": true, "nas:8080": true,
		"bridge.local": true, "nas.fritz.box": true, "nas.box": false, "homeserver.lan:8080": true, "box.home.arpa": true,
		"evil.example.com:8080": false, "bridge.example.com": false, "": false,
	} {
		if got := localHost(h); got != want {
			t.Errorf("%q: got %v", h, got)
		}
	}
}

func TestStartupHintNotRedacted(t *testing.T) {
	l := "Web UI not protected yet: open http://<this server's IP>:8080 from your home network and choose the sign-in, or set WEBUI_PASSWORD."
	if got := redact(l); got != l {
		t.Errorf("redacted: %s", got)
	}
}
