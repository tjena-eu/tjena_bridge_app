package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed web
var webFS embed.FS

func writeJ(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) { writeJ(w, status, J{"error": msg}) }

func ok(w http.ResponseWriter) { writeJ(w, 200, J{"ok": true}) }

func readJ(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.UseNumber()
	if err := d.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return errors.New("bad request body")
	}
	return nil
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

func (m *Manager) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })

	open := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, m.guard(h, false)) }
	authd := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, m.guard(h, true)) }

	open("GET /api/auth", m.hAuthState)
	open("POST /api/auth/setup", m.hAuthSetup)
	open("POST /api/auth/login", m.hAuthLogin)
	open("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) { m.auth.logout(w, r); ok(w) })
	authd("POST /api/auth/password", m.hAuthPassword)

	authd("GET /api/state", m.hState)
	authd("POST /api/bridge/{action}", m.hBridge)

	authd("POST /api/setup/login", m.hSetupLogin)
	authd("POST /api/setup/fresh", m.hSetupFresh)
	authd("POST /api/setup/move", m.hSetupMove)
	authd("POST /api/setup/cancel", m.hSetupCancel)
	authd("POST /api/backup/import", m.hBackupImport)
	authd("POST /api/backup/export", m.hBackupExport)

	authd("GET /api/job", func(w http.ResponseWriter, r *http.Request) { writeJ(w, 200, m.jobGet()) })
	authd("POST /api/job/cancel", m.hJobCancel)
	authd("POST /api/job/confirm", m.hJobConfirm)
	authd("POST /api/move/out", m.hMoveOut)
	authd("POST /api/move/out/resolve", m.hMoveResolve)

	authd("POST /api/relay", m.hLocal("relay_port"))
	authd("POST /api/wacall", m.hLocal("wacall_port"))
	authd("POST /api/qr", m.hQR)

	authd("GET /api/options", m.hGetOptions)
	authd("POST /api/options", m.hSetOptions)
	authd("POST /api/extra-users", m.hExtraUsers)
	authd("POST /api/gateway", m.hGateway)
	authd("POST /api/calls", m.hCalls)
	authd("POST /api/double-puppet", m.hDoublePuppet)
	authd("POST /api/repair", m.hRepair)
	authd("POST /api/remove", m.hRemove)

	authd("GET /api/tools/{tool}", m.hToolInfo)
	authd("POST /api/tools/{tool}", m.hToolRun)
	authd("GET /api/logs", func(w http.ResponseWriter, r *http.Request) { writeJ(w, 200, J{"text": m.log.text()}) })
	authd("GET /api/logs/export", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="tjena-bridge-log.txt"`)
		exportLogs(w, m.rt, m.sup, m.log)
	})
	return securityHeaders(mux)
}

// guard: every API call needs the X-Tjena-UI header (CSRF), most need a session.
func (m *Manager) guard(h http.HandlerFunc, needAuth bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tjena-UI") != "1" {
			fail(w, http.StatusForbidden, "missing X-Tjena-UI header")
			return
		}
		if needAuth && !m.auth.valid(r) {
			fail(w, http.StatusUnauthorized, "Please sign in.")
			return
		}
		h(w, r)
	}
}

// ---------------------------------------------------------------- web UI sign-in

func (m *Manager) hAuthState(w http.ResponseWriter, r *http.Request) {
	writeJ(w, 200, J{
		"configured": m.auth.configured(), "signed_in": m.auth.valid(r),
		"env_password": m.auth.envPass != "",
	})
}

func (m *Manager) hAuthSetup(w http.ResponseWriter, r *http.Request) {
	var b struct{ Password string }
	_ = readJ(r, &b)
	if m.auth.configured() {
		fail(w, http.StatusConflict, "A password is already set.")
		return
	}
	if !localHost(r.Host) {
		fail(w, http.StatusForbidden, "Open this page by its IP address (for example http://192.168.1.20:8080) to choose the password, or set WEBUI_PASSWORD.")
		return
	}
	if err := m.auth.setPassword(b.Password); err != nil {
		fail(w, 400, err.Error())
		return
	}
	m.auth.newSession(w, r)
	ok(w)
}

func (m *Manager) hAuthLogin(w http.ResponseWriter, r *http.Request) {
	var b struct{ Password string }
	_ = readJ(r, &b)
	if !m.auth.configured() {
		fail(w, http.StatusConflict, "Choose a password first.")
		return
	}
	if !m.auth.login(w, r, b.Password) {
		fail(w, http.StatusUnauthorized, "Wrong password.")
		return
	}
	ok(w)
}

func (m *Manager) hAuthPassword(w http.ResponseWriter, r *http.Request) {
	var b struct{ Old, New string }
	_ = readJ(r, &b)
	if !m.auth.check(b.Old) {
		time.Sleep(time.Second)
		fail(w, 400, "The current password is wrong.")
		return
	}
	if err := m.auth.setPassword(b.New); err != nil {
		fail(w, 400, err.Error())
		return
	}
	m.auth.newSession(w, r)
	ok(w)
}

// ---------------------------------------------------------------- state

func (m *Manager) hState(w http.ResponseWriter, r *http.Request) {
	st := m.sup.snapshot()
	enabled, _, _ := m.prefs.Get()
	o := J{
		"set_up": m.rt.isSetUp(), "enabled": enabled, "calls_enabled": m.rt.callsEnabled(),
		"service_state": st.State, "service_detail": st.Detail, "service_since": st.Since,
		"last_exit": st.LastExit, "relay": m.rt.relayStatus(),
		"app_version": "Docker " + version, "bridge_version": m.rt.bridgeVersion(),
		"binaries_present": exists(m.rt.bridgeBin) && exists(m.rt.relayBin),
		"calls_available":  exists(m.rt.wacallBin),
	}
	if s, err := m.rt.secrets.Load(); err != nil {
		o["secrets_error"] = err.Error()
	} else {
		if b := get(s, "bundle"); b != nil {
			o["owner"], o["slot"], o["bot_mxid"], o["server_name"] = str(b, "owner"), str(b, "slot"), str(b, "bot_mxid"), str(b, "server_name")
		}
		o["gateway_url"] = gatewayOf(s)
		o["homeserver"] = str(get(s, "session"), "homeserver")
		extra := get(s, "settings")["extra_users"]
		if extra == nil {
			extra = []any{}
		}
		o["extra_users"] = extra
	}
	m.mu.Lock()
	if p := m.pending; p != nil {
		o["pending"] = J{"user_id": p.session.UserID, "gateway": p.gateway, "existing": p.existing}
	}
	m.mu.Unlock()
	writeJ(w, 200, o)
}

func (m *Manager) hBridge(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("action") {
	case "start":
		if !m.rt.isSetUp() {
			fail(w, 409, "The bridge is not set up.")
			return
		}
		m.startBridge()
	case "stop":
		m.stopBridge()
	case "restart":
		if !m.rt.isSetUp() {
			fail(w, 409, "The bridge is not set up.")
			return
		}
		_ = m.prefs.Update(func(p *Prefs) { p.Enabled = true })
		m.sup.Restart()
	default:
		fail(w, 404, "unknown action")
		return
	}
	ok(w)
}

// ---------------------------------------------------------------- setup

func apiErr(err error) (int, string) {
	var e *APIError
	if errors.As(err, &e) {
		switch e.Status {
		case 503:
			return 503, "The server has no free bridge slots right now. Ask the tjena admin to add more."
		case 429:
			return 429, "Too many attempts. Please try again in an hour."
		case 403, 401:
			return 400, e.Msg
		case 0:
			return 502, "Can't reach the server: " + e.Msg
		}
		return 502, e.Msg
	}
	return 500, err.Error()
}

func (m *Manager) hSetupLogin(w http.ResponseWriter, r *http.Request) {
	var b struct{ Homeserver, Gateway, User, Password, Link string }
	_ = readJ(r, &b)
	if m.rt.isSetUp() {
		fail(w, 409, "The bridge is already set up.")
		return
	}
	hs, gw, token := trimURL(b.Homeserver), trimURL(b.Gateway), ""
	if hs == "" {
		hs = defaultHomeserver
	}
	if gw == "" {
		gw = defaultGateway
	}
	if strings.TrimSpace(b.Link) != "" {
		l := parsePairingLink(b.Link)
		if l == nil {
			fail(w, 400, "This is not a tjena bridge setup link.")
			return
		}
		if l.Token == "" {
			fail(w, 400, "This link has no sign-in token. Sign in with your password instead.")
			return
		}
		hs, gw, token = l.Homeserver, l.Gateway, l.Token
	} else if strings.TrimSpace(b.User) == "" || b.Password == "" {
		fail(w, 400, "Enter your tjena username and password.")
		return
	}
	if !isHTTPURL(hs) || !isHTTPURL(gw) {
		fail(w, 400, "Homeserver and gateway must be http(s) URLs.")
		return
	}
	sess, err := matrixLogin(hs, token, strings.TrimSpace(b.User), b.Password)
	if err != nil {
		code, msg := apiErr(err)
		fail(w, code, "Sign-in failed: "+msg)
		return
	}
	st, err := Provisioner{gw, sess.AccessToken}.status()
	if err != nil {
		matrixLogout(sess)
		code, msg := apiErr(err)
		fail(w, code, "Can't reach the bridge gateway: "+msg)
		return
	}
	var existing J
	if st != nil && str(st, "state") == "assigned" {
		existing = st
	}
	m.mu.Lock()
	if old := m.pending; old != nil {
		go matrixLogout(old.session)
	}
	m.pending = &pendingSetup{session: sess, gateway: gw, existing: existing}
	m.mu.Unlock()
	writeJ(w, 200, J{"user_id": sess.UserID, "existing": existing})
}

func (m *Manager) takePending() *pendingSetup {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pending
	m.pending = nil
	return p
}

func (m *Manager) hSetupCancel(w http.ResponseWriter, r *http.Request) {
	if p := m.takePending(); p != nil {
		matrixLogout(p.session)
	}
	ok(w)
}

// hSetupFresh: claim the slot (new or the existing one, empty) and start.
func (m *Manager) hSetupFresh(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	p := m.pending
	m.mu.Unlock()
	if p == nil {
		fail(w, 409, "Sign in first.")
		return
	}
	err := m.startJob("setup", func(j *Job) { j.Progress = "Requesting your bridge…" }, func(ctx context.Context) error {
		prov := Provisioner{p.gateway, p.session.AccessToken}
		bundle, err := prov.claim()
		if err != nil {
			_, msg := apiErr(err)
			return errors.New(msg)
		}
		// Usable once Synapse has loaded the slot (normally right away).
		for !boolean(bundle, "active") {
			m.jobSet(func(j *Job) {
				j.Progress = "Waiting for server activation… This bridge slot becomes usable after the server's next planned restart. You can leave this page open."
			})
			select {
			case <-ctx.Done():
				return errors.New("Cancelled.")
			case <-time.After(15 * time.Second):
			}
			if st, err := prov.status(); err == nil && boolean(st, "active") {
				bundle["active"] = true
			}
		}
		m.jobSet(func(j *Job) { j.Progress = "Configuring the bridge…" })
		if err = m.saveSetup(bundle, p.session, p.gateway); err != nil {
			return err
		}
		m.takePending()
		m.startBridge()
		return nil
	})
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	ok(w)
}

// hSetupMove: {"code": "tjena-bridge://move?…"}, or {"compare": true} to
// move without a code (both devices show a number to compare).
func (m *Manager) hSetupMove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code    string
		Compare bool
	}
	_ = readJ(r, &b)
	m.mu.Lock()
	p := m.pending
	m.mu.Unlock()
	if p == nil {
		fail(w, 409, "Sign in first.")
		return
	}
	code := MoveCode{Gateway: p.gateway}
	if !b.Compare {
		var err error
		if code, err = parseMoveCode(b.Code); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	err := m.startJob("move-in", nil, func(ctx context.Context) error {
		m.opMu.Lock()
		defer m.opMu.Unlock()
		if err := m.moveIn(ctx, code, p.session, m.jobConfirm()); err != nil {
			if !m.rt.isSetUp() {
				// Nothing was set up: drop the unused session.
				m.takePending()
				matrixLogout(p.session)
			}
			return err
		}
		m.takePending()
		return nil
	})
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	ok(w)
}

func (m *Manager) hBackupImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<30)
	f, _, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "No backup file.")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		fail(w, 400, "Could not read the file.")
		return
	}
	pass := r.FormValue("passphrase")
	m.opMu.Lock()
	m.sup.Stop()
	err = importBackup(m.rt, data, pass)
	m.opMu.Unlock()
	if err != nil {
		if m.rt.isSetUp() {
			m.startBridge() // the old setup is still in place
		}
		fail(w, 400, err.Error())
		return
	}
	if p := m.takePending(); p != nil {
		go matrixLogout(p.session) // the backup brings its own session
	}
	m.startBridge()
	ok(w)
}

func (m *Manager) hBackupExport(w http.ResponseWriter, r *http.Request) {
	var b struct{ Passphrase string }
	_ = readJ(r, &b)
	if !m.rt.isSetUp() {
		fail(w, 409, "The bridge is not set up.")
		return
	}
	if len([]rune(b.Passphrase)) < 8 {
		fail(w, 400, "Passphrase must have at least 8 characters")
		return
	}
	var data []byte
	err := m.withStopped(func() (err error) { data, err = exportBackup(m.rt, b.Passphrase); return })
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="tjena-bridge-backup-%s.tjbk"`, time.Now().Format("2006-01-02")))
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	_, _ = w.Write(data)
}

// ---------------------------------------------------------------- move out

func qrDataURL(data string) (string, error) {
	png, err := qrcode.Encode(data, qrcode.Medium, 360)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func (m *Manager) hMoveOut(w http.ResponseWriter, r *http.Request) {
	s, err := m.rt.secrets.Load()
	if err != nil || get(s, "bundle") == nil {
		fail(w, 409, "The bridge is not set up.")
		return
	}
	code := newMoveCode(gatewayOf(s))
	token := str(get(s, "bundle"), "as_token")
	qr, err := qrDataURL(code.URI())
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	err = m.startJob("move-out", func(j *Job) {
		j.Phase, j.URI, j.QR = "waiting", code.URI(), qr
	}, func(ctx context.Context) error {
		m.opMu.Lock()
		defer m.opMu.Unlock()
		m.moveOut(ctx, code, token, m.jobConfirm())
		return nil
	})
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJ(w, 200, m.jobGet())
}

func (m *Manager) hJobCancel(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	j := m.job
	m.mu.Unlock()
	if j != nil && !j.Done && j.cancel != nil {
		j.cancel()
	}
	ok(w)
}

// hJobConfirm: {"match": true|false} for the number shown on both devices.
func (m *Manager) hJobConfirm(w http.ResponseWriter, r *http.Request) {
	var b struct{ Match bool }
	_ = readJ(r, &b)
	if !m.confirmJob(b.Match) {
		fail(w, 409, "Nothing to confirm.")
		return
	}
	ok(w)
}

// hMoveResolve: after a move whose end wasn't confirmed, the user decides.
func (m *Manager) hMoveResolve(w http.ResponseWriter, r *http.Request) {
	var b struct{ Remove bool }
	_ = readJ(r, &b)
	j := m.jobGet()
	if j.Kind != "move-out" || j.Phase != "unsure" {
		fail(w, 409, "Nothing to decide.")
		return
	}
	if b.Remove {
		m.wipeLocal()
		m.jobSet(func(j *Job) { j.Phase = "done"; j.Progress = "The bridge data was removed here." })
	} else {
		m.startBridge()
		m.jobSet(func(j *Job) { j.Phase = "failed"; j.Error = "Kept the bridge here." })
	}
	ok(w)
}

// ---------------------------------------------------------------- local APIs

// hLocal proxies a call to the relay's or calls daemon's local API.
func (m *Manager) hLocal(portKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Method    string          `json:"method"`
			Path      string          `json:"path"`
			Body      json.RawMessage `json:"body"`
			TimeoutMs int             `json:"timeout_ms"`
		}
		if err := readJ(r, &b); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if !strings.HasPrefix(b.Path, "/") || strings.Contains(b.Path, "..") ||
			(portKey == "relay_port" && !strings.HasPrefix(b.Path, "/relay/")) {
			fail(w, 400, "bad path")
			return
		}
		if b.Method != "GET" && b.Method != "POST" && b.Method != "DELETE" {
			fail(w, 400, "bad method")
			return
		}
		timeout := time.Duration(b.TimeoutMs) * time.Millisecond
		if timeout <= 0 || timeout > 10*time.Minute {
			timeout = 15 * time.Second
		}
		var body []byte
		if len(b.Body) > 0 && !bytes.Equal(b.Body, []byte("null")) {
			body = b.Body
		}
		status, data := m.rt.localCall(portKey, b.Method, b.Path, body, timeout)
		writeJ(w, 200, J{"status": status, "body": string(data)})
	}
}

func (m *Manager) hQR(w http.ResponseWriter, r *http.Request) {
	var b struct{ Data string }
	_ = readJ(r, &b)
	if b.Data == "" || len(b.Data) > 2000 {
		fail(w, 400, "bad data")
		return
	}
	u, err := qrDataURL(b.Data)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJ(w, 200, J{"png": u})
}

// ---------------------------------------------------------------- settings

func (m *Manager) restartIfOn() {
	if enabled, _, _ := m.prefs.Get(); enabled {
		m.sup.Restart()
	}
}

func (m *Manager) hGetOptions(w http.ResponseWriter, r *http.Request) {
	s, err := m.rt.secrets.Load()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	o := get(get(s, "settings"), "options")
	if o == nil {
		o = J{}
	}
	writeJ(w, 200, o)
}

func (m *Manager) hSetOptions(w http.ResponseWriter, r *http.Request) {
	var o J
	if err := readJ(r, &o); err != nil || o == nil {
		fail(w, 400, "bad options")
		return
	}
	if sb := str(o, "status_broadcast"); sb != "" && sb != "off" && sb != "muted" && sb != "on" {
		fail(w, 400, "invalid status_broadcast")
		return
	}
	if err := m.rt.secrets.Update(func(s J) error { obj(s, "settings")["options"] = o; return nil }); err != nil {
		fail(w, 500, err.Error())
		return
	}
	m.restartIfOn()
	ok(w)
}

func (m *Manager) hExtraUsers(w http.ResponseWriter, r *http.Request) {
	var b struct{ Users []string }
	_ = readJ(r, &b)
	s, _ := m.rt.secrets.Load()
	owner := str(get(s, "bundle"), "owner")
	seen, users := map[string]bool{}, []any{}
	for _, u := range b.Users {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		if !mxidRe.MatchString(u) {
			fail(w, 400, fmt.Sprintf("%q is not a full Matrix ID. Wildcards and whole domains are not allowed.", u))
			return
		}
		if u == owner {
			fail(w, 400, "The owner is always admin.")
			return
		}
		seen[u] = true
		users = append(users, u)
	}
	if err := m.rt.secrets.Update(func(s J) error { obj(s, "settings")["extra_users"] = users; return nil }); err != nil {
		fail(w, 500, err.Error())
		return
	}
	m.restartIfOn()
	ok(w)
}

func (m *Manager) hGateway(w http.ResponseWriter, r *http.Request) {
	var b struct{ URL string }
	_ = readJ(r, &b)
	u := trimURL(b.URL)
	if !isHTTPURL(u) {
		fail(w, 400, "URL must start with https://")
		return
	}
	if err := m.rt.secrets.Update(func(s J) error { s["gateway_url"] = u; return nil }); err != nil {
		fail(w, 500, err.Error())
		return
	}
	m.restartIfOn()
	ok(w)
}

func (m *Manager) hCalls(w http.ResponseWriter, r *http.Request) {
	var b struct{ On bool }
	_ = readJ(r, &b)
	// The supervisor starts or stops the calls daemon within a few seconds.
	if err := m.rt.secrets.Update(func(s J) error { obj(s, "settings")["calls_enabled"] = b.On; return nil }); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w)
}

func (m *Manager) hDoublePuppet(w http.ResponseWriter, r *http.Request) {
	var b struct{ Action string }
	_ = readJ(r, &b)
	var out string
	var err error
	switch b.Action {
	case "status":
		out, err = m.rt.doublePuppet("status")
	case "pause", "resume":
		err = m.withStopped(func() (e error) { out, e = m.rt.doublePuppet(b.Action); return })
	default:
		fail(w, 400, "bad action")
		return
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeRaw(w, out)
}

func writeRaw(w http.ResponseWriter, out string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(out))
}

// hRepair: fresh bridge tokens for the same slot (after another device took over).
func (m *Manager) hRepair(w http.ResponseWriter, r *http.Request) {
	s, _ := m.rt.secrets.Load()
	sess := sessionFrom(get(s, "session"))
	if sess == nil {
		fail(w, 409, "No tjena session stored.")
		return
	}
	bundle, err := Provisioner{gatewayOf(s), sess.AccessToken}.claim()
	if err != nil {
		code, msg := apiErr(err)
		fail(w, code, msg)
		return
	}
	err = m.rt.secrets.Update(func(s J) error {
		if str(get(s, "bundle"), "owner") != str(bundle, "owner") {
			return errors.New("owner changed")
		}
		s["bundle"] = bundle
		return nil
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	_ = m.prefs.Update(func(p *Prefs) { p.Enabled = true })
	m.sup.Restart()
	ok(w)
}

// hRemove: log WhatsApp out, remove the bridge from the server, sign out of
// tjena and delete everything here.
func (m *Manager) hRemove(w http.ResponseWriter, r *http.Request) {
	if status, data := m.rt.localCall("relay_port", "GET", "/relay/provision/v3/whoami", nil, 15*time.Second); status == 200 {
		var who struct {
			Logins []struct {
				ID string `json:"id"`
			} `json:"logins"`
		}
		if json.Unmarshal(data, &who) == nil {
			for _, l := range who.Logins {
				m.rt.localCall("relay_port", "POST", "/relay/provision/v3/logout/"+l.ID, []byte("{}"), 30*time.Second)
			}
		}
	}
	if status, data := m.rt.localCall("wacall_port", "GET", "/status", nil, 5*time.Second); status == 200 {
		var st struct {
			WhatsApp struct {
				Accounts []struct {
					Phone string `json:"phone"`
				} `json:"accounts"`
			} `json:"whatsapp"`
		}
		if json.Unmarshal(data, &st) == nil {
			for _, a := range st.WhatsApp.Accounts {
				body, _ := json.Marshal(J{"phone": a.Phone})
				m.rt.localCall("wacall_port", "POST", "/unlink", body, 20*time.Second)
			}
		}
	}
	s, _ := m.rt.secrets.Load()
	if sess := sessionFrom(get(s, "session")); sess != nil {
		_ = Provisioner{gatewayOf(s), sess.AccessToken}.revoke()
		matrixLogout(sess)
	}
	m.opMu.Lock()
	m.wipeLocal()
	m.opMu.Unlock()
	ok(w)
}

// ---------------------------------------------------------------- tools

func (m *Manager) hToolInfo(w http.ResponseWriter, r *http.Request) {
	var out string
	var err error
	switch r.PathValue("tool") {
	case "security":
		out, err = m.rt.securityInfo()
	case "history":
		out, err = m.rt.historyInfo()
	case "names":
		out, err = m.rt.namesInfo()
	default:
		fail(w, 404, "unknown tool")
		return
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeRaw(w, out)
}

func (m *Manager) hToolRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Mode  string   `json:"mode"`
		Login string   `json:"login"`
		Chats []string `json:"chats"`
	}
	_ = readJ(r, &b)
	var out string
	var err error
	switch r.PathValue("tool") {
	case "history-catchup":
		if b.Mode != "" && b.Mode != "missing" && b.Mode != "held" && b.Mode != "both" {
			fail(w, 400, "invalid mode")
			return
		}
		err = m.withStopped(func() (e error) { out, e = m.rt.historyCatchup(b.Mode, b.Login, b.Chats); return })
	case "history-stop":
		err = m.withStopped(func() (e error) { out, e = m.rt.historyStop(); return })
	case "names-fix":
		err = m.withStopped(func() (e error) { out, e = m.rt.namesFix(); return })
	default:
		fail(w, 404, "unknown tool")
		return
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeRaw(w, out)
}
