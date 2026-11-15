package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Removes secrets and personal data from log lines before they are shown
// or exported. Same patterns as the phone app (LogRedactor).
var redactions = []struct {
	re  *regexp.Regexp
	rep string
}{
	{regexp.MustCompile(`(?i)(access_token|as_token|hs_token|token|secret|password|pickle_key)(["'=: ]+)[^"'&\s,}]+`), "${1}${2}[redacted]"},
	{regexp.MustCompile(`Bearer [A-Za-z0-9._~+/=-]+`), "Bearer [redacted]"},
	{regexp.MustCompile(`\+?\b\d{9,15}\b`), "[number]"},
	{regexp.MustCompile(`\b\d{9,15}@(s\.whatsapp\.net|lid|g\.us)`), "[jid]"},
	{regexp.MustCompile(`(?i)"(body|formatted_body|text|caption)":"(\\.|[^"\\])*"`), `"${1}":"[content]"`},
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func redact(line string) string {
	out := ansi.ReplaceAllString(line, "")
	for _, r := range redactions {
		out = r.re.ReplaceAllString(out, r.rep)
	}
	return out
}

// LogBuf keeps the recent process output (redacted) and copies it to stdout,
// so `docker logs` shows it too.
type LogBuf struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newLogBuf() *LogBuf { return &LogBuf{max: 1000} }

func (l *LogBuf) add(name, line string) {
	clean := fmt.Sprintf("[%s] %s", name, redact(line))
	l.mu.Lock()
	l.lines = append(l.lines, clean)
	if len(l.lines) > l.max {
		l.lines = l.lines[len(l.lines)-l.max:]
	}
	l.mu.Unlock()
	fmt.Fprintln(os.Stdout, clean)
}

func (l *LogBuf) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// lineWriter splits a process's output into lines for the LogBuf and hands
// each line to onLine (the supervisor watches for some of them).
type lineWriter struct {
	name   string
	log    *LogBuf
	onLine func(string)
	mu     sync.Mutex
	buf    bytes.Buffer
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf.Next(i+1)), "\r\n")
		w.log.add(w.name, line)
		if w.onLine != nil {
			w.onLine(line)
		}
	}
	if w.buf.Len() > 64<<10 { // a very long line without newline
		w.log.add(w.name, w.buf.String())
		w.buf.Reset()
	}
	return len(p), nil
}

// exportLogs writes the bridge's log files and the recent output, redacted.
func exportLogs(w io.Writer, rt *Runtime, sup *Supervisor, log *LogBuf) {
	st := sup.snapshot()
	fmt.Fprintf(w, "tjena Bridge (Docker) log export (secrets, numbers and message text removed)\n")
	fmt.Fprintf(w, "bridge: %s\nstate: %s %s\n\n", rt.bridgeVersion(), st.State, st.Detail)
	entries, _ := os.ReadDir(rt.logDir)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "==== %s\n", n)
		f, err := os.Open(filepath.Join(rt.logDir, n))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			fmt.Fprintln(w, redact(sc.Text()))
		}
		f.Close()
	}
	fmt.Fprintf(w, "==== recent process output\n%s\n", log.text())
}
