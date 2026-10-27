package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func namesTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE whatsmeow_contacts (our_jid TEXT, their_jid TEXT, first_name TEXT, full_name TEXT,
			push_name TEXT, business_name TEXT, redacted_phone TEXT, PRIMARY KEY (our_jid, their_jid))`,
		`CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL)`,
		`CREATE TABLE ghost (bridge_id TEXT, id TEXT, name TEXT, name_set BOOLEAN, PRIMARY KEY (bridge_id, id))`,
		`CREATE TABLE portal (bridge_id TEXT, id TEXT, receiver TEXT, mxid TEXT, other_user_id TEXT,
			name TEXT, name_set BOOLEAN, name_is_custom BOOLEAN NOT NULL DEFAULT false)`,
		// Anna: saved in the address book, ghost still shows her number.
		`INSERT INTO whatsmeow_contacts VALUES ('me@s.whatsapp.net','46701111111@s.whatsapp.net','Anna','Anna Svensson','anna_s','','')`,
		`INSERT INTO ghost VALUES ('wa','46701111111','+46701111111 (WA)',true)`,
		`INSERT INTO portal VALUES ('wa','46701111111@s.whatsapp.net','46700000000','!anna:x','46701111111','+46701111111 (WA)',true,false)`,
		// Bert: LID ghost; address-book name stored under his phone number.
		`INSERT INTO whatsmeow_lid_map VALUES ('999','46702222222')`,
		`INSERT INTO whatsmeow_contacts VALUES ('me@s.whatsapp.net','46702222222@s.whatsapp.net','','Bert B','','','')`,
		`INSERT INTO whatsmeow_contacts VALUES ('me@s.whatsapp.net','999@lid','','','bertie','','')`,
		`INSERT INTO ghost VALUES ('wa','lid-999','bertie (WA)',true)`,
		// Carl: nobody knows a name.
		`INSERT INTO ghost VALUES ('wa','46703333333','+46703333333 (WA)',true)`,
		// Dana: already right; renamed room is left alone.
		`INSERT INTO whatsmeow_contacts VALUES ('me@s.whatsapp.net','46704444444@s.whatsapp.net','','Dana','','','')`,
		`INSERT INTO ghost VALUES ('wa','46704444444','Dana (WA)',true)`,
		`INSERT INTO portal VALUES ('wa','46704444444@s.whatsapp.net','46700000000','!dana:x','46704444444','My Dana',true,true)`,
		// Eva: database right, but Matrix still shows her number (room name
		// and her name in the chat): only the live check finds this.
		`INSERT INTO whatsmeow_contacts VALUES ('me@s.whatsapp.net','46705555555@s.whatsapp.net','','Eva E','','','')`,
		`INSERT INTO ghost VALUES ('wa','46705555555','Eva E (WA)',true)`,
		`INSERT INTO portal VALUES ('wa','46705555555@s.whatsapp.net','46700000000','!eva:x','46705555555','Eva E (WA)',true,false)`,
		// Bot ghost: skipped.
		`INSERT INTO ghost VALUES ('wa','bot-1','Some bot',true)`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

func TestNamesInfo(t *testing.T) {
	rep, err := namesInfo(namesTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Contacts != 5 || rep.OK != 2 || len(rep.Fixable) != 2 || len(rep.NoName) != 1 || rep.RoomsWrong != 1 {
		t.Fatalf("report: %+v", rep)
	}
	got := map[string]string{}
	for _, e := range rep.Fixable {
		got[e.GhostID] = e.Expected
	}
	if got["46701111111"] != "Anna Svensson (WA)" || got["lid-999"] != "Bert B (WA)" {
		t.Fatalf("expected names: %v", got)
	}
	if rep.NoName[0].GhostID != "46703333333" {
		t.Fatalf("no-name: %+v", rep.NoName)
	}
}

// fakeMatrix keeps profile names and room state, like Synapse would.
type fakeMatrix struct {
	mu    sync.Mutex
	state map[string]string // path without query -> JSON
	puts  []string
}

func newFakeMatrix() *fakeMatrix {
	return &fakeMatrix{state: map[string]string{
		"/_matrix/client/v3/profile/@wa01_46701111111:x.eu/displayname": `{"displayname":"+46701111111 (WA)"}`,
		"/_matrix/client/v3/profile/@wa01_lid-999:x.eu/displayname":     `{"displayname":"bertie (WA)"}`,
		// Dana: the database says "Dana (WA)", Matrix never got it.
		"/_matrix/client/v3/profile/@wa01_46704444444:x.eu/displayname":              `{"displayname":"+46704444444 (WA)"}`,
		"/_matrix/client/v3/profile/@wa01_46705555555:x.eu/displayname":              `{"displayname":"Eva E (WA)"}`,
		"/_matrix/client/v3/rooms/!anna:x/state/m.room.name/":                        `{"name":"+46701111111 (WA)"}`,
		"/_matrix/client/v3/rooms/!eva:x/state/m.room.name/":                         `{"name":"+46705555555 (WA)"}`,
		"/_matrix/client/v3/rooms/!eva:x/state/m.room.member/@wa01_46705555555:x.eu": `{"membership":"join","displayname":"+46705555555 (WA)","avatar_url":"mxc://x/eva"}`,
	}}
}

func (f *fakeMatrix) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(401)
		return
	}
	key := r.URL.Path
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		v, ok := f.state[key]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND"}`))
			return
		}
		_, _ = w.Write([]byte(v))
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.state[key] = string(b)
		f.puts = append(f.puts, r.URL.EscapedPath()+"?"+r.URL.RawQuery+" "+string(b))
		_, _ = w.Write([]byte("{}"))
	}
}

func TestNamesInfoLive(t *testing.T) {
	path := namesTestDB(t)
	f := newFakeMatrix()
	srv := httptest.NewServer(f)
	defer srv.Close()
	rep, err := namesInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := NamesFixConfig{Upstream: srv.URL, ASToken: "tok", BotMXID: "@wa01bot:x.eu", UsernameTemplate: "wa01_{{.}}"}
	m, ghost, err := cfg.client(srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	dms, _ := loadDMPortals(db)
	db.Close()
	liveCheck(rep, dms, m, cfg.BotMXID, ghost)
	if !rep.LiveChecked || rep.LiveError != "" || rep.OK != 0 || len(rep.Fixable) != 4 {
		t.Fatalf("live report: %+v", rep)
	}
	for _, e := range rep.Fixable {
		if e.GhostID == "46705555555" && len(e.Live) != 2 {
			t.Fatalf("eva: %+v", e)
		}
	}
}

func TestNamesFix(t *testing.T) {
	path := namesTestDB(t)
	f := newFakeMatrix()
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := NamesFixConfig{Upstream: srv.URL, ASToken: "tok", BotMXID: "@wa01bot:x.eu", UsernameTemplate: "wa01_{{.}}"}
	res, err := namesFix(path, cfg, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.puts, "\n")
	if res.Contacts != 3 || res.Rooms != 2 || len(res.Failed) != 0 {
		t.Fatalf("result: %+v puts:\n%s", res, joined)
	}
	for _, want := range []string{
		"/_matrix/client/v3/profile/@wa01_46701111111:x.eu/displayname?user_id=%40wa01_46701111111%3Ax.eu",
		"/_matrix/client/v3/profile/@wa01_lid-999:x.eu/displayname",
		"/_matrix/client/v3/profile/@wa01_46704444444:x.eu/displayname",
		"/_matrix/client/v3/rooms/%21anna:x/state/m.room.name/?user_id=%40wa01bot%3Ax.eu",
		"/_matrix/client/v3/rooms/%21eva:x/state/m.room.name/",
		`/_matrix/client/v3/rooms/%21eva:x/state/m.room.member/@wa01_46705555555:x.eu?user_id=%40wa01_46705555555%3Ax.eu {"avatar_url":"mxc://x/eva","displayname":"Eva E (WA)","membership":"join"}`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing call %q in\n%s", want, joined)
		}
	}
	// Database and Matrix now agree.
	rep, err := namesInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	m, ghost, _ := cfg.client(srv.Client())
	db, _ := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	dms, _ := loadDMPortals(db)
	db.Close()
	liveCheck(rep, dms, m, cfg.BotMXID, ghost)
	if len(rep.Fixable) != 0 || rep.OK != 4 {
		t.Fatalf("after fix: %+v", rep)
	}
	raw, _ := json.Marshal(rep)
	if !strings.Contains(string(raw), `"no_name"`) {
		t.Fatal(string(raw))
	}
}
