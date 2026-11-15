package main

// Moving the bridge between devices (phone ↔ Docker, Docker ↔ Docker), same
// protocol as the phone app (bridge_app/lib/screens/move_screen.dart):
//
// The old device shows a code tjena-bridge://move?id=…&key=…&gw=… . The new
// one (signed in with the same tjena account) reads it, or, without a camera,
// connects without the code and both compare a 6-digit number (kex.go). Both
// connect to the gateway (/_tjena/move/v1/send and /receive), which only
// pipes the data through.
//
// Messages: both get {"type":"paired"}; new → old {"type":"hello",…} (kex.go);
// old → new {"type":"meta","size":N}, binary chunks, {"type":"end"};
// new → old {"type":"received"}, then {"type":"imported","proof":…} or
// {"type":"error","message":…}. The old device removes its copy only after
// an "imported" with a valid proof.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type MoveCode struct{ ID, Key, Gateway string }

func newMoveCode(gateway string) MoveCode {
	return MoveCode{randomSecret(18), randomSecret(32), gateway}
}

func (c MoveCode) URI() string {
	q := url.Values{"id": {c.ID}, "key": {c.Key}, "gw": {c.Gateway}}
	return "tjena-bridge://move?" + q.Encode()
}

func parseMoveCode(raw string) (MoveCode, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "tjena-bridge" || u.Host != "move" {
		return MoveCode{}, errors.New("This is not a move code. On the old device open Settings → Move to a new phone.")
	}
	q := u.Query()
	c := MoveCode{q.Get("id"), q.Get("key"), q.Get("gw")}
	if c.Gateway == "" {
		c.Gateway = defaultGateway
	}
	if c.ID == "" || c.Key == "" || !isHTTPURL(c.Gateway) {
		return MoveCode{}, errors.New("The move code is incomplete.")
	}
	return c, nil
}

func (c MoveCode) keyBytes() ([]byte, error) {
	k, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(c.Key, "="))
	if err != nil || len(k) != 32 {
		return nil, errors.New("bad key in the move code")
	}
	return k, nil
}

func (c MoveCode) wsURL(role string) string {
	u, _ := url.Parse(trimURL(c.Gateway))
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path += "/_tjena/move/v1/" + role
	if c.ID != "" {
		u.RawQuery = url.Values{"id": {c.ID}}.Encode()
	}
	return u.String()
}

func msgType(data []byte) (string, J) {
	m, err := decodeJ(data)
	if err != nil {
		return "", nil
	}
	return str(m, "type"), m
}

func wsText(ctx context.Context, ws *websocket.Conn, v any) error {
	data, _ := json.Marshal(v)
	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return ws.Write(wctx, websocket.MessageText, data)
}

type frame struct {
	typ  websocket.MessageType
	data []byte
}

// readFrames reads in the background, so the connection keeps answering the
// gateway's pings while we wait for the user. The channel closes at the end.
func readFrames(ctx context.Context, ws *websocket.Conn) <-chan frame {
	ch := make(chan frame, 8)
	go func() {
		defer close(ch)
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			select {
			case ch <- frame{typ, data}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

func dialMove(ctx context.Context, c MoveCode, role, token string) (*websocket.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(dctx, c.wsURL(role), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			if c.ID == "" {
				return nil, errors.New("Your old device isn't ready yet. On it open Settings → Move to a new phone, then try again.")
			}
			return nil, errors.New("Your old device isn't waiting for this code. Open \"Move to a new phone\" on it again.")
		}
		return nil, fmt.Errorf("Can't reach the tjena server: %v", err)
	}
	ws.SetReadLimit(4 << 20)
	return ws, nil
}

// ---------------------------------------------------------------- old device

// moveOut runs on the old device (this one): wait for the new one, check it
// (code proof, or matching numbers), send, remove the bridge here once the
// new one has it.
func (m *Manager) moveOut(ctx context.Context, code MoveCode, token string, confirm <-chan bool) {
	stopped, received := false, false
	fail := func(msg string) {
		m.jobSet(func(j *Job) { j.Phase, j.Error, j.Done = "failed", msg, true })
		if stopped && !received {
			m.startBridge() // nothing usable reached the new device: keep it here
		}
	}
	ws, err := dialMove(ctx, code, "send", token)
	if err != nil {
		fail(err.Error())
		return
	}
	defer ws.CloseNow()
	frames := readFrames(ctx, ws)
	codeKey, _ := code.keyBytes()

	var key []byte // the transfer key, once agreed
	var kx *kex
	var commit string
	localOK, remoteOK, sending := false, false, false
	sendErr := make(chan error, 1)
	startSend := func(k []byte) {
		key, sending, stopped = k, true, true
		m.jobSet(func(j *Job) { j.Phase, j.Progress = "sending", "Preparing the bridge data…" })
		m.sup.Stop()
		go func() {
			if err := m.sendBridge(ctx, ws, k); err != nil {
				sendErr <- err
				_ = ws.Close(websocket.StatusInternalError, "send failed")
			}
		}()
	}
loop:
	for {
		select {
		case ok := <-confirm:
			if kx == nil || key == nil || sending {
				continue
			}
			if !ok {
				_ = ws.Close(websocket.StatusNormalClosure, "codes differ")
				fail("Cancelled: the numbers didn't match. Nothing was sent.")
				return
			}
			localOK = true
			m.jobSet(func(j *Job) { j.Confirmed = true })
			if remoteOK {
				startSend(key)
			}
		case err := <-sendErr:
			fail("Sending failed: " + err.Error())
			return
		case f, open := <-frames:
			if !open {
				break loop
			}
			if f.typ != websocket.MessageText {
				continue
			}
			typ, msg := msgType(f.data)
			switch typ {
			case "paired":
				m.jobSet(func(j *Job) { j.Progress = "The new device is connected…" })
			case "hello":
				if sending || kx != nil {
					continue
				}
				switch str(msg, "mode") {
				case "code":
					if !checkProof(codeKey, proofHello, str(msg, "proof")) {
						fail("The new device has a different code. Try again.")
						return
					}
					startSend(codeKey)
				case "compare":
					commit = str(msg, "commit")
					if kx, err = newKex(); err != nil {
						fail(err.Error())
						return
					}
					m.jobSet(func(j *Job) { j.Mode = "compare" })
					if err = wsText(ctx, ws, J{"type": "key", "pub": kx.pubB64()}); err != nil {
						break loop
					}
				}
			case "key":
				if kx == nil || key != nil {
					continue
				}
				pub := str(msg, "pub")
				raw, err := b64.DecodeString(pub)
				if err != nil || commitOf(raw) != commit {
					fail("The key check failed, so nothing was sent. Try again.")
					return
				}
				k, sas, err := kx.derive(pub, true)
				if err != nil {
					fail(err.Error())
					return
				}
				key = k
				m.jobSet(func(j *Job) { j.Phase, j.SAS = "compare", sas })
			case "confirm":
				remoteOK = true
				if localOK && key != nil && !sending {
					startSend(key)
				}
			case "received":
				received = true
				m.jobSet(func(j *Job) { j.Phase, j.Progress = "importing", "The new device is setting up the bridge…" })
			case "imported":
				if key == nil || !checkProof(key, proofImported, str(msg, "proof")) {
					m.log.add("manager", "move: ignored an \"imported\" without a valid proof")
					continue
				}
				_ = ws.Close(websocket.StatusNormalClosure, "")
				m.wipeLocal()
				m.jobSet(func(j *Job) {
					j.Phase, j.Done, j.Progress = "done", true,
						"Moved. The new device runs your bridge now, and the bridge data was removed here."
				})
				return
			case "error":
				fail("The new device could not take over: " + str(msg, "message"))
				return
			}
		}
	}
	select {
	case err := <-sendErr:
		fail("Sending failed: " + err.Error())
		return
	default:
	}
	if ctx.Err() != nil {
		fail("Cancelled.")
		return
	}
	if received {
		// The new device has everything but didn't confirm. Starting here
		// again could run WhatsApp twice: ask.
		m.jobSet(func(j *Job) { j.Phase, j.Done = "unsure", true })
		return
	}
	if !sending {
		fail("The connection to the tjena server ended.")
	} else {
		fail("The transfer was interrupted.")
	}
}

func (m *Manager) sendBridge(ctx context.Context, ws *websocket.Conn, key []byte) error {
	data, err := exportForMove(m.rt, key)
	if err != nil {
		return err
	}
	size := int64(len(data))
	m.jobSet(func(j *Job) { j.Size, j.Progress = size, "Sending to the new device…" })
	if err = wsText(ctx, ws, J{"type": "meta", "size": size}); err != nil {
		return err
	}
	const chunk = 256 << 10
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err = ws.Write(wctx, websocket.MessageBinary, data[off:end])
		cancel()
		if err != nil {
			return err
		}
		sent := int64(end)
		m.jobSet(func(j *Job) { j.Sent = sent })
	}
	return wsText(ctx, ws, J{"type": "end"})
}

// ---------------------------------------------------------------- new device

// moveIn runs on the new device (this one): receive, import, take over the
// bridge slot with session (fresh tokens, same bot), start. A code without
// a key (only the gateway) means: no code, compare numbers instead.
func (m *Manager) moveIn(ctx context.Context, code MoveCode, session *Session, confirm <-chan bool) error {
	compare := code.Key == ""
	var key []byte
	if !compare {
		k, err := code.keyBytes()
		if err != nil {
			return err
		}
		key = k
	}
	progress := func(text string, frac float64) {
		m.jobSet(func(j *Job) { j.Progress, j.Fraction = text, frac })
	}
	progress("Connecting to your old device…", -1)
	ws, err := dialMove(ctx, code, "receive", session.AccessToken)
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	path := filepath.Join(m.rt.runDir, "move-in.tjmv")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	defer f.Close()
	var size, got int64
	rctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	frames := readFrames(rctx, ws)
	var kx *kex
	confirmed := !compare
	for done := false; !done; {
		select {
		case <-rctx.Done():
			if ctx.Err() != nil {
				return errors.New("Cancelled.")
			}
			return errors.New("The transfer took too long.")
		case ok := <-confirm:
			if !compare || key == nil || confirmed {
				continue
			}
			if !ok {
				_ = ws.Close(websocket.StatusNormalClosure, "codes differ")
				return errors.New("Cancelled: the numbers didn't match. Nothing was received.")
			}
			confirmed = true
			m.jobSet(func(j *Job) { j.Confirmed = true })
			progress("Confirm on your old device too…", -1)
			if err := wsText(ctx, ws, J{"type": "confirm"}); err != nil {
				return errors.New("Your old device stopped the transfer.")
			}
		case fr, open := <-frames:
			if !open {
				if rctx.Err() != nil {
					continue // handled above
				}
				return errors.New("Your old device stopped the transfer.")
			}
			if fr.typ == websocket.MessageBinary {
				if !confirmed || key == nil {
					return errors.New("The old device sent data before the numbers were checked.")
				}
				if _, err = f.Write(fr.data); err != nil {
					return err
				}
				got += int64(len(fr.data))
				frac := -1.0
				if size > 0 {
					frac = float64(got) / float64(size)
				}
				progress("Receiving from your old device…", frac)
				continue
			}
			t, msg := msgType(fr.data)
			switch t {
			case "paired":
				hello := J{"type": "hello", "mode": "code", "proof": moveProof(key, proofHello)}
				if compare {
					if kx, err = newKex(); err != nil {
						return err
					}
					hello = J{"type": "hello", "mode": "compare", "commit": commitOf(kx.pub)}
					progress("Connected. Exchanging keys…", -1)
				} else {
					progress("Your old device is preparing the bridge…", -1)
				}
				if err := wsText(ctx, ws, hello); err != nil {
					return errors.New("Your old device stopped the transfer.")
				}
			case "key":
				if kx == nil || key != nil {
					continue
				}
				k, sas, err := kx.derive(str(msg, "pub"), false)
				if err != nil {
					return err
				}
				if err := wsText(ctx, ws, J{"type": "key", "pub": kx.pubB64()}); err != nil {
					return errors.New("Your old device stopped the transfer.")
				}
				key = k
				m.jobSet(func(j *Job) { j.Phase, j.Mode, j.SAS = "compare", "compare", sas })
				progress("Check that both devices show the same number.", -1)
			case "meta":
				if !confirmed || key == nil {
					return errors.New("The old device sent data before the numbers were checked.")
				}
				size = int64(num(msg, "size"))
				m.jobSet(func(j *Job) { j.Phase = "receiving" })
			case "end":
				done = true
			}
		}
	}
	if err = f.Close(); err != nil {
		return err
	}
	_ = wsText(ctx, ws, J{"type": "received"})
	progress("Setting up the bridge here…", -1)
	data, err := os.ReadFile(path)
	if err == nil {
		m.sup.Stop()
		err = importFromMove(m.rt, data, key)
	}
	if err != nil {
		_ = wsText(ctx, ws, J{"type": "error", "message": err.Error()})
		return err
	}
	_ = wsText(ctx, ws, J{"type": "imported", "proof": moveProof(key, proofImported)})
	_ = ws.Close(websocket.StatusNormalClosure, "")

	// Take over the slot: the old device's tokens stop working, same bridge
	// bot, so all chats in tjena stay as they are.
	progress("Making this the active bridge…", -1)
	s, _ := m.rt.secrets.Load()
	old := sessionFrom(get(s, "session"))
	bundle, err := Provisioner{code.Gateway, session.AccessToken}.claim()
	if err == nil {
		err = m.saveSetup(bundle, session, code.Gateway)
	}
	if err == nil {
		if old != nil && old.DeviceID != session.DeviceID {
			matrixLogout(old) // the old device's tjena session
		}
	} else {
		// The moved setup still works as it is; Settings → Make this the
		// active bridge can retry.
		m.log.add("manager", "move: taking over the slot failed: "+err.Error())
		matrixLogout(session)
	}
	m.startBridge()
	return nil
}
