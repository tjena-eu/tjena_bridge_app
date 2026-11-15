package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultGateway = "https://wap.tjena.eu"

// Runtime: paths, config generation and calls into the relay and the calls
// daemon. Same steps as the phone app's BridgeRuntime.kt.
type Runtime struct {
	bridgeBin, relayBin, wacallBin string

	dataDir string // bridge.db, wacall.db, logs (in the volume)
	runDir  string // generated configs, deleted after start (tmpfs)
	logDir  string

	secrets *Store

	versionOnce sync.Once
	version     string
}

func newRuntime(dataRoot, binDir, runDir string) *Runtime {
	rt := &Runtime{
		bridgeBin: filepath.Join(binDir, "mautrix-whatsapp"),
		relayBin:  filepath.Join(binDir, "bridge-relay"),
		wacallBin: filepath.Join(binDir, "wacall"),
		dataDir:   filepath.Join(dataRoot, "bridge"),
		runDir:    runDir,
		secrets:   &Store{path: filepath.Join(dataRoot, "secrets.json")},
	}
	rt.logDir = filepath.Join(rt.dataDir, "logs")
	for _, d := range []string{rt.dataDir, rt.runDir, rt.logDir} {
		_ = os.MkdirAll(d, 0o700)
	}
	return rt
}

func (rt *Runtime) path(name string) string { return filepath.Join(rt.dataDir, name) }

func (rt *Runtime) bridgeConfig() string    { return filepath.Join(rt.runDir, "config.yaml") }
func (rt *Runtime) relayConfig() string     { return filepath.Join(rt.runDir, "relay.json") }
func (rt *Runtime) relayStatusFile() string { return filepath.Join(rt.runDir, "relay-status.json") }
func (rt *Runtime) bridgeDB() string        { return rt.path("bridge.db") }
func (rt *Runtime) observedKeys() string    { return rt.path("observed-keys.json") }
func (rt *Runtime) dpStash() string         { return rt.path("dp-stash.json") }

func (rt *Runtime) isSetUp() bool {
	s, err := rt.secrets.Load()
	return err == nil && get(s, "bundle") != nil
}

func gatewayOf(s J) string {
	if g := str(s, "gateway_url"); g != "" {
		return g
	}
	return defaultGateway
}

func (rt *Runtime) bridgeVersion() string {
	rt.versionOnce.Do(func() {
		out, _ := rt.capture(20*time.Second, rt.bridgeBin, "--version")
		rt.version = "unknown"
		if line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]); line != "" {
			rt.version = line
		}
	})
	return rt.version
}

// capture runs a command in the data dir and returns its combined output.
func (rt *Runtime) capture(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = rt.dataDir
	cmd.Env = rt.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// relayJSON runs a relay subcommand: stdout is the result, stderr the error.
func (rt *Runtime) relayJSON(timeout time.Duration, stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, rt.relayBin, args...)
	cmd.Dir = rt.dataDir
	cmd.Env = rt.env()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = args[0] + " failed: " + err.Error()
		}
		return "", errors.New(msg)
	}
	return out.String(), nil
}

func (rt *Runtime) env() []string {
	return append(os.Environ(), "HOME="+rt.dataDir, "TMPDIR="+rt.runDir)
}

// ensureLocalSecrets creates the secrets that never leave this machine.
func (rt *Runtime) ensureLocalSecrets() error {
	return rt.secrets.Update(func(s J) error {
		st := obj(s, "settings")
		for _, k := range []string{"prov_secret", "pickle_key", "api_secret"} {
			if str(st, k) == "" {
				st[k] = randomSecret(32)
			}
		}
		if _, ok := st["extra_users"]; !ok {
			st["extra_users"] = []any{}
		}
		if str(s, "gateway_url") == "" {
			s["gateway_url"] = defaultGateway
		}
		return nil
	})
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// writeRunConfigs writes the bridge and relay configs from the secrets.
// The supervisor deletes them once both processes have read them.
func (rt *Runtime) writeRunConfigs() error {
	if err := rt.ensureLocalSecrets(); err != nil {
		return err
	}
	s, err := rt.secrets.Load()
	if err != nil {
		return err
	}
	bundle, settings := get(s, "bundle"), get(s, "settings")
	if bundle == nil {
		return errors.New("bridge is not set up")
	}

	// Example config from the bundled bridge version keeps upstream defaults.
	example, exVersion := rt.path("example-config.yaml"), rt.path("example-config.version")
	version := rt.bridgeVersion()
	if v, _ := os.ReadFile(exVersion); !exists(example) || string(v) != version {
		_ = os.Remove(example)
		out, _ := rt.capture(30*time.Second, rt.bridgeBin, "-e", "-c", example)
		if !exists(example) {
			return fmt.Errorf("bridge could not generate its example config: %s", trimOut(out))
		}
		_ = os.WriteFile(exVersion, []byte(version), 0o600)
	}

	// Chats whose room creation keeps failing would hold up the history queue.
	_, _ = rt.capture(30*time.Second, rt.relayBin, "history-settle", rt.bridgeDB(), rt.logDir)

	port, err := freePort()
	if err != nil {
		return err
	}
	opts := settings["options"]
	if opts == nil {
		opts = J{}
	}
	extra := settings["extra_users"]
	if extra == nil {
		extra = []any{}
	}
	bundleFile, settingsFile := filepath.Join(rt.runDir, "bundle.json"), filepath.Join(rt.runDir, "settings.json")
	defer os.Remove(bundleFile)
	defer os.Remove(settingsFile)
	if err = writeJSON(bundleFile, bundle); err != nil {
		return err
	}
	if err = writeJSON(settingsFile, J{
		"data_dir": rt.dataDir, "relay_port": port,
		"prov_secret": str(settings, "prov_secret"), "pickle_key": str(settings, "pickle_key"),
		"extra_users": extra, "log_level": "info", "options": opts,
	}); err != nil {
		return err
	}
	_ = os.Remove(rt.bridgeConfig())
	out, _ := rt.capture(30*time.Second, rt.relayBin, "bridge-config", example, bundleFile, settingsFile, rt.bridgeConfig())
	if !exists(rt.bridgeConfig()) {
		return fmt.Errorf("config generation failed: %s", trimOut(out))
	}
	if err = writeJSON(rt.relayConfig(), J{
		"listen": fmt.Sprintf("127.0.0.1:%d", port), "upstream": gatewayOf(s),
		"as_token": str(bundle, "as_token"), "api_secret": str(settings, "api_secret"),
		"prov_secret": str(settings, "prov_secret"), "owner": str(bundle, "owner"),
		"status_file": rt.relayStatusFile(), "keys_file": rt.observedKeys(),
	}); err != nil {
		return err
	}
	return rt.secrets.Update(func(s J) error { obj(s, "settings")["relay_port"] = port; return nil })
}

func trimOut(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (rt *Runtime) deleteRunConfigs() {
	_ = os.Remove(rt.bridgeConfig())
	_ = os.Remove(rt.relayConfig())
}

func (rt *Runtime) relayStatus() J {
	data, err := os.ReadFile(rt.relayStatusFile())
	if err != nil {
		return J{}
	}
	m, err := decodeJ(data)
	if err != nil {
		return J{}
	}
	return m
}

// localCall: HTTP call to the relay's or the calls daemon's local API.
// Status 0 means not reachable.
func (rt *Runtime) localCall(portKey, method, path string, body []byte, timeout time.Duration) (int, []byte) {
	s, err := rt.secrets.Load()
	if err != nil {
		return 0, errJSON(err.Error())
	}
	settings := get(s, "settings")
	port := num(settings, portKey)
	if port == 0 {
		what := "bridge"
		if portKey == "wacall_port" {
			what = "calls"
		}
		return 0, errJSON(what + " not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), rd)
	if err != nil {
		return 0, errJSON(err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+str(settings, "api_secret"))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, errJSON(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, data
}

func errJSON(msg string) []byte {
	b, _ := json.Marshal(J{"error": msg})
	return b
}

func (rt *Runtime) callsEnabled() bool {
	s, err := rt.secrets.Load()
	return err == nil && boolean(get(s, "settings"), "calls_enabled")
}

// wacallConfig: the calls daemon's config, given on stdin.
func (rt *Runtime) wacallConfig() ([]byte, error) {
	if err := rt.ensureLocalSecrets(); err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	var cfg J
	err = rt.secrets.Update(func(s J) error {
		st := obj(s, "settings")
		st["wacall_port"] = port
		cfg = J{
			"data_dir": rt.dataDir, "listen": fmt.Sprintf("127.0.0.1:%d", port),
			"api_secret": str(st, "api_secret"), "gateway": gatewayOf(s),
			"token": str(get(s, "bundle"), "as_token"), "log_level": "info",
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(cfg)
}

// ---------------------------------------------------------------- relay tools

func (rt *Runtime) securityInfo() (string, error) {
	s, err := rt.secrets.Load()
	if err != nil {
		return "", err
	}
	b := get(s, "bundle")
	return rt.relayJSON(30*time.Second, nil, "security-info", rt.bridgeDB(),
		str(get(s, "settings"), "pickle_key"), str(b, "owner"), str(b, "bot_mxid"), rt.observedKeys())
}

func (rt *Runtime) historyInfo() (string, error) {
	return rt.relayJSON(60*time.Second, nil, "history-info", rt.bridgeDB(), rt.logDir)
}

func (rt *Runtime) historyCatchup(mode, login string, chats []string) (string, error) {
	args := []string{"history-catchup", rt.bridgeDB()}
	if mode != "" {
		args = append(args, "--mode", mode)
	}
	if login != "" {
		args = append(args, "--login", login)
	}
	if len(chats) > 0 {
		args = append(args, "--chats", strings.Join(chats, ","))
	}
	return rt.relayJSON(60*time.Second, nil, args...)
}

func (rt *Runtime) historyStop() (string, error) {
	return rt.relayJSON(60*time.Second, nil, "history-stop", rt.bridgeDB())
}

// namesConfig goes to the relay on stdin, so the bridge token never hits the disk.
func (rt *Runtime) namesConfig() ([]byte, error) {
	s, err := rt.secrets.Load()
	if err != nil {
		return nil, err
	}
	b := get(s, "bundle")
	return json.Marshal(J{
		"upstream": gatewayOf(s), "as_token": str(b, "as_token"),
		"bot_mxid": str(b, "bot_mxid"), "username_template": str(b, "username_template"),
	})
}

func (rt *Runtime) namesInfo() (string, error) {
	cfg, err := rt.namesConfig()
	if err != nil {
		return "", err
	}
	return rt.relayJSON(5*time.Minute, cfg, "names-info", rt.bridgeDB(), "--live")
}

func (rt *Runtime) namesFix() (string, error) {
	cfg, err := rt.namesConfig()
	if err != nil {
		return "", err
	}
	return rt.relayJSON(10*time.Minute, cfg, "names-fix", rt.bridgeDB())
}

func (rt *Runtime) doublePuppet(action string) (string, error) {
	return rt.relayJSON(60*time.Second, nil, "double-puppet", rt.bridgeDB(), rt.dpStash(), action)
}

// wipe removes the bridge setup and data (the web UI password stays).
func (rt *Runtime) wipe() {
	rt.secrets.Wipe()
	_ = os.RemoveAll(rt.dataDir)
	_ = os.RemoveAll(rt.runDir)
	for _, d := range []string{rt.dataDir, rt.runDir, rt.logDir} {
		_ = os.MkdirAll(d, 0o700)
	}
}
