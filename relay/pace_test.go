package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func limitedUpstream(limitFirst int, retryMS int) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	var calls, ok atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if n <= int64(limitFirst) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errcode":"M_LIMIT_EXCEEDED","retry_after_ms":` + itoa(retryMS) + `}`))
			return
		}
		if string(body) != `{"body":"hi"}` {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ok.Add(1)
		_, _ = w.Write([]byte(`{"event_id":"$e"}`))
	}))
	return srv, &calls, &ok
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func proxyTo(t *testing.T, upstream string) (http.Handler, *pacer) {
	r := &Relay{cfg: Config{Upstream: upstream}, pace: newPacer()}
	return r.pace.wrap(r.httpProxy()), r.pace
}

func send(h http.Handler, txn string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut,
		"/_matrix/client/v3/rooms/!r:x/send/m.room.message/"+txn+"?user_id=@me:x",
		strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPaceRetries429(t *testing.T) {
	srv, calls, ok := limitedUpstream(2, 150)
	defer srv.Close()
	h, p := proxyTo(t, srv.URL)
	start := time.Now()
	rec := send(h, "1")
	if rec.Code != 200 || ok.Load() != 1 || calls.Load() != 3 {
		t.Fatalf("code %d ok %d calls %d", rec.Code, ok.Load(), calls.Load())
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("did not wait retry_after_ms")
	}
	if s := p.status(); s.RateLimitHits != 2 || s.Retried != 2 || s.HandedBack != 0 {
		t.Fatalf("status %+v", s)
	}
	// Next send of the same sender is spaced out (learned interval).
	start = time.Now()
	if send(h, "2").Code != 200 || time.Since(start) < 100*time.Millisecond {
		t.Fatal("second send not paced")
	}
}

func TestPaceHandsBackWithRetryAfter(t *testing.T) {
	old := paceMaxHold
	paceMaxHold = 100 * time.Millisecond
	defer func() { paceMaxHold = old }()
	srv, _, ok := limitedUpstream(100, 5000)
	defer srv.Close()
	h, p := proxyTo(t, srv.URL)
	rec := send(h, "1")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || ok.Load() != 0 {
		t.Fatalf("code %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if p.status().HandedBack != 1 {
		t.Fatalf("status %+v", p.status())
	}
}

func TestPaceLeavesOtherRequestsAlone(t *testing.T) {
	srv, calls, _ := limitedUpstream(1, 10)
	defer srv.Close()
	h, _ := proxyTo(t, srv.URL)
	req := httptest.NewRequest(http.MethodGet, "/_matrix/client/v3/rooms/!r:x/messages", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Fatalf("GET must pass through unchanged: %d", rec.Code)
	}
}

func catchupDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE user_login (bridge_id TEXT, id TEXT, remote_name TEXT, metadata TEXT)`,
		`CREATE TABLE portal (id TEXT, receiver TEXT, mxid TEXT, name TEXT)`,
		`CREATE TABLE user_portal (login_id TEXT, portal_id TEXT, portal_receiver TEXT)`,
		`CREATE TABLE message (room_id TEXT, room_receiver TEXT, timestamp BIGINT)`,
		`CREATE TABLE whatsapp_history_sync_conversation (user_login_id TEXT, chat_jid TEXT, synced_login_ts BIGINT, last_message_timestamp BIGINT)`,
		`CREATE TABLE whatsapp_history_sync_message (user_login_id TEXT, chat_jid TEXT, timestamp BIGINT)`,
		`INSERT INTO user_login VALUES ('wa','L1','me','{"logged_in_at":100,"history_sync_portals_need_creating":false,"other":1}')`,
		`INSERT INTO user_login VALUES ('wa','L2','work','{"logged_in_at":200}')`,
		// a: gap at the end of a room (stored messages newer than the room's newest)
		`INSERT INTO portal VALUES ('a@s','L1','!a:x','A')`,
		`INSERT INTO user_portal VALUES ('L1','a@s','L1')`,
		`INSERT INTO message VALUES ('a@s','L1',10000000000)`, // 10 s
		`INSERT INTO whatsapp_history_sync_conversation VALUES ('L1','a@s',100,50)`,
		`INSERT INTO whatsapp_history_sync_message VALUES ('L1','a@s',20),('L1','a@s',30)`,
		// b: complete
		`INSERT INTO portal VALUES ('b@s','L1','!b:x','B')`,
		`INSERT INTO user_portal VALUES ('L1','b@s','L1')`,
		`INSERT INTO whatsapp_history_sync_conversation VALUES ('L1','b@s',100,60)`,
		// d: room, stored messages all older than the room's newest: can't be filled
		`INSERT INTO portal VALUES ('d@s','L1','!d:x','D')`,
		`INSERT INTO user_portal VALUES ('L1','d@s','L1')`,
		`INSERT INTO message VALUES ('d@s','L1',90000000000)`,
		`INSERT INTO whatsapp_history_sync_conversation VALUES ('L1','d@s',100,70)`,
		`INSERT INTO whatsapp_history_sync_message VALUES ('L1','d@s',5)`,
		// c, e: held back (no room yet)
		`INSERT INTO whatsapp_history_sync_conversation VALUES ('L1','c@s',NULL,1)`,
		`INSERT INTO whatsapp_history_sync_message VALUES ('L1','c@s',1)`,
		`INSERT INTO whatsapp_history_sync_conversation VALUES ('L1','e@s',NULL,2)`,
		`INSERT INTO whatsapp_history_sync_message VALUES ('L1','e@s',2)`,
		// other account, held back
		`INSERT INTO whatsapp_history_sync_conversation VALUES ('L2','f@s',NULL,3)`,
		`INSERT INTO whatsapp_history_sync_message VALUES ('L2','f@s',3)`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	return path
}

func syncedMarks(t *testing.T, path string) map[string]any {
	t.Helper()
	db, _ := sql.Open("sqlite3", path)
	defer db.Close()
	rows, err := db.Query(`SELECT user_login_id||'/'||chat_jid, synced_login_ts FROM whatsapp_history_sync_conversation`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var k string
		var v sql.NullInt64
		_ = rows.Scan(&k, &v)
		if v.Valid {
			out[k] = v.Int64
		} else {
			out[k] = nil
		}
	}
	return out
}

func loginFlag(t *testing.T, path, login string) map[string]any {
	t.Helper()
	db, _ := sql.Open("sqlite3", path)
	defer db.Close()
	var meta string
	_ = db.QueryRow(`SELECT metadata FROM user_login WHERE id=?`, login).Scan(&meta)
	m := map[string]any{}
	_ = json.Unmarshal([]byte(meta), &m)
	return m
}

func TestHistoryCatchupMissingOnly(t *testing.T) {
	path := catchupDB(t)
	res, err := historyCatchup(path, CatchupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chats != 1 || res.Missing != 1 || res.Held != 0 || res.Stored != 2 || res.Logins["L1"] != 1 {
		t.Fatalf("result %+v", res)
	}
	m := syncedMarks(t, path)
	// a cleared; held-back c/e marked done so they aren't pulled in; L2 untouched.
	if m["L1/a@s"] != nil || m["L1/b@s"] != int64(100) || m["L1/c@s"] != int64(100) ||
		m["L1/e@s"] != int64(100) || m["L1/d@s"] != int64(100) || m["L2/f@s"] != nil {
		t.Fatalf("marks %v", m)
	}
	f := loginFlag(t, path, "L1")
	if f["history_sync_portals_need_creating"] != true || f["other"] != float64(1) || f["logged_in_at"] != float64(100) {
		t.Fatalf("metadata %v", f)
	}
	if _, ok := loginFlag(t, path, "L2")["history_sync_portals_need_creating"]; ok {
		t.Fatal("L2 flag set")
	}
	if !catchupActive(filepath.Dir(path)) {
		t.Fatal("catch-up flag file missing")
	}
}

func TestHistoryCatchupHeldAndChats(t *testing.T) {
	path := catchupDB(t)
	res, err := historyCatchup(path, CatchupOptions{Mode: "held", Login: "L1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chats != 2 || res.Held != 2 || res.Logins["L2"] != 0 {
		t.Fatalf("held %+v", res)
	}
	m := syncedMarks(t, path)
	if m["L1/c@s"] != nil || m["L1/e@s"] != nil || m["L1/a@s"] != int64(100) || m["L2/f@s"] != nil {
		t.Fatalf("held marks %v", m)
	}

	path = catchupDB(t)
	res, err = historyCatchup(path, CatchupOptions{Chats: []string{"e@s", "f@s"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chats != 2 || res.Logins["L1"] != 1 || res.Logins["L2"] != 1 {
		t.Fatalf("chats %+v", res)
	}
	m = syncedMarks(t, path)
	if m["L1/e@s"] != nil || m["L1/c@s"] != int64(100) || m["L2/f@s"] != nil {
		t.Fatalf("chats marks %v", m)
	}
	if loginFlag(t, path, "L2")["history_sync_portals_need_creating"] != true {
		t.Fatal("L2 flag not set")
	}
}

func TestHistoryStop(t *testing.T) {
	path := catchupDB(t)
	if _, err := historyCatchup(path, CatchupOptions{Mode: "both"}); err != nil {
		t.Fatal(err)
	}
	res, err := historyStop(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != 4 { // a, c, e (L1) and f (L2)
		t.Fatalf("stopped %+v", res)
	}
	for k, v := range syncedMarks(t, path) {
		if v == nil {
			t.Fatalf("%s still pending", k)
		}
	}
	if loginFlag(t, path, "L1")["history_sync_portals_need_creating"] != false {
		t.Fatal("flag not cleared")
	}
	if catchupActive(filepath.Dir(path)) {
		t.Fatal("catch-up flag file not removed")
	}
}
