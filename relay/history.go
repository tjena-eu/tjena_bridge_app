package main

// history-info: how far the WhatsApp history import got, for the bridge
// app's "History import" screen. Reads the bridge database (read-only) and
// its JSON log file. Nothing here changes the bridge.
//
//	librelay.so history-info <bridge.db> <log_dir>
//
// How the import works (mautrix-whatsapp, unmodified):
//  1. The phone sends the history in chunks. Each chunk's messages are stored
//     in whatsapp_history_sync_message.
//  2. Once no chunk arrived for history_sync.dispatch_wait, a room is created
//     for each recent chat and filled from the stored messages, oldest first.
//     Stored messages of that chat are then deleted.
//  3. If one message fails to send (for example Synapse rate-limits your own
//     messages sent through double puppeting), the rest of that chat is
//     skipped and its stored messages stay in the table. Those are counted
//     here as "not copied".

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type HistoryInfo struct {
	Logins []HistoryLogin `json:"logins"`
	Log    HistoryLog     `json:"log"`
	Errors []string       `json:"errors,omitempty"`
}

type HistoryLogin struct {
	ID                 string        `json:"id"`
	Name               string        `json:"name"`
	LoggedInAt         int64         `json:"logged_in_at"`
	RoomsPending       bool          `json:"rooms_pending"` // chat rooms not created yet
	PendingChunks      int           `json:"pending_chunks"`
	Conversations      int           `json:"conversations"`       // chats the phone sent history for
	ConversationsRooms int           `json:"conversations_rooms"` // ... that got a room
	StoredMessages     int           `json:"stored_messages"`     // history waiting in the bridge DB
	NotCopied          int           `json:"not_copied"`          // stored, but the chat's room exists
	Fillable           int           `json:"fillable"`            // ... and newer than the room's newest message
	BlockedChats       int           `json:"blocked_chats"`       // chats with not-copied history all older than the room's newest message
	FixableChats       int           `json:"fixable_chats"`       // chats in state "fixable"
	UnfixableChats     int           `json:"unfixable_chats"`     // chats in state "unfixable"
	UnfixableMessages  int           `json:"unfixable_messages"`  // stored messages of those chats
	Rooms              int           `json:"rooms"`
	BridgedMessages    int           `json:"bridged_messages"`
	Chats              []HistoryChat `json:"chats"`
}

type HistoryChat struct {
	JID        string `json:"jid"`
	Name       string `json:"name"`
	HasRoom    bool   `json:"has_room"`
	Bridged    int    `json:"bridged"`     // messages in the room
	Stored     int    `json:"stored"`      // history still waiting in the DB
	Backfilled int    `json:"backfilled"`  // messages the bridge tried to copy (log)
	Failed     bool   `json:"failed"`      // copying stopped with an error (log)
	RateLimit  bool   `json:"rate_limit"`  // ... because Synapse said "too many requests"
	LastMsgTS  int64  `json:"last_msg_ts"` // newest message on the phone, unix seconds
	RoomNewest int64  `json:"room_newest"` // newest message in the room, unix seconds
	Fillable   int    `json:"fillable"`    // stored messages newer than RoomNewest (catch-up can add these)
	// CreateFailed: how often the bridge failed to create this chat's room (log).
	CreateFailed int `json:"create_failed"`
	// State: "ok", "fixable" (catch-up can still add messages), "unfixable"
	// (nothing more can be added, see Reason), "held" (no room yet, waiting)
	// or "" (nothing stored, no room).
	State string `json:"state"`
	// Reason for "unfixable": "newer_messages" (the missing ones would land
	// before newer messages already in the chat) or "create_failed" (the
	// bridge keeps failing to create the chat; it no longer holds up others).
	Reason string `json:"reason,omitempty"`
}

// createFailLimit: after this many failed attempts to create a chat's room,
// the chat counts as unfixable and history-settle takes it out of the queue.
const createFailLimit = 2

func (c *HistoryChat) classify() {
	switch {
	case c.HasRoom && c.Stored > 0 && c.Fillable > 0:
		c.State = "fixable"
	case c.HasRoom && c.Stored > 0:
		c.State, c.Reason = "unfixable", "newer_messages"
	case c.HasRoom:
		c.State = "ok"
	case c.CreateFailed >= createFailLimit:
		c.State, c.Reason = "unfixable", "create_failed"
	case c.Stored > 0:
		c.State = "held"
	}
}

type HistoryChunk struct {
	Time          string `json:"time"`
	Type          string `json:"type"`
	Order         int    `json:"order"`
	Progress      int    `json:"progress"`
	Conversations int    `json:"conversations"`
}

type HistoryLog struct {
	Found             bool           `json:"found"`
	Since             string         `json:"since,omitempty"`
	Chunks            []HistoryChunk `json:"chunks"`
	Progress          map[string]int `json:"progress"` // sync type -> last percentage
	Saved             int            `json:"saved"`
	SaveFailed        int            `json:"save_failed"`
	Ignored           int            `json:"ignored"` // message types that are never bridged
	ParseDropped      int            `json:"parse_dropped"`
	RoomsCreating     bool           `json:"rooms_creating"`
	RoomsTarget       int            `json:"rooms_target"`
	RoomsDone         bool           `json:"rooms_done"`
	BackfillChats     int            `json:"backfill_chats"`
	BackfillMessages  int            `json:"backfill_messages"`
	BackfillFailed    int            `json:"backfill_failed"`
	SendFailed        int            `json:"send_failed"`
	RateLimited       int            `json:"rate_limited"`
	RecentErrors      []LogProblem   `json:"recent_errors"`
	perPortalSent     map[string]int
	perPortalFailed   map[string]bool
	perPortalRateLimt map[string]bool
	perChatCreateFail map[string]int
}

type LogProblem struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
	Error string `json:"error,omitempty"`
	Chat  string `json:"chat,omitempty"`
}

func runHistoryInfo(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: history-info <bridge.db> <log_dir>")
	}
	info, err := historyInfo(args[0], args[1])
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(info)
}

func historyInfo(dbPath, logDir string) (*HistoryInfo, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("bridge database: %w", err)
	}
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	info := &HistoryInfo{Logins: []HistoryLogin{}}
	addErr := func(what string, err error) {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			info.Errors = append(info.Errors, what+": "+err.Error())
		}
	}

	rows, err := db.Query("SELECT id, remote_name, metadata FROM user_login")
	addErr("logins", err)
	if err == nil {
		for rows.Next() {
			var l HistoryLogin
			var meta []byte
			if rows.Scan(&l.ID, &l.Name, &meta) != nil {
				continue
			}
			var m struct {
				LoggedInAt   int64 `json:"logged_in_at"`
				PortalsNeedC bool  `json:"history_sync_portals_need_creating"`
			}
			_ = json.Unmarshal(meta, &m)
			l.LoggedInAt, l.RoomsPending = m.LoggedInAt, m.PortalsNeedC
			l.Chats = []HistoryChat{}
			info.Logins = append(info.Logins, l)
		}
		rows.Close()
	}

	var since time.Time
	for _, l := range info.Logins {
		t := time.Unix(l.LoggedInAt, 0)
		if l.LoggedInAt > 0 && (since.IsZero() || t.Before(since)) {
			since = t
		}
	}
	info.Log = readHistoryLog(logDir, since)

	for i := range info.Logins {
		l := &info.Logins[i]
		count := func(q string, args ...any) int {
			var n int
			addErr("count", db.QueryRow(q, args...).Scan(&n))
			return n
		}
		l.PendingChunks = count("SELECT count(*) FROM whatsapp_history_sync_notification WHERE user_login_id=?", l.ID)

		chats := map[string]*HistoryChat{}
		get := func(jid string) *HistoryChat {
			c := chats[jid]
			if c == nil {
				c = &HistoryChat{JID: jid}
				chats[jid] = c
			}
			return c
		}
		// Rooms of this login, with their bridged message counts.
		rows, err := db.Query(`SELECT p.id, p.name, p.mxid,
				(SELECT count(DISTINCT m.id) FROM message m WHERE m.room_id=p.id AND m.room_receiver=p.receiver)
			FROM portal p JOIN user_portal up ON up.portal_id=p.id AND up.portal_receiver=p.receiver
			WHERE up.login_id=?`, l.ID)
		addErr("rooms", err)
		if err == nil {
			for rows.Next() {
				var jid, name string
				var mxid sql.NullString
				var n int
				if rows.Scan(&jid, &name, &mxid, &n) != nil {
					continue
				}
				c := get(jid)
				c.Name, c.HasRoom, c.Bridged = name, mxid.Valid && mxid.String != "", n
				var newest sql.NullInt64
				_ = db.QueryRow(`SELECT max(timestamp) FROM message WHERE room_id=? AND room_receiver=?`, jid, l.ID).Scan(&newest)
				if !newest.Valid {
					// group portals have an empty receiver
					_ = db.QueryRow(`SELECT max(timestamp) FROM message WHERE room_id=?`, jid).Scan(&newest)
				}
				if newest.Valid {
					c.RoomNewest = newest.Int64 / 1e9
				}
				if c.HasRoom {
					l.Rooms++
					l.BridgedMessages += n
				}
			}
			rows.Close()
		}
		// Chats the phone sent history for.
		rows, err = db.Query(`SELECT chat_jid, coalesce(last_message_timestamp, 0),
				(SELECT count(*) FROM whatsapp_history_sync_message hm
					WHERE hm.user_login_id=hc.user_login_id AND hm.chat_jid=hc.chat_jid)
			FROM whatsapp_history_sync_conversation hc WHERE user_login_id=?`, l.ID)
		addErr("history", err)
		if err == nil {
			for rows.Next() {
				var jid string
				var ts int64
				var stored int
				if rows.Scan(&jid, &ts, &stored) != nil {
					continue
				}
				c := get(jid)
				c.LastMsgTS, c.Stored = ts, stored
				l.Conversations++
				l.StoredMessages += stored
				if c.HasRoom {
					l.ConversationsRooms++
					l.NotCopied += stored
					if stored > 0 {
						// The bridge only catches up when the phone's newest message is newer
						// than the room's, and then only adds messages after the room's newest.
						if ts > c.RoomNewest {
							_ = db.QueryRow(`SELECT count(*) FROM whatsapp_history_sync_message
								WHERE user_login_id=? AND chat_jid=? AND timestamp>?`, l.ID, jid, c.RoomNewest).Scan(&c.Fillable)
						}
						l.Fillable += c.Fillable
						if c.Fillable == 0 {
							l.BlockedChats++
						}
					}
				}
			}
			rows.Close()
		}
		for jid, c := range chats {
			c.Backfilled = info.Log.perPortalSent[jid]
			c.Failed = info.Log.perPortalFailed[jid]
			c.RateLimit = info.Log.perPortalRateLimt[jid]
			c.CreateFailed = info.Log.perChatCreateFail[jid]
			c.classify()
			switch c.State {
			case "fixable":
				l.FixableChats++
			case "unfixable":
				l.UnfixableChats++
				l.UnfixableMessages += c.Stored
			}
			l.Chats = append(l.Chats, *c)
		}
		// Problems first, then newest.
		sort.Slice(l.Chats, func(a, b int) bool {
			ca, cb := l.Chats[a], l.Chats[b]
			rank := func(c HistoryChat) int {
				switch {
				case c.State == "fixable":
					return 0
				case c.State == "unfixable":
					return 1
				case c.Failed:
					return 2
				}
				return 3
			}
			if ra, rb := rank(ca), rank(cb); ra != rb {
				return ra < rb
			}
			return ca.LastMsgTS > cb.LastMsgTS
		})
		// Enough for choosing chats to copy; keeps the JSON small.
		if len(l.Chats) > 1500 {
			l.Chats = l.Chats[:1500]
		}
	}
	return info, nil
}

// readHistoryLog summarises the bridge's JSON log since [since] (the first
// login). Only counts, ids and error texts; no message content is logged.
func readHistoryLog(dir string, since time.Time) HistoryLog {
	out := HistoryLog{
		Chunks: []HistoryChunk{}, Progress: map[string]int{}, RecentErrors: []LogProblem{},
		perPortalSent: map[string]int{}, perPortalFailed: map[string]bool{}, perPortalRateLimt: map[string]bool{},
		perChatCreateFail: map[string]int{},
	}
	files, _ := filepath.Glob(filepath.Join(dir, "bridge*.log"))
	sort.Slice(files, func(i, j int) bool { // rotated backups first, current file last
		ci, cj := filepath.Base(files[i]) == "bridge.log", filepath.Base(files[j]) == "bridge.log"
		if ci != cj {
			return cj
		}
		return files[i] < files[j]
	})
	if !since.IsZero() {
		out.Since = since.UTC().Format(time.RFC3339)
		since = since.Add(-time.Minute)
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		out.Found = true
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			var e map[string]any
			if json.Unmarshal(line, &e) != nil {
				continue
			}
			ts, _ := e["time"].(string)
			if !since.IsZero() {
				if t, err := time.Parse(time.RFC3339Nano, ts); err == nil && t.Before(since) {
					continue
				}
			}
			out.add(e, ts)
		}
		fh.Close()
	}
	if len(out.Chunks) > 60 {
		out.Chunks = out.Chunks[len(out.Chunks)-60:]
	}
	if len(out.RecentErrors) > 40 {
		out.RecentErrors = out.RecentErrors[len(out.RecentErrors)-40:]
	}
	return out
}

func num(e map[string]any, k string) int {
	if v, ok := e[k].(float64); ok {
		return int(v)
	}
	return 0
}

func str(e map[string]any, k string) string {
	s, _ := e[k].(string)
	return s
}

func isRateLimit(s string) bool {
	return strings.Contains(s, "M_LIMIT_EXCEEDED") || strings.Contains(s, "HTTP 429") || strings.Contains(s, "Too Many Requests")
}

func (out *HistoryLog) add(e map[string]any, ts string) {
	msg, level, errText := str(e, "message"), str(e, "level"), str(e, "error")
	portal := str(e, "portal_id")
	switch msg {
	case "Storing history sync":
		t := strings.TrimPrefix(str(e, "sync_type"), "HistorySync_")
		out.Chunks = append(out.Chunks, HistoryChunk{
			Time: ts, Type: t, Order: num(e, "chunk_order"), Progress: num(e, "progress"),
			Conversations: num(e, "conversation_count"),
		})
		out.Progress[t] = num(e, "progress")
	case "Finished storing history sync":
		out.Saved += num(e, "total_saved_count")
		out.SaveFailed += num(e, "total_failed_count")
	case "Collected messages to save from history sync conversation":
		out.Ignored += num(e, "ignored_msg_type_count")
	case "Dropping historical message due to parse error":
		out.ParseDropped++
	case "Creating portals from history sync":
		out.RoomsCreating, out.RoomsDone = true, false
		out.RoomsTarget = num(e, "conversation_count")
	case "Finished processing all history sync chat resync events":
		out.RoomsDone = true
	case "Sending backfill messages":
		out.BackfillChats++
		out.BackfillMessages += num(e, "message_count")
		if portal != "" {
			out.perPortalSent[portal] += num(e, "message_count")
		}
	case "Failed to send forward backfill", "Failed to send backfill messages":
		out.BackfillFailed++
		if portal != "" {
			out.perPortalFailed[portal] = true
			if isRateLimit(errText) {
				out.perPortalRateLimt[portal] = true
			}
		}
	case "Failed to send message part to Matrix":
		out.SendFailed++
	case "Failed to get chat info", "Failed to create portal to handle event":
		// Room creation failed. The bridge doesn't mark the chat as done, so
		// it retries it first on every start (see history-settle).
		if jid := str(e, "chat_jid"); jid != "" {
			out.perChatCreateFail[jid]++
		} else if portal != "" {
			out.perChatCreateFail[portal]++
		}
	}
	if isRateLimit(errText) || (level == "warn" && isRateLimit(msg)) {
		out.RateLimited++
		if portal != "" {
			out.perPortalRateLimt[portal] = true
		}
	}
	if level == "error" || level == "fatal" {
		if len(errText) > 300 {
			errText = errText[:300] + "…"
		}
		out.RecentErrors = append(out.RecentErrors, LogProblem{Time: ts, Level: level, Msg: msg, Error: errText, Chat: portal})
	}
}

// history-catchup: copies stored history the bridge hasn't put into tjena
// yet. Run it only while the bridge is stopped.
//
//	librelay.so history-catchup <bridge.db> [--mode missing|held|both]
//	    [--login <login id>] [--chats jid,jid,...]
//
// Modes (--chats overrides them):
//   - missing: chats that have a room but whose import stopped early (only
//     those with stored messages newer than the room's newest message: older
//     ones can't be put in between afterwards).
//   - held: chats the phone sent history for that got no room yet (the
//     bridge only creates rooms for the most recent chats at login).
//
// It uses the bridge's own mechanism, nothing new: the bridge re-runs
// "create portals from history sync" for every conversation of a login
// that isn't marked as synced, when the login's
// history_sync_portals_need_creating flag is set. So the chosen chats get
// their mark cleared, every other pending chat of that login is marked as
// synced (so it isn't pulled in too), and the flag is set. Chats with a room
// get a resync that forward-fills from the stored history (up to
// backfill.max_catchup_messages); held-back chats get a new room filled with
// up to backfill.max_initial_messages.
//
// The bridge creates at most network.history_sync.max_initial_conversations
// rooms per run; history-catchup.json in the data dir lifts that limit for
// the next start (see catchupActive).
type CatchupOptions struct {
	Mode  string   // "missing" (default), "held" or "both"
	Login string   // only this login ("" = all)
	Chats []string // exactly these chats (overrides Mode)
}

type CatchupResult struct {
	Chats   int            `json:"chats"`
	Stored  int            `json:"stored"`
	Missing int            `json:"missing"` // chats with a room
	Held    int            `json:"held"`    // chats that get a new room
	Logins  map[string]int `json:"logins"`
}

const catchupFile = "history-catchup.json"

// catchupActive: a catch-up was started recently, so the next bridge start
// must not limit how many chats get created.
func catchupActive(dataDir string) bool {
	raw, err := os.ReadFile(filepath.Join(dataDir, catchupFile))
	if err != nil {
		return false
	}
	var f struct {
		Until int64 `json:"until"`
	}
	return json.Unmarshal(raw, &f) == nil && time.Now().Unix() < f.Until
}

func runHistoryCatchup(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: history-catchup <bridge.db> [--mode missing|held|both] [--login ID] [--chats a,b]")
	}
	var o CatchupOptions
	for i := 1; i < len(args); i++ {
		if i+1 >= len(args) {
			return fmt.Errorf("%s needs a value", args[i])
		}
		switch args[i] {
		case "--mode":
			o.Mode = args[i+1]
		case "--login":
			o.Login = args[i+1]
		case "--chats":
			for _, c := range strings.Split(args[i+1], ",") {
				if c = strings.TrimSpace(c); c != "" {
					o.Chats = append(o.Chats, c)
				}
			}
		default:
			return fmt.Errorf("unknown option %s", args[i])
		}
		i++
	}
	res, err := historyCatchup(args[0], o)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}

type catchupConv struct {
	login, jid string
	synced     sql.NullInt64
	stored     int
	hasRoom    bool
	fillable   int
}

// loadCatchupConvs reads every history conversation with what catch-up
// needs to choose.
func loadCatchupConvs(tx *sql.Tx) ([]catchupConv, error) {
	rows, err := tx.Query(`SELECT hc.user_login_id, hc.chat_jid, hc.synced_login_ts,
			(SELECT count(*) FROM whatsapp_history_sync_message hm
				WHERE hm.user_login_id=hc.user_login_id AND hm.chat_jid=hc.chat_jid),
			EXISTS (SELECT 1 FROM portal p JOIN user_portal up
					ON up.portal_id=p.id AND up.portal_receiver=p.receiver
				WHERE up.login_id=hc.user_login_id AND p.id=hc.chat_jid
					AND p.mxid IS NOT NULL AND p.mxid<>'')
		FROM whatsapp_history_sync_conversation hc`)
	if err != nil {
		return nil, fmt.Errorf("find chats: %w", err)
	}
	var out []catchupConv
	for rows.Next() {
		var c catchupConv
		if err = rows.Scan(&c.login, &c.jid, &c.synced, &c.stored, &c.hasRoom); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	for i := range out {
		c := &out[i]
		if !c.hasRoom || c.stored == 0 {
			continue
		}
		// Only messages newer than the room's newest can still be added.
		var newest sql.NullInt64
		if tx.QueryRow(`SELECT max(timestamp) FROM message WHERE room_id=? AND room_receiver=?`, c.jid, c.login).Scan(&newest) != nil || !newest.Valid {
			_ = tx.QueryRow(`SELECT max(timestamp) FROM message WHERE room_id=?`, c.jid).Scan(&newest)
		}
		since := int64(0)
		if newest.Valid {
			since = newest.Int64 / 1e9
		}
		if err := tx.QueryRow(`SELECT count(*) FROM whatsapp_history_sync_message
				WHERE user_login_id=? AND chat_jid=? AND timestamp>?`, c.login, c.jid, since).Scan(&c.fillable); err != nil {
			c.fillable = c.stored // no timestamps to compare: try all
		}
	}
	return out, nil
}

func openWrite(dbPath string) (*sql.DB, *sql.Tx, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil, fmt.Errorf("bridge database: %w", err)
	}
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, tx, nil
}

// loginMeta reads a login's metadata (logged_in_at and the rest, kept as is).
func loginMeta(tx *sql.Tx, login string) (map[string]any, int64, error) {
	var raw []byte
	if err := tx.QueryRow(`SELECT metadata FROM user_login WHERE id=?`, login).Scan(&raw); err != nil {
		return nil, 0, fmt.Errorf("read login: %w", err)
	}
	meta := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, 0, fmt.Errorf("login metadata: %w", err)
		}
	}
	ts, _ := meta["logged_in_at"].(float64)
	return meta, int64(ts), nil
}

func saveLoginMeta(tx *sql.Tx, login string, meta map[string]any) error {
	raw, _ := json.Marshal(meta)
	_, err := tx.Exec(`UPDATE user_login SET metadata=? WHERE id=?`, string(raw), login)
	return err
}

func historyCatchup(dbPath string, o CatchupOptions) (*CatchupResult, error) {
	switch o.Mode {
	case "":
		o.Mode = "missing"
	case "missing", "held", "both":
	default:
		return nil, fmt.Errorf("unknown mode %q", o.Mode)
	}
	db, tx, err := openWrite(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	convs, err := loadCatchupConvs(tx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, c := range o.Chats {
		want[c] = true
	}
	chosen := func(c catchupConv) bool {
		if o.Login != "" && c.login != o.Login {
			return false
		}
		if len(want) > 0 {
			// Only chats where something can still be added.
			return want[c.jid] && ((c.hasRoom && c.fillable > 0) || (!c.hasRoom && c.stored > 0))
		}
		missing := c.hasRoom && c.fillable > 0
		held := !c.hasRoom && c.stored > 0
		switch o.Mode {
		case "missing":
			return missing
		case "held":
			return held
		}
		return missing || held
	}
	res := &CatchupResult{Logins: map[string]int{}}
	for _, c := range convs {
		if chosen(c) {
			res.Chats++
			res.Stored += c.stored
			res.Logins[c.login]++
			if c.hasRoom {
				res.Missing++
			} else {
				res.Held++
			}
		}
	}
	if res.Chats == 0 {
		return res, nil
	}
	for login := range res.Logins {
		meta, loginTS, err := loginMeta(tx, login)
		if err != nil {
			return nil, err
		}
		for _, c := range convs {
			if c.login != login {
				continue
			}
			if chosen(c) {
				_, err = tx.Exec(`UPDATE whatsapp_history_sync_conversation SET synced_login_ts=NULL
					WHERE user_login_id=? AND chat_jid=?`, login, c.jid)
			} else if !c.synced.Valid || c.synced.Int64 < loginTS {
				// Pending but not chosen: mark as done so the bridge skips it.
				_, err = tx.Exec(`UPDATE whatsapp_history_sync_conversation SET synced_login_ts=?
					WHERE user_login_id=? AND chat_jid=?`, loginTS, login, c.jid)
			}
			if err != nil {
				return nil, fmt.Errorf("mark chat: %w", err)
			}
		}
		meta["history_sync_portals_need_creating"] = true
		if err = saveLoginMeta(tx, login, meta); err != nil {
			return nil, fmt.Errorf("update login: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	flag, _ := json.Marshal(map[string]any{"until": time.Now().Add(6 * time.Hour).Unix(), "chats": res.Chats})
	_ = os.WriteFile(filepath.Join(filepath.Dir(dbPath), catchupFile), flag, 0o600)
	return res, nil
}

// history-stop: stops the bridge from creating and filling more chats from
// history (bridge stopped). Every pending chat is marked as synced and the
// logins' "portals need creating" flag is cleared. Stored messages stay, so
// they can be copied later with history-catchup.
//
//	librelay.so history-stop <bridge.db>
type StopResult struct {
	Stopped int `json:"stopped"` // chats that were still waiting
}

func runHistoryStop(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: history-stop <bridge.db>")
	}
	res, err := historyStop(args[0])
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}

func historyStop(dbPath string) (*StopResult, error) {
	db, tx, err := openWrite(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	var logins []string
	rows, err := tx.Query(`SELECT id FROM user_login`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			logins = append(logins, id)
		}
	}
	rows.Close()
	res := &StopResult{}
	for _, login := range logins {
		meta, loginTS, err := loginMeta(tx, login)
		if err != nil {
			return nil, err
		}
		r, err := tx.Exec(`UPDATE whatsapp_history_sync_conversation SET synced_login_ts=?
			WHERE user_login_id=? AND (synced_login_ts IS NULL OR synced_login_ts<?)`, loginTS, login, loginTS)
		if err != nil {
			return nil, fmt.Errorf("mark chats: %w", err)
		}
		n, _ := r.RowsAffected()
		res.Stopped += int(n)
		meta["history_sync_portals_need_creating"] = false
		if err = saveLoginMeta(tx, login, meta); err != nil {
			return nil, fmt.Errorf("update login: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	_ = os.Remove(filepath.Join(filepath.Dir(dbPath), catchupFile))
	return res, nil
}

// history-settle: run before every bridge start (bridge stopped). Takes chats
// out of the bridge's "create from history" queue whose room creation keeps
// failing (for example WhatsApp refuses their chat info).
//
//	librelay.so history-settle <bridge.db> <log_dir>
//
// Why: the bridge only marks a chat as done when its room was created. A chat
// that fails stays first in line (the queue is the newest chats, at most
// max_initial_conversations per start) and is retried on every start, so the
// chats behind it never get their turn. The bridge also never records that
// the import finished. Marking such a chat as done ends that: the next start
// moves on to the other chats. Its stored messages stay, and a new message in
// that chat still creates its room as usual.
type SettleResult struct {
	Settled []string `json:"settled"` // chats taken out of the queue
}

func runHistorySettle(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: history-settle <bridge.db> <log_dir>")
	}
	res, err := historySettle(args[0], args[1])
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}

func historySettle(dbPath, logDir string) (*SettleResult, error) {
	res := &SettleResult{Settled: []string{}}
	if _, err := os.Stat(dbPath); err != nil {
		return res, nil // not set up yet
	}
	db, tx, err := openWrite(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	type pend struct {
		login, jid string
		loginTS    int64
	}
	var logins []string
	rows, err := tx.Query(`SELECT id FROM user_login`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			logins = append(logins, id)
		}
	}
	rows.Close()
	var since time.Time
	var pending []pend
	for _, login := range logins {
		_, loginTS, err := loginMeta(tx, login)
		if err != nil {
			return nil, err
		}
		if t := time.Unix(loginTS, 0); loginTS > 0 && (since.IsZero() || t.Before(since)) {
			since = t
		}
		rows, err := tx.Query(`SELECT hc.chat_jid FROM whatsapp_history_sync_conversation hc
			WHERE hc.user_login_id=? AND (hc.synced_login_ts IS NULL OR hc.synced_login_ts<?)
				AND NOT EXISTS (SELECT 1 FROM portal p WHERE p.id=hc.chat_jid AND p.mxid IS NOT NULL AND p.mxid<>'')`,
			login, loginTS)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var jid string
			if rows.Scan(&jid) == nil {
				pending = append(pending, pend{login, jid, loginTS})
			}
		}
		rows.Close()
	}
	if len(pending) == 0 {
		return res, nil
	}
	fails := readHistoryLog(logDir, since).perChatCreateFail
	for _, p := range pending {
		if fails[p.jid] < createFailLimit {
			continue
		}
		if _, err := tx.Exec(`UPDATE whatsapp_history_sync_conversation SET synced_login_ts=?
			WHERE user_login_id=? AND chat_jid=?`, p.loginTS, p.login, p.jid); err != nil {
			return nil, fmt.Errorf("mark chat: %w", err)
		}
		res.Settled = append(res.Settled, p.jid)
	}
	if len(res.Settled) == 0 {
		return res, nil
	}
	return res, tx.Commit()
}
