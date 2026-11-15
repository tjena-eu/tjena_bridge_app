package main

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeGateway pipes /_tjena/move/v1/send to /receive like tjena_proxy's
// move.go (without auth), and answers the provisioner and logout.
func fakeGateway(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	senders := map[string]*websocket.Conn{}
	paired := map[string]chan *websocket.Conn{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_tjena/move/v1/send", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ws.SetReadLimit(4 << 20)
		id := r.URL.Query().Get("id")
		ch := make(chan *websocket.Conn, 1)
		mu.Lock()
		senders[id], paired[id] = ws, ch
		mu.Unlock()
		recv := <-ch
		ctx := context.Background()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				_ = recv.Close(websocket.StatusNormalClosure, "")
				return
			}
			if recv.Write(ctx, typ, data) != nil {
				return
			}
		}
	})
	mux.HandleFunc("GET /_tjena/move/v1/receive", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		mu.Lock()
		if id == "" { // no code: the account's transfer (here: any)
			for k := range senders {
				id = k
			}
		}
		snd, ch := senders[id], paired[id]
		delete(senders, id)
		mu.Unlock()
		if snd == nil {
			http.Error(w, "no", 404)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := context.Background()
		notice := []byte(`{"type":"paired"}`)
		_ = ws.Write(ctx, websocket.MessageText, notice)
		ch <- ws
		_ = snd.Write(ctx, websocket.MessageText, notice)
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				_ = snd.Close(websocket.StatusNormalClosure, "")
				return
			}
			_ = snd.Write(ctx, typ, data)
		}
	})
	mux.HandleFunc("POST /provision/v1/bridge", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(J{"owner": "@me:tjena.eu", "as_token": "new-token", "slot": "s1", "active": true})
	})
	mux.HandleFunc("POST /_matrix/client/v3/logout", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{}")) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testManager(t *testing.T) *Manager {
	dir := t.TempDir()
	rt := newRuntime(dir, filepath.Join(dir, "bin"), filepath.Join(dir, "run"))
	prefs := loadPrefs(filepath.Join(dir, "manager.json"))
	log := newLogBuf()
	m := &Manager{rt: rt, log: log, prefs: prefs, sup: newSupervisor(rt, log), auth: newAuth("x", prefs)}
	t.Cleanup(m.sup.Stop)
	return m
}

type moveCase struct {
	compare          bool
	oldOK, newOK     bool // answers to "numbers match?"
	wrongKey         bool // the new device has a code with another key
	wantMoved        bool
	wantOldErrSubstr string // in the old job's error when not moved
}

func runMove(t *testing.T, c moveCase) {
	gw := fakeGateway(t)
	old, nu := testManager(t), testManager(t)
	seed(t, old.rt)
	_ = old.rt.secrets.Update(func(s J) error {
		s["gateway_url"] = gw.URL
		s["session"] = J{"homeserver": gw.URL, "user_id": "@me:tjena.eu", "access_token": "old", "device_id": "OLD"}
		return nil
	})
	big := make([]byte, 3<<20) // more than one chunk
	for i := range big {
		big[i] = byte(i * 7)
	}
	_ = os.WriteFile(old.rt.path("wacall.db"), big, 0o600)

	code := newMoveCode(gw.URL)
	if err := old.startJob("move-out", func(j *Job) { j.Phase = "waiting" }, func(ctx context.Context) error {
		old.moveOut(ctx, code, "tok", old.jobConfirm())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The receiver must find the sender waiting.
	time.Sleep(200 * time.Millisecond)
	sess := &Session{Homeserver: gw.URL, UserID: "@me:tjena.eu", AccessToken: "new", DeviceID: "NEW"}
	inCode := code
	if c.compare {
		inCode = MoveCode{Gateway: gw.URL}
	} else if c.wrongKey {
		inCode.Key = randomSecret(32)
	}
	if err := nu.startJob("move-in", nil, func(ctx context.Context) error {
		return nu.moveIn(ctx, inCode, sess, nu.jobConfirm())
	}); err != nil {
		t.Fatal(err)
	}
	wait := func(cond func() bool, what string) {
		deadline := time.Now().Add(15 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %s; old %+v new %+v", what, old.jobGet(), nu.jobGet())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if c.compare {
		wait(func() bool { return old.jobGet().Phase == "compare" && nu.jobGet().Phase == "compare" }, "compare")
		o, n := old.jobGet().SAS, nu.jobGet().SAS
		if len(o) != 6 || o != n {
			t.Fatalf("numbers: old %q new %q", o, n)
		}
		if !nu.confirmJob(c.newOK) || !old.confirmJob(c.oldOK) {
			t.Fatal("confirm refused")
		}
	}
	wait(func() bool { return old.jobGet().Done && nu.jobGet().Done }, "both done")
	if !c.wantMoved {
		if old.jobGet().Phase != "failed" || !strings.Contains(old.jobGet().Error, c.wantOldErrSubstr) {
			t.Fatalf("old job: %+v", old.jobGet())
		}
		if !old.rt.isSetUp() || nu.rt.isSetUp() {
			t.Fatal("bridge left the old device")
		}
		return
	}
	if j := old.jobGet(); j.Phase != "done" {
		t.Fatalf("old job: %+v", j)
	}
	if j := nu.jobGet(); j.Phase != "done" {
		t.Fatalf("new job: %+v", j)
	}
	if old.rt.isSetUp() {
		t.Fatal("old device still set up")
	}
	s, _ := nu.rt.secrets.Load()
	if str(get(s, "bundle"), "as_token") != "new-token" || str(get(s, "session"), "device_id") != "NEW" {
		t.Fatalf("new secrets: %v", s)
	}
	got, _ := os.ReadFile(nu.rt.path("wacall.db"))
	if len(got) != len(big) || got[12345] != big[12345] {
		t.Fatal("wacall.db not moved intact")
	}
}

func TestMoveWithCode(t *testing.T) { runMove(t, moveCase{wantMoved: true}) }

func TestMoveComparingNumbers(t *testing.T) {
	runMove(t, moveCase{compare: true, oldOK: true, newOK: true, wantMoved: true})
}

func TestMoveNumbersDifferOnOldDevice(t *testing.T) {
	runMove(t, moveCase{compare: true, oldOK: false, newOK: true, wantOldErrSubstr: "didn't match"})
}

func TestMoveNumbersDifferOnNewDevice(t *testing.T) {
	runMove(t, moveCase{compare: true, oldOK: true, newOK: false, wantOldErrSubstr: "connection"})
}

func TestMoveWrongCode(t *testing.T) {
	runMove(t, moveCase{wrongKey: true, wantOldErrSubstr: "different code"})
}

func TestKexBothSidesAgree(t *testing.T) {
	a, _ := newKex()
	b, _ := newKex()
	ka, sa, err := a.derive(b.pubB64(), true)
	if err != nil {
		t.Fatal(err)
	}
	kb, sb, _ := b.derive(a.pubB64(), false)
	if string(ka) != string(kb) || sa != sb || len(sa) != 6 {
		t.Fatalf("disagree: %s %s", sa, sb)
	}
	// The roles matter: same keys with swapped roles give another result.
	kc, _, _ := b.derive(a.pubB64(), true)
	if string(kc) == string(ka) {
		t.Fatal("roles not bound")
	}
	if !checkProof(ka, proofImported, moveProof(kb, proofImported)) || checkProof(ka, proofHello, moveProof(kb, proofImported)) {
		t.Fatal("proof")
	}
}

// Fixed values, also checked by bridge_app (MoveKexTest.kt): phone and Docker
// must agree byte for byte.
func TestKexVectors(t *testing.T) {
	d1, d2 := make([]byte, 32), make([]byte, 32)
	for i := range d1 {
		d1[i], d2[i] = byte(i+1), byte(0x40+i)
	}
	p1, _ := ecdh.P256().NewPrivateKey(d1)
	p2, _ := ecdh.P256().NewPrivateKey(d2)
	old, nw := &kex{p1, p1.PublicKey().Bytes()}, &kex{p2, p2.PublicKey().Bytes()}
	key, sas, err := nw.derive(old.pubB64(), false)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string][2]string{
		"oldPub":        {old.pubB64(), "BFFcPW6545a5BNP-yn9U_c0MwemXvzddylFa0KbDtANfRTa-OlDzGPv5pUdZAqIhUCvvDVfgjFOyzApW8X2fk1Q"},
		"newPub":        {nw.pubB64(), "BGjsfPCM1BBuQ7FN6JVCZSK9CkUVDAJ-RceVNDTXR-e64685qI677oZ5u2HnhFw6icubWjI3w_2wsFh9uvQVEY0"},
		"commit":        {commitOf(nw.pub), "TVsLZpfsmEbeDNdqSEwCvb6cujifVglj2cog1BY_Q7A"},
		"key":           {b64.EncodeToString(key), "Pa1PMMGxB6TQbx8ETL9hscqpIyeLmLz1uv5B7Bf4Bfw"},
		"sas":           {sas, "197704"},
		"proofHello":    {moveProof(key, proofHello), "8ouimbtf_3UMfTXLUaxEJVpbw4JLL7K3w8OXsPjkauQ"},
		"proofImported": {moveProof(key, proofImported), "-cxZvd-8SOf6zD8AC5qNJe5BivVnQKvPT1-4eTC-mHg"},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: %s, want %s", name, c[0], c[1])
		}
	}
	// A point that isn't on the curve is refused.
	bad := append([]byte{}, nw.pub...)
	bad[40] ^= 1
	if _, _, err := old.derive(b64.EncodeToString(bad), true); err == nil {
		t.Error("off-curve key accepted")
	}
}
