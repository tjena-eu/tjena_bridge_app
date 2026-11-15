package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// proc is one running child process.
type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *proc) alive() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *proc) exitCode() int {
	if p == nil || p.cmd.ProcessState == nil {
		return -1
	}
	return p.cmd.ProcessState.ExitCode()
}

// stop sends SIGTERM and kills after grace.
func (p *proc) stop(grace time.Duration) {
	if !p.alive() {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(grace):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// Supervisor runs the relay, the unmodified bridge and the calls daemon,
// restarts them with backoff and stops for good when the server says this
// bridge is no longer the active one. Same logic as the phone app's
// BridgeService.kt.
type Supervisor struct {
	rt  *Runtime
	log *LogBuf

	mu       sync.Mutex
	running  bool
	stopCh   chan struct{}
	loopDone chan struct{}
	restart  chan struct{}

	state, detail, lastExit string
	since                   time.Time

	relay, bridge, wacall *proc
	wacallStartedAt       time.Time
	wacallBackoff         time.Duration

	haltReason  string
	dbFix       string
	dpResumeDue bool
	dpCheckedAt time.Time
	configGen   int
}

func newSupervisor(rt *Runtime, log *LogBuf) *Supervisor {
	return &Supervisor{rt: rt, log: log, state: "stopped", since: time.Now()}
}

type SupState struct {
	State    string    `json:"state"`
	Detail   string    `json:"detail,omitempty"`
	Since    time.Time `json:"since"`
	LastExit string    `json:"last_exit,omitempty"`
	Running  bool      `json:"running"`
}

func (s *Supervisor) snapshot() SupState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SupState{s.state, s.detail, s.since, s.lastExit, s.running}
}

func (s *Supervisor) setState(st, detail string) {
	s.mu.Lock()
	s.state, s.detail, s.since = st, detail, time.Now()
	s.mu.Unlock()
}

// Start starts the supervisor loop if it isn't running.
func (s *Supervisor) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.loopDone = make(chan struct{})
	s.restart = make(chan struct{}, 1)
	go s.loop(s.stopCh, s.restart, s.loopDone)
}

// Stop stops everything and waits until the processes have exited.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopCh)
	done := s.loopDone
	s.mu.Unlock()
	<-done
}

// Restart restarts the processes now (or starts them).
func (s *Supervisor) Restart() {
	s.mu.Lock()
	running := s.running
	ch := s.restart
	s.mu.Unlock()
	if !running {
		s.Start()
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *Supervisor) loop(stop <-chan struct{}, restart <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer func() {
		s.killAll()
		s.rt.deleteRunConfigs()
		s.mu.Lock()
		mine := s.stopCh == stop // not replaced by a newer Start
		if mine {
			s.running = false
		}
		if mine && s.state != "halted" && s.state != "not_set_up" && s.state != "error" {
			s.state, s.detail, s.since = "stopped", "", time.Now()
		}
		s.mu.Unlock()
	}()
	backoff := 2 * time.Second
	for {
		select {
		case <-stop:
			return
		default:
		}
		started := time.Now()
		if !s.rt.isSetUp() {
			s.setState("not_set_up", "The bridge is not set up")
			return
		}
		s.setState("starting", "")
		s.mu.Lock()
		s.haltReason, s.dbFix = "", ""
		s.mu.Unlock()
		fatal, restarted := "", false
		if err := s.rt.writeRunConfigs(); err != nil {
			s.setState("error", err.Error())
			s.log.add("manager", "start failed: "+err.Error())
		} else {
			s.relay = s.launch("relay", nil, s.rt.relayBin, "-config", s.rt.relayConfig())
			time.Sleep(500 * time.Millisecond)
			s.bridge = s.launch("bridge", nil, s.rt.bridgeBin, "-c", s.rt.bridgeConfig(), "-n")
			s.wacallBackoff = 5 * time.Second
			s.startWacall()
			// Both read their config at startup; don't leave secrets on disk.
			s.mu.Lock()
			s.configGen++
			gen := s.configGen
			s.mu.Unlock()
			go func() {
				time.Sleep(15 * time.Second)
				s.mu.Lock()
				same := s.configGen == gen
				s.mu.Unlock()
				if same {
					s.rt.deleteRunConfigs()
				}
			}()
			s.setState("running", "")
			fatal, restarted = s.waitForExit(stop, restart)
		}
		s.killAll()
		s.rt.deleteRunConfigs()
		select {
		case <-stop:
			return
		default:
		}
		if restarted {
			backoff = 2 * time.Second
			continue
		}
		s.mu.Lock()
		dpResume, fix := s.dpResumeDue, s.dbFix
		s.dpResumeDue, s.dbFix = false, ""
		s.mu.Unlock()
		if dpResume {
			out, err := s.rt.doublePuppet("resume")
			s.log.add("manager", fmt.Sprintf("fast import done, double puppeting back on: %s %v", strings.TrimSpace(out), errOrNil(err)))
			backoff = 2 * time.Second
			continue
		}
		if fix != "" && s.applyDBFix(fix) {
			backoff = 2 * time.Second
			continue
		}
		if fatal != "" {
			s.setState("halted", fatal)
			return
		}
		if time.Since(started) > 10*time.Minute {
			backoff = 2 * time.Second
		}
		s.setState("restarting", fmt.Sprintf("Restarting in %ds", int(backoff.Seconds())))
		select {
		case <-stop:
			return
		case <-restart:
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Minute)
	}
}

func errOrNil(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Supervisor) launch(name string, stdin []byte, bin string, args ...string) *proc {
	cmd := exec.Command(bin, args...)
	cmd.Dir = s.rt.dataDir
	cmd.Env = s.rt.env()
	w := &lineWriter{name: name, log: s.log, onLine: s.onLine}
	cmd.Stdout, cmd.Stderr = w, w
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	p := &proc{cmd: cmd, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		s.log.add("manager", fmt.Sprintf("%s start failed: %v", name, err))
		close(p.done)
		return p
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p
}

func (s *Supervisor) onLine(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Contains(line, "closed by another instance") {
		s.haltReason = "Another device is now running this bridge. Use Settings → Make this the active bridge to take it back."
	}
	// Fresh database but the bot already has cross-signing keys on the
	// server: let the bridge make new ones. The opposite case is fixed by
	// forgetting the stale local recovery key.
	if strings.Contains(line, "Server already has cross-signing keys, but no key in database") {
		s.dbFix = "overwrite"
	} else if strings.Contains(line, "No keys on server, but database already has recovery key") {
		s.dbFix = "delete"
	}
}

func (s *Supervisor) applyDBFix(fix string) bool {
	db, err := sql.Open("sqlite3", "file:"+s.rt.bridgeDB()+"?_busy_timeout=5000")
	if err != nil {
		s.log.add("manager", "recovery_key fix failed: "+err.Error())
		return false
	}
	defer db.Close()
	q := "DELETE FROM kv_store WHERE bridge_id = '' AND key = 'recovery_key'"
	if fix == "overwrite" {
		q = "INSERT INTO kv_store (bridge_id, key, value) VALUES ('', 'recovery_key', 'overwrite') " +
			"ON CONFLICT (bridge_id, key) DO UPDATE SET value = 'overwrite'"
	}
	if _, err = db.Exec(q); err != nil {
		s.log.add("manager", "recovery_key fix failed: "+err.Error())
		return false
	}
	s.log.add("manager", "applied recovery_key fix: "+fix)
	return true
}

func (s *Supervisor) killAll() {
	s.stopWacall()
	if s.bridge != nil {
		s.bridge.stop(8 * time.Second)
	}
	if s.relay != nil {
		s.relay.stop(3 * time.Second)
	}
}

// ---------------------------------------------------------------- calls daemon

func (s *Supervisor) startWacall() {
	if !s.rt.callsEnabled() || !exists(s.rt.wacallBin) {
		return
	}
	cfg, err := s.rt.wacallConfig()
	if err != nil {
		s.log.add("manager", "calls daemon config: "+err.Error())
		return
	}
	s.wacall = s.launch("calls", cfg, s.rt.wacallBin)
	s.wacallStartedAt = time.Now()
}

func (s *Supervisor) stopWacall() {
	if s.wacall != nil {
		s.wacall.stop(3 * time.Second)
	}
	s.wacall = nil
}

// superviseWacall keeps the calls daemon in line with the setting. Its
// failures never stop the bridge.
func (s *Supervisor) superviseWacall() {
	on := s.rt.callsEnabled()
	p := s.wacall
	if !on {
		if p != nil {
			s.stopWacall()
		}
		return
	}
	if p.alive() {
		return
	}
	now := time.Now()
	if p != nil {
		s.log.add("manager", fmt.Sprintf("calls daemon exited (%d)", p.exitCode()))
		s.wacall = nil
		if now.Sub(s.wacallStartedAt) > 10*time.Minute {
			s.wacallBackoff = 5 * time.Second
		}
		s.wacallStartedAt = now
		return
	}
	if now.Sub(s.wacallStartedAt) < s.wacallBackoff {
		return
	}
	s.startWacall()
	s.wacallBackoff = min(s.wacallBackoff*2, 5*time.Minute)
}

// fastImportDone: during a fast history import, true once double puppeting
// should come back.
func (s *Supervisor) fastImportDone() bool {
	if !exists(s.rt.dpStash()) || time.Since(s.dpCheckedAt) < time.Minute {
		return false
	}
	s.dpCheckedAt = time.Now()
	out, err := s.rt.doublePuppet("status")
	if err != nil {
		s.log.add("manager", "double puppet status: "+err.Error())
		return false
	}
	var st struct {
		ImportDone bool `json:"import_done"`
	}
	return json.Unmarshal([]byte(out), &st) == nil && st.ImportDone
}

// waitForExit waits until a process exits. fatal: reason not to restart.
func (s *Supervisor) waitForExit(stop, restart <-chan struct{}) (fatal string, restarted bool) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		if boolean(s.rt.relayStatus(), "revoked") {
			return "The server no longer accepts this bridge: it was removed, or another device took it over. " +
				"Use Settings → Make this the active bridge, or set it up again.", false
		}
		if !s.relay.alive() || !s.bridge.alive() {
			which := fmt.Sprintf("relay exited (%d)", s.relay.exitCode())
			if !s.bridge.alive() {
				which = fmt.Sprintf("bridge exited (%d)", s.bridge.exitCode())
			}
			s.log.add("manager", which)
			s.mu.Lock()
			s.lastExit = which + " at " + time.Now().Format(time.RFC3339)
			reason := s.haltReason
			s.mu.Unlock()
			return reason, false
		}
		s.superviseWacall()
		if s.fastImportDone() {
			s.mu.Lock()
			s.dpResumeDue = true
			s.mu.Unlock()
			return "", false
		}
		select {
		case <-stop:
			return "", false
		case <-restart:
			return "", true
		case <-t.C:
		}
	}
}
