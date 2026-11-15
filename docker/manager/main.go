// tjena-bridge: the tjena personal WhatsApp bridge in a container.
//
// Runs the same programs as the tjena Bridge phone app, with a web UI for
// setup and settings instead of the app's screens:
//
//	mautrix-whatsapp (unmodified) ──ws──► bridge-relay (127.0.0.1) ──wss──► gateway (wap.tjena.eu)
//	wacall (WhatsApp calls, optional) ──────────────────────────────wss──► gateway
//	web UI (this program, :8080) ──► setup, WhatsApp linking, settings, backup, move
//
// Environment:
//
//	TJENA_DATA       data directory (default /data): secrets.json, bridge/ (databases, logs)
//	TJENA_BIN        directory with mautrix-whatsapp, bridge-relay, wacall (default /usr/local/bin)
//	TJENA_RUN        directory for generated configs (default /tmp/tjena-run; best on tmpfs)
//	WEBUI_LISTEN     web UI address (default :8080)
//	WEBUI_PASSWORD   web UI password; if unset, it is chosen on the first visit
//	WEBUI_ALLOW      who may open the web UI: private (default: home networks), any, or CIDRs
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

var version = "0.4.0"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	listen := env("WEBUI_LISTEN", ":8080")
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(listen))
	}
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("tjena-bridge", version)
		return
	}
	dataRoot := env("TJENA_DATA", "/data")
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "data directory:", err)
		os.Exit(1)
	}
	allow, err := parseAllow(os.Getenv("WEBUI_ALLOW"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log := newLogBuf()
	rt := newRuntime(dataRoot, env("TJENA_BIN", "/usr/local/bin"), env("TJENA_RUN", filepath.Join(os.TempDir(), "tjena-run")))
	prefs := loadPrefs(filepath.Join(dataRoot, "manager.json"))
	m := &Manager{rt: rt, log: log, prefs: prefs, sup: newSupervisor(rt, log), auth: newAuth(os.Getenv("WEBUI_PASSWORD"), prefs)}

	if enabled, _, _ := prefs.Get(); enabled && rt.isSetUp() {
		m.sup.Start()
	}
	if !m.auth.configured() {
		log.add("manager", "Web UI not protected yet: open http://<this server's IP>:8080 from your home network and choose the sign-in, or set WEBUI_PASSWORD.")
	}
	srv := &http.Server{Addr: listen, Handler: allow.wrap(m.routes()), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		log.add("manager", fmt.Sprintf("tjena-bridge %s, web UI on %s", version, listen))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.add("manager", "web UI: "+err.Error())
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.add("manager", "shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	m.sup.Stop() // keeps "enabled": starts again with the container
}

func healthcheck(listen string) int {
	addr := listen
	if addr != "" && addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return 1
	}
	return 0
}

// Manager ties the parts together for the web UI.
type Manager struct {
	rt    *Runtime
	sup   *Supervisor
	log   *LogBuf
	prefs *Prefs
	auth  *Auth

	mu      sync.Mutex
	job     *Job
	jobSeq  int
	pending *pendingSetup // signed in to tjena, not set up yet

	opMu sync.Mutex // one operation at a time that stops the bridge
}

type pendingSetup struct {
	session  *Session
	gateway  string
	existing J
}

// Job: a long task the web UI follows (setup, moves).
type Job struct {
	ID       int     `json:"id"`
	Kind     string  `json:"kind"` // setup, move-in, move-out
	Phase    string  `json:"phase"`
	Progress string  `json:"progress"`
	Fraction float64 `json:"fraction"` // -1 = unknown
	Error    string  `json:"error,omitempty"`
	Done     bool    `json:"done"`
	URI      string  `json:"uri,omitempty"` // move code (move-out)
	QR       string  `json:"qr,omitempty"`  // move code as PNG data URL
	Sent     int64   `json:"sent"`
	Size     int64   `json:"size"`
	// Moving without a code: the number both devices show, and whether the
	// user confirmed it here.
	Mode      string `json:"mode,omitempty"`
	SAS       string `json:"sas,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`

	cancel  context.CancelFunc
	confirm chan bool
}

func (m *Manager) jobSet(fn func(*Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil {
		fn(m.job)
	}
}

func (m *Manager) jobGet() Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return Job{}
	}
	return *m.job
}

// startJob runs fn in the background unless another job is running.
func (m *Manager) startJob(kind string, init func(*Job), fn func(ctx context.Context) error) error {
	m.mu.Lock()
	if m.job != nil && !m.job.Done {
		m.mu.Unlock()
		return errors.New("Another task is still running.")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.jobSeq++
	j := &Job{ID: m.jobSeq, Kind: kind, Phase: "running", Fraction: -1, cancel: cancel, confirm: make(chan bool, 1)}
	if init != nil {
		init(j)
	}
	m.job = j
	m.mu.Unlock()
	go func() {
		defer cancel()
		err := fn(ctx)
		m.jobSet(func(j *Job) {
			if j.Done {
				return // fn set the outcome itself
			}
			j.Done = true
			if err != nil {
				j.Phase, j.Error = "failed", err.Error()
			} else {
				j.Phase = "done"
			}
		})
	}()
	return nil
}

// confirmJob passes the user's answer (numbers match or not) to the running job.
func (m *Manager) confirmJob(match bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil || m.job.Done || m.job.Phase != "compare" || m.job.Confirmed {
		return false
	}
	select {
	case m.job.confirm <- match:
	default:
	}
	return true
}

// jobConfirm: the confirm channel of the job just started (called from its fn).
func (m *Manager) jobConfirm() <-chan bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	return m.job.confirm
}

func (m *Manager) startBridge() {
	_ = m.prefs.Update(func(p *Prefs) { p.Enabled = true })
	m.sup.Start()
}

func (m *Manager) stopBridge() {
	_ = m.prefs.Update(func(p *Prefs) { p.Enabled = false })
	m.sup.Stop()
}

// withStopped runs fn with the bridge stopped and starts it again if it was on.
func (m *Manager) withStopped(fn func() error) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	enabled, _, _ := m.prefs.Get()
	m.sup.Stop()
	err := fn()
	if enabled && m.rt.isSetUp() {
		m.sup.Start()
	}
	return err
}

func (m *Manager) wipeLocal() {
	_ = m.prefs.Update(func(p *Prefs) { p.Enabled = false })
	m.sup.Stop()
	m.rt.wipe()
	m.sup.setState("not_set_up", "")
}

var mxidRe = regexp.MustCompile(`^@[a-z0-9._=/+-]+:[a-zA-Z0-9.-]+(:\d+)?$`)

func (m *Manager) saveSetup(bundle J, session *Session, gateway string) error {
	owner := str(bundle, "owner")
	if !mxidRe.MatchString(owner) {
		return errors.New("invalid owner")
	}
	if session.UserID != owner {
		return errors.New("session user and bridge owner differ")
	}
	if err := m.rt.secrets.Update(func(s J) error {
		s["bundle"] = bundle
		s["session"] = session.J()
		s["gateway_url"] = trimURL(gateway)
		return nil
	}); err != nil {
		return err
	}
	return m.rt.ensureLocalSecrets()
}
