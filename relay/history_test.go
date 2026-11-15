package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadHistoryLog(t *testing.T) {
	dir := t.TempDir()
	log := `{"level":"info","time":"2026-10-22T09:00:00Z","message":"Sending backfill messages","portal_id":"old","message_count":5}
{"level":"info","time":"2026-10-22T10:00:00Z","message":"Storing history sync","sync_type":"RECENT","chunk_order":2,"progress":45,"conversation_count":30}
{"level":"info","time":"2026-10-22T10:00:01Z","message":"Finished storing history sync","total_saved_count":300}
{"level":"info","time":"2026-10-22T10:06:00Z","message":"Creating portals from history sync","conversation_count":25}
{"level":"info","time":"2026-10-22T10:06:05Z","message":"Sending backfill messages","portal_id":"a","message_count":19}
{"level":"warn","time":"2026-10-22T10:06:06Z","message":"Request failed, retrying","error":"HTTP 429"}
{"level":"error","time":"2026-10-22T10:06:20Z","message":"Failed to send forward backfill","error":"M_LIMIT_EXCEEDED","portal_id":"a"}
not json
`
	if err := os.WriteFile(filepath.Join(dir, "bridge.log"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	since, _ := time.Parse(time.RFC3339, "2026-10-22T09:59:30Z")
	h := readHistoryLog(dir, since)
	if !h.Found || h.Progress["RECENT"] != 45 || h.Saved != 300 || h.RoomsTarget != 25 || h.RoomsDone {
		t.Fatalf("summary wrong: %+v", h)
	}
	if h.BackfillChats != 1 || h.BackfillMessages != 19 || h.perPortalSent["old"] != 0 {
		t.Fatalf("entries before login must be skipped: %+v", h)
	}
	if h.BackfillFailed != 1 || !h.perPortalFailed["a"] || !h.perPortalRateLimt["a"] || h.RateLimited != 2 {
		t.Fatalf("failure not detected: %+v", h)
	}
	if len(h.RecentErrors) != 1 {
		t.Fatalf("errors: %+v", h.RecentErrors)
	}
}

func TestClassifyChats(t *testing.T) {
	cases := []struct {
		c          HistoryChat
		state, why string
	}{
		{HistoryChat{HasRoom: true, Stored: 5, Fillable: 2}, "fixable", ""},
		{HistoryChat{HasRoom: true, Stored: 5}, "unfixable", "newer_messages"},
		{HistoryChat{HasRoom: true}, "ok", ""},
		{HistoryChat{Stored: 5}, "held", ""},
		{HistoryChat{Stored: 5, CreateFailed: 1}, "held", ""},
		{HistoryChat{Stored: 5, CreateFailed: 2}, "unfixable", "create_failed"},
		{HistoryChat{}, "", ""},
	}
	for i, tc := range cases {
		tc.c.classify()
		if tc.c.State != tc.state || tc.c.Reason != tc.why {
			t.Errorf("case %d: got %q/%q, want %q/%q", i, tc.c.State, tc.c.Reason, tc.state, tc.why)
		}
	}
}

func TestHistorySettle(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bridge.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE user_login (id TEXT, remote_name TEXT, metadata TEXT)`,
		`CREATE TABLE whatsapp_history_sync_conversation (user_login_id TEXT, chat_jid TEXT, synced_login_ts INTEGER)`,
		`CREATE TABLE portal (id TEXT, mxid TEXT)`,
		`INSERT INTO user_login VALUES ('L', 'me', '{"logged_in_at":1000}')`,
		// broken: failed twice, no room -> settled
		`INSERT INTO whatsapp_history_sync_conversation VALUES ('L', 'bad@s.whatsapp.net', NULL)`,
		// failed once -> stays in the queue
		`INSERT INTO whatsapp_history_sync_conversation VALUES ('L', 'once@s.whatsapp.net', NULL)`,
		// never tried -> stays
		`INSERT INTO whatsapp_history_sync_conversation VALUES ('L', 'new@s.whatsapp.net', NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()
	logs := filepath.Join(dir, "logs")
	_ = os.MkdirAll(logs, 0o700)
	log := `{"level":"error","time":"2026-10-22T10:06:00Z","message":"Failed to get chat info","chat_jid":"bad@s.whatsapp.net"}
{"level":"error","time":"2026-10-22T11:06:00Z","message":"Failed to create portal to handle event","chat_jid":"bad@s.whatsapp.net"}
{"level":"error","time":"2026-10-22T11:06:01Z","message":"Failed to get chat info","chat_jid":"once@s.whatsapp.net"}
`
	if err := os.WriteFile(filepath.Join(logs, "bridge.log"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := historySettle(dbPath, logs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Settled) != 1 || res.Settled[0] != "bad@s.whatsapp.net" {
		t.Fatalf("settled: %v", res.Settled)
	}
	db, _ = sql.Open("sqlite3", dbPath)
	defer db.Close()
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM whatsapp_history_sync_conversation WHERE synced_login_ts IS NULL`).Scan(&n)
	if n != 2 {
		t.Fatalf("still queued: %d, want 2", n)
	}
	// Second run: nothing left to do.
	if res, _ = historySettle(dbPath, logs); len(res.Settled) != 0 {
		t.Fatalf("second run settled %v", res.Settled)
	}
}
