package main

// Chat-name check and repair for the bridge app's debug screen.
//
//	librelay.so names-info <bridge.db>          read-only report (JSON)
//	librelay.so names-fix  <bridge.db> < cfg    apply fixes; bridge must be stopped
//
// Why names go stale: the bridge names a WhatsApp contact once, from whatever it
// knows at that moment (often just the phone number, before the contact list
// from the phone has arrived, or before our template preferred the address-book
// name). Afterwards it only re-checks a contact when they are active and at most
// every 7 days, so quiet chats keep showing a number.
//
// names-info computes the name the bridge itself would give each contact now,
// from the bridge's own contact store, with the same template and the same
// phone-number/LID merging as mautrix-whatsapp's contactToUserInfo. names-fix
// then does what the bridge's Ghost.UpdateInfo does: set the contact's Matrix
// display name, update DM room names that follow it, and save both in the
// bridge database so the bridge agrees on its next start.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
)

type contactInfo struct {
	FirstName, FullName, PushName, BusinessName, RedactedPhone string
}

// Fields the bridge's displayname template can use.
type displaynameParams struct {
	contactInfo
	Phone string
}

type NameEntry struct {
	GhostID  string   `json:"id"`
	Current  string   `json:"current"`
	Expected string   `json:"expected"`
	Source   string   `json:"source"` // contact, business, push, phone, unknown
	Phone    string   `json:"phone,omitempty"`
	Chats    []string `json:"chats,omitempty"` // DM rooms with this contact (current room name)
	// What Matrix shows, when it differs from the bridge database (live check):
	// e.g. "profile: +4917…", "room name: +4917…", "name in chat: +4917…".
	Live []string `json:"live,omitempty"`

	liveProfile bool                      // Matrix profile name is wrong
	liveRooms   map[string]bool           // room mxid -> its m.room.name is wrong
	liveMembers map[string]map[string]any // room mxid -> member content to fix
}

type NamesReport struct {
	Contacts   int         `json:"contacts"`    // contacts the bridge knows (ghosts)
	OK         int         `json:"ok"`          // already right
	Fixable    []NameEntry `json:"fixable"`     // a better name is known; names-fix applies it
	NoName     []NameEntry `json:"no_name"`     // WhatsApp knows no name, only the number
	RoomsWrong int         `json:"rooms_wrong"` // DM rooms whose name doesn't match the contact
	Template   string      `json:"template"`
	// Checked what Matrix shows too (not only the bridge database).
	LiveChecked bool   `json:"live_checked"`
	LiveError   string `json:"live_error,omitempty"`

	ok []NameEntry // the OK ones, for the live check
}

type namesDB struct {
	contacts map[string]contactInfo // full JID -> merged info over all logins
	lidToPN  map[string]string      // user parts
	pnToLID  map[string]string
}

type ghostRow struct {
	bridgeID, id, name string
}

type dmPortal struct {
	bridgeID, id, receiver, mxid, name string
}

var nameTmpl = template.Must(template.New("displayname").Parse(displaynameTemplate))

func loadNamesDB(db *sql.DB) (*namesDB, error) {
	n := &namesDB{contacts: map[string]contactInfo{}, lidToPN: map[string]string{}, pnToLID: map[string]string{}}
	rows, err := db.Query(`SELECT their_jid, COALESCE(first_name,''), COALESCE(full_name,''),
		COALESCE(push_name,''), COALESCE(business_name,''), COALESCE(redacted_phone,'') FROM whatsmeow_contacts`)
	if err != nil {
		return nil, fmt.Errorf("read contacts: %w", err)
	}
	for rows.Next() {
		var jid string
		var c contactInfo
		if err = rows.Scan(&jid, &c.FirstName, &c.FullName, &c.PushName, &c.BusinessName, &c.RedactedPhone); err != nil {
			rows.Close()
			return nil, err
		}
		// Several linked accounts may each have a row; fill gaps from any of them.
		old := n.contacts[jid]
		n.contacts[jid] = contactInfo{
			FirstName:     firstNonEmpty(old.FirstName, c.FirstName),
			FullName:      firstNonEmpty(old.FullName, c.FullName),
			PushName:      firstNonEmpty(old.PushName, c.PushName),
			BusinessName:  firstNonEmpty(old.BusinessName, c.BusinessName),
			RedactedPhone: firstNonEmpty(old.RedactedPhone, c.RedactedPhone),
		}
	}
	rows.Close()
	rows, err = db.Query(`SELECT lid, pn FROM whatsmeow_lid_map`)
	if err != nil {
		return nil, fmt.Errorf("read lid map: %w", err)
	}
	for rows.Next() {
		var lid, pn string
		if err = rows.Scan(&lid, &pn); err != nil {
			rows.Close()
			return nil, err
		}
		n.lidToPN[lid] = pn
		n.pnToLID[pn] = lid
	}
	rows.Close()
	return n, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// expectedName mirrors contactToUserInfo + FormatDisplayname. ok=false for
// ghost ids this check leaves alone (bots, special accounts).
func (n *namesDB) expectedName(ghostID string) (name, source, phone string, ok bool) {
	var user, server string
	switch {
	case strings.HasPrefix(ghostID, "lid-"):
		user, server = strings.TrimPrefix(ghostID, "lid-"), "lid"
	case strings.HasPrefix(ghostID, "bot-"), ghostID == "", ghostID == "0", ghostID == "13135550002":
		return "", "", "", false // bots, WhatsApp PSA, Meta AI: special-cased by the bridge
	default:
		user, server = ghostID, "s.whatsapp.net"
	}
	contact := n.contacts[user+"@"+server]
	var altUser, altServer string
	if server == "lid" {
		altUser, altServer = n.lidToPN[user], "s.whatsapp.net"
	} else {
		altUser, altServer = n.pnToLID[user], "lid"
	}
	if altUser != "" {
		extra := n.contacts[altUser+"@"+altServer]
		// Address-book names are only stored for phone-number JIDs.
		if altServer == "s.whatsapp.net" {
			contact.FirstName = firstNonEmpty(contact.FirstName, extra.FirstName)
			contact.FullName = firstNonEmpty(contact.FullName, extra.FullName)
		}
		if contact.PushName == "" || (extra.PushName != "" && altServer == "s.whatsapp.net") {
			contact.PushName = firstNonEmpty(extra.PushName, contact.PushName)
		}
		if contact.BusinessName == "" || (extra.BusinessName != "" && altServer == "s.whatsapp.net") {
			contact.BusinessName = firstNonEmpty(extra.BusinessName, contact.BusinessName)
		}
	}
	if server == "s.whatsapp.net" {
		phone = "+" + user
	} else if altUser != "" {
		phone = "+" + altUser
	}
	if contact.RedactedPhone == "" && phone != "" {
		contact.RedactedPhone = redactPhone(phone)
	}
	var buf strings.Builder
	if err := nameTmpl.Execute(&buf, displaynameParams{contactInfo: contact, Phone: phone}); err != nil {
		return "", "", "", false
	}
	switch {
	case contact.FullName != "":
		source = "contact"
	case contact.BusinessName != "":
		source = "business"
	case contact.PushName != "":
		source = "push"
	case phone != "" || contact.RedactedPhone != "":
		source = "phone"
	default:
		source = "unknown"
	}
	return buf.String(), source, phone, true
}

func redactPhone(phone string) string {
	if len(phone) <= 4 {
		return phone
	}
	return phone[:2] + strings.Repeat("∙", len(phone)-4) + phone[len(phone)-2:]
}

func loadGhosts(db *sql.DB) ([]ghostRow, error) {
	rows, err := db.Query(`SELECT bridge_id, id, name FROM ghost`)
	if err != nil {
		return nil, fmt.Errorf("read ghosts: %w", err)
	}
	defer rows.Close()
	var out []ghostRow
	for rows.Next() {
		var g ghostRow
		if err = rows.Scan(&g.bridgeID, &g.id, &g.name); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DM rooms whose name follows the contact (not renamed by the user).
func loadDMPortals(db *sql.DB) (map[string][]dmPortal, error) {
	rows, err := db.Query(`SELECT bridge_id, id, receiver, COALESCE(mxid,''), name, other_user_id FROM portal
		WHERE other_user_id IS NOT NULL AND other_user_id<>'' AND NOT name_is_custom`)
	if err != nil {
		return nil, fmt.Errorf("read portals: %w", err)
	}
	defer rows.Close()
	out := map[string][]dmPortal{}
	for rows.Next() {
		var p dmPortal
		var other string
		if err = rows.Scan(&p.bridgeID, &p.id, &p.receiver, &p.mxid, &p.name, &other); err != nil {
			return nil, err
		}
		out[other] = append(out[other], p)
	}
	return out, rows.Err()
}

func namesInfo(dbPath string) (*NamesReport, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("bridge database: %w", err)
	}
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return buildNamesReport(db)
}

func buildNamesReport(db *sql.DB) (*NamesReport, error) {
	n, err := loadNamesDB(db)
	if err != nil {
		return nil, err
	}
	ghosts, err := loadGhosts(db)
	if err != nil {
		return nil, err
	}
	dms, err := loadDMPortals(db)
	if err != nil {
		return nil, err
	}
	rep := &NamesReport{Fixable: []NameEntry{}, NoName: []NameEntry{}, Template: displaynameTemplate}
	for _, g := range ghosts {
		expected, source, phone, ok := n.expectedName(g.id)
		if !ok {
			continue
		}
		rep.Contacts++
		e := NameEntry{GhostID: g.id, Current: g.name, Expected: expected, Source: source, Phone: phone}
		roomsWrong := 0
		for _, p := range dms[g.id] {
			if p.mxid == "" {
				continue
			}
			e.Chats = append(e.Chats, p.name)
			if p.name != expected {
				roomsWrong++
			}
		}
		switch {
		case source == "phone" || source == "unknown":
			if looksLikeNumber(g.name) || g.name == "" {
				rep.NoName = append(rep.NoName, e)
			} else {
				rep.OK++ // a name came in some other way; leave it
			}
		case g.name != expected || roomsWrong > 0:
			rep.Fixable = append(rep.Fixable, e)
			rep.RoomsWrong += roomsWrong
		default:
			rep.OK++
			rep.ok = append(rep.ok, e)
		}
	}
	return rep, nil
}

// liveCheck compares what Matrix shows for the contacts the database calls
// OK: their profile name, the DM room name and their name in the DM (the
// member event, which clients use when a room has no name). The database can
// be right while Matrix isn't (a request that failed, a name changed while
// the bridge was offline), and names-info alone would then say "OK".
func liveCheck(rep *NamesReport, dms map[string][]dmPortal, m *matrixClient, botMXID string, ghostMXID func(string) string) {
	rep.LiveChecked = true
	jobs := make(chan NameEntry)
	results := make(chan NameEntry)
	var firstErr atomicString
	const workers = 8
	for range workers {
		go func() {
			for e := range jobs {
				results <- checkOneLive(e, dms[e.GhostID], m, botMXID, ghostMXID(e.GhostID), &firstErr)
			}
		}()
	}
	go func() {
		for _, e := range rep.ok {
			jobs <- e
		}
		close(jobs)
	}()
	var stillOK []NameEntry
	for range rep.ok {
		e := <-results
		if len(e.Live) > 0 {
			rep.OK--
			rep.Fixable = append(rep.Fixable, e)
			rep.RoomsWrong += len(e.liveRooms)
		} else {
			stillOK = append(stillOK, e)
		}
	}
	rep.ok = stillOK
	rep.LiveError = firstErr.get()
}

func checkOneLive(e NameEntry, portals []dmPortal, m *matrixClient, botMXID, ghost string, errOut *atomicString) NameEntry {
	var prof struct {
		Displayname string `json:"displayname"`
	}
	if err := m.get("/_matrix/client/v3/profile/"+url.PathEscape(ghost)+"/displayname", ghost, &prof); err != nil {
		errOut.setOnce("profile " + ghost + ": " + err.Error())
	} else if prof.Displayname != e.Expected {
		e.liveProfile = true
		e.Live = append(e.Live, "profile: "+prof.Displayname)
	}
	for _, p := range portals {
		if p.mxid == "" {
			continue
		}
		var name struct {
			Name string `json:"name"`
		}
		err := m.get("/_matrix/client/v3/rooms/"+url.PathEscape(p.mxid)+"/state/m.room.name/", botMXID, &name)
		if err == nil && name.Name != e.Expected {
			if e.liveRooms == nil {
				e.liveRooms = map[string]bool{}
			}
			e.liveRooms[p.mxid] = true
			e.Live = append(e.Live, "room name: "+name.Name)
		} else if err != nil && !isNotFound(err) {
			errOut.setOnce("room " + p.mxid + ": " + err.Error())
		}
		var member map[string]any
		err = m.get("/_matrix/client/v3/rooms/"+url.PathEscape(p.mxid)+"/state/m.room.member/"+url.PathEscape(ghost), botMXID, &member)
		if err != nil {
			if !isNotFound(err) {
				errOut.setOnce("member " + p.mxid + ": " + err.Error())
			}
			continue
		}
		if member["membership"] == "join" && member["displayname"] != e.Expected {
			if e.liveMembers == nil {
				e.liveMembers = map[string]map[string]any{}
			}
			cur, _ := member["displayname"].(string)
			member["displayname"] = e.Expected
			e.liveMembers[p.mxid] = member
			e.Live = append(e.Live, "name in chat: "+cur)
		}
	}
	return e
}

type httpError struct {
	status int
	msg    string
}

func (h *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", h.status, h.msg) }

func isNotFound(err error) bool {
	var h *httpError
	return errors.As(err, &h) && h.status == http.StatusNotFound
}

type atomicString struct {
	mu sync.Mutex
	s  string
}

func (a *atomicString) setOnce(s string) {
	a.mu.Lock()
	if a.s == "" {
		a.s = s
	}
	a.mu.Unlock()
}

func (a *atomicString) get() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.s
}

var numberName = regexp.MustCompile(`^[+\d\s∙()-]*( \(WA\))?$`)

func looksLikeNumber(s string) bool { return numberName.MatchString(strings.TrimSpace(s)) }

// ---- fix ----

type NamesFixConfig struct {
	Upstream         string `json:"upstream"`          // gateway base URL
	ASToken          string `json:"as_token"`          // bridge token (gateway swaps it)
	BotMXID          string `json:"bot_mxid"`          // sets DM room names, like the bridge does
	UsernameTemplate string `json:"username_template"` // e.g. wa01_{{.}}
}

type NamesFixResult struct {
	Contacts int         `json:"contacts"` // display names updated
	Rooms    int         `json:"rooms"`    // DM room names updated
	Failed   []NameError `json:"failed"`
}

type NameError struct {
	GhostID string `json:"id"`
	Error   string `json:"error"`
}

func runNamesInfo(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: names-info <bridge.db> [--live] (with --live: config JSON on stdin)")
	}
	rep, err := namesInfo(args[0])
	if err != nil {
		return err
	}
	if len(args) > 1 && args[1] == "--live" {
		var cfg NamesFixConfig
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<16)).Decode(&cfg); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		m, ghostMXID, err := cfg.client(&http.Client{Timeout: 30 * time.Second})
		if err != nil {
			return err
		}
		db, err := sql.Open("sqlite3", "file:"+args[0]+"?mode=ro&_busy_timeout=5000")
		if err != nil {
			return err
		}
		dms, err := loadDMPortals(db)
		db.Close()
		if err != nil {
			return err
		}
		liveCheck(rep, dms, m, cfg.BotMXID, ghostMXID)
	}
	return json.NewEncoder(os.Stdout).Encode(rep)
}

// client returns the Matrix client and the ghost id -> Matrix ID mapping.
func (cfg NamesFixConfig) client(hc *http.Client) (*matrixClient, func(string) string, error) {
	if cfg.Upstream == "" || cfg.ASToken == "" || cfg.BotMXID == "" || cfg.UsernameTemplate == "" {
		return nil, nil, errors.New("config: upstream, as_token, bot_mxid and username_template are required")
	}
	userTmpl, err := template.New("username").Parse(cfg.UsernameTemplate)
	if err != nil {
		return nil, nil, fmt.Errorf("username template: %w", err)
	}
	_, domain, found := strings.Cut(cfg.BotMXID, ":")
	if !found {
		return nil, nil, errors.New("bot_mxid has no server name")
	}
	m := &matrixClient{base: strings.TrimRight(cfg.Upstream, "/"), token: cfg.ASToken, hc: hc}
	return m, func(id string) string {
		var local strings.Builder
		_ = userTmpl.Execute(&local, id)
		return "@" + local.String() + ":" + domain
	}, nil
}

func runNamesFix(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: names-fix <bridge.db> (config JSON on stdin)")
	}
	var cfg NamesFixConfig
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<16)).Decode(&cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.Upstream == "" || cfg.ASToken == "" || cfg.BotMXID == "" || cfg.UsernameTemplate == "" {
		return errors.New("config: upstream, as_token, bot_mxid and username_template are required")
	}
	res, err := namesFix(args[0], cfg, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}

func namesFix(dbPath string, cfg NamesFixConfig, hc *http.Client) (*NamesFixResult, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("bridge database: %w", err)
	}
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rep, err := buildNamesReport(db)
	if err != nil {
		return nil, err
	}
	dms, err := loadDMPortals(db)
	if err != nil {
		return nil, err
	}
	m, ghostMXID, err := cfg.client(hc)
	if err != nil {
		return nil, err
	}
	liveCheck(rep, dms, m, cfg.BotMXID, ghostMXID)

	res := &NamesFixResult{Failed: []NameError{}}
	for _, e := range rep.Fixable {
		mxid := ghostMXID(e.GhostID)
		if e.Current != e.Expected || e.liveProfile {
			err = m.put("/_matrix/client/v3/profile/"+url.PathEscape(mxid)+"/displayname", mxid,
				map[string]string{"displayname": e.Expected})
			if err != nil {
				res.Failed = append(res.Failed, NameError{e.GhostID, err.Error()})
				continue
			}
			if _, err = db.Exec(`UPDATE ghost SET name=?, name_set=true WHERE id=?`, e.Expected, e.GhostID); err != nil {
				return res, fmt.Errorf("save contact: %w", err)
			}
			res.Contacts++
		}
		for room, content := range e.liveMembers {
			// The contact's name inside the chat (what clients show for an
			// unnamed DM). Sent as the contact, like a profile change does.
			err = m.put("/_matrix/client/v3/rooms/"+url.PathEscape(room)+"/state/m.room.member/"+url.PathEscape(mxid),
				mxid, content)
			if err != nil {
				res.Failed = append(res.Failed, NameError{e.GhostID, "name in chat: " + err.Error()})
			}
		}
		for _, p := range dms[e.GhostID] {
			if p.mxid == "" || (p.name == e.Expected && !e.liveRooms[p.mxid]) {
				continue
			}
			err = m.put("/_matrix/client/v3/rooms/"+url.PathEscape(p.mxid)+"/state/m.room.name/", cfg.BotMXID,
				map[string]string{"name": e.Expected})
			if err != nil {
				res.Failed = append(res.Failed, NameError{e.GhostID, "room: " + err.Error()})
				continue
			}
			if _, err = db.Exec(`UPDATE portal SET name=?, name_set=true WHERE bridge_id=? AND id=? AND receiver=?`,
				e.Expected, p.bridgeID, p.id, p.receiver); err != nil {
				return res, fmt.Errorf("save room: %w", err)
			}
			res.Rooms++
		}
	}
	return res, nil
}

type matrixClient struct {
	base, token string
	hc          *http.Client
}

// put sends an appservice request as userID, waiting out rate limits.
func (m *matrixClient) put(path, userID string, body any) error {
	raw, _ := json.Marshal(body)
	u := m.base + path + "?user_id=" + url.QueryEscape(userID)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+m.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := m.hc.Do(req)
		if err != nil {
			return err
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 5 {
			time.Sleep(namesRetryAfter(resp, msg))
			continue
		}
		if resp.StatusCode >= 300 {
			return &httpError{resp.StatusCode, strings.TrimSpace(string(msg))}
		}
		return nil
	}
}

// get reads JSON as userID, waiting out rate limits.
func (m *matrixClient) get(path, userID string, out any) error {
	u := m.base + path + "?user_id=" + url.QueryEscape(userID)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+m.token)
		resp, err := m.hc.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 5 {
			time.Sleep(namesRetryAfter(resp, body))
			continue
		}
		if resp.StatusCode >= 300 {
			return &httpError{resp.StatusCode, strings.TrimSpace(string(body))}
		}
		return json.Unmarshal(body, out)
	}
}

func namesRetryAfter(resp *http.Response, body []byte) time.Duration {
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		return min(time.Duration(s)*time.Second, time.Minute)
	}
	var j struct {
		RetryAfterMS int `json:"retry_after_ms"`
	}
	if json.Unmarshal(body, &j) == nil && j.RetryAfterMS > 0 {
		return min(time.Duration(j.RetryAfterMS)*time.Millisecond, time.Minute)
	}
	return 2 * time.Second
}
