package main

import (
	"bytes"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRuntime(t *testing.T) *Runtime {
	t.Helper()
	dir := t.TempDir()
	return newRuntime(dir, filepath.Join(dir, "bin"), filepath.Join(dir, "run"))
}

func seed(t *testing.T, rt *Runtime) {
	t.Helper()
	if err := rt.secrets.Save(J{
		"bundle":      J{"owner": "@me:tjena.eu", "as_token": "tok", "slot": "s1"},
		"settings":    J{"prov_secret": "p", "relay_port": 1234},
		"gateway_url": "https://wap.example",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.bridgeDB(), []byte("db-content"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRoundTrip(t *testing.T) {
	a := testRuntime(t)
	seed(t, a)
	data, err := exportBackup(a, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("TJBK1")) {
		t.Fatal("missing magic")
	}
	b := testRuntime(t)
	if err := importBackup(b, data, "wrong pass"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	if err := importBackup(b, data, "correct horse"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(b.bridgeDB())
	if string(got) != "db-content" {
		t.Fatalf("db = %q", got)
	}
	s, _ := b.secrets.Load()
	if str(get(s, "bundle"), "as_token") != "tok" || num(get(s, "settings"), "relay_port") != 1234 {
		t.Fatalf("secrets = %v", s)
	}
	if _, err := exportBackup(a, "short"); err == nil {
		t.Fatal("short passphrase accepted")
	}
}

func TestMoveFormat(t *testing.T) {
	a := testRuntime(t)
	seed(t, a)
	code := newMoveCode("https://wap.example")
	key, err := code.keyBytes()
	if err != nil {
		t.Fatal(err)
	}
	data, err := exportForMove(a, key)
	if err != nil {
		t.Fatal(err)
	}
	b := testRuntime(t)
	other, _ := newMoveCode("x").keyBytes()
	if err := importFromMove(b, data, other); err == nil {
		t.Fatal("wrong key accepted")
	}
	if err := importFromMove(b, data, key); err != nil {
		t.Fatal(err)
	}
	if !b.isSetUp() {
		t.Fatal("not set up after move")
	}
}

func TestMoveCodeParse(t *testing.T) {
	c := newMoveCode("https://wap.example")
	p, err := parseMoveCode(c.URI())
	if err != nil || p != c {
		t.Fatalf("round trip: %v %v", p, err)
	}
	if got := p.wsURL("send"); !strings.HasPrefix(got, "wss://wap.example/_tjena/move/v1/send?id=") {
		t.Fatalf("ws url %s", got)
	}
	// Code made by the phone app (Dart Uri, unpadded base64url).
	phone := "tjena-bridge://move?id=AbCdEfGhIjKlMnOpQrStUvWx&key=" + c.Key + "&gw=https%3A%2F%2Fwap.tjena.eu"
	if p, err = parseMoveCode(phone); err != nil || p.Gateway != "https://wap.tjena.eu" {
		t.Fatalf("phone code: %v %v", p, err)
	}
	for _, bad := range []string{"", "https://x", "tjena-bridge://login?token=1", "tjena-bridge://move?id=1", "tjena-bridge://move?id=a&key=b&gw=ftp://x"} {
		if _, err := parseMoveCode(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestPairingLink(t *testing.T) {
	l := parsePairingLink("tjena-bridge://login?hs=https%3A%2F%2Fm.example&user=%40me%3Am.example&token=T")
	if l == nil || l.Homeserver != "https://m.example" || l.Gateway != defaultGateway || l.Token != "T" {
		t.Fatalf("%+v", l)
	}
	if parsePairingLink("tjena-bridge://move?id=1") != nil {
		t.Fatal("move code accepted as pairing link")
	}
}

func TestRedact(t *testing.T) {
	in := `{"as_token":"abc123","body":"hello there"} Bearer xyz +491701234567 491701234567@s.whatsapp.net`
	out := redact(in)
	for _, leak := range []string{"abc123", "hello there", "xyz", "491701234567"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked in %q", leak, out)
		}
	}
}

func TestAPIGuard(t *testing.T) {
	dir := t.TempDir()
	rt := newRuntime(dir, filepath.Join(dir, "bin"), filepath.Join(dir, "run"))
	prefs := loadPrefs(filepath.Join(dir, "manager.json"))
	log := newLogBuf()
	m := &Manager{rt: rt, log: log, prefs: prefs, sup: newSupervisor(rt, log), auth: newAuth("", prefs)}
	srv := httptest.NewServer(m.routes())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	do := func(method, path, body string, header bool) int {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if header {
			req.Header.Set("X-Tjena-UI", "1")
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := do("GET", "/api/state", "", true); s != 401 {
		t.Fatalf("state without session: %d", s)
	}
	if s := do("POST", "/api/auth/setup", `{"password":"0123456789"}`, false); s != 403 {
		t.Fatalf("no CSRF header: %d", s)
	}
	// DNS rebinding: a foreign name pointing at this address can't choose the password.
	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/setup", strings.NewReader(`{"password":"0123456789"}`))
	req.Host = "evil.example.com"
	req.Header.Set("X-Tjena-UI", "1")
	if resp, err := c.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("setup via foreign host: %v %v", resp, err)
	}
	if s := do("POST", "/api/auth/setup", `{"password":"short"}`, true); s != 400 {
		t.Fatalf("short password: %d", s)
	}
	if s := do("POST", "/api/auth/setup", `{"password":"0123456789"}`, true); s != 200 {
		t.Fatalf("setup: %d", s)
	}
	if s := do("POST", "/api/auth/setup", `{"password":"9876543210"}`, true); s != 409 {
		t.Fatalf("second setup: %d", s)
	}
	if s := do("GET", "/api/state", "", true); s != 200 {
		t.Fatalf("state with session: %d", s)
	}
	if s := do("POST", "/api/relay", `{"method":"GET","path":"/etc/passwd"}`, true); s != 400 {
		t.Fatalf("relay path not checked: %d", s)
	}
	c.Jar, _ = cookiejar.New(nil)
	if s := do("POST", "/api/auth/login", `{"password":"wrong"}`, true); s != 401 {
		t.Fatalf("wrong password: %d", s)
	}
	if s := do("POST", "/api/auth/login", `{"password":"0123456789"}`, true); s != 200 {
		t.Fatalf("login: %d", s)
	}
	if s := do("GET", "/", "", false); s != 200 {
		t.Fatalf("index: %d", s)
	}
}
