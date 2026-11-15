package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultHomeserver = "https://matrix.tjena.eu"
	sessionDeviceName = "WhatsApp bridge (Docker)"
)

// APIError: an error from the homeserver or gateway, with its HTTP status.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string { return e.Msg }

type Session struct {
	Homeserver  string `json:"homeserver"`
	UserID      string `json:"user_id"`
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
}

func sessionFrom(m J) *Session {
	if m == nil || str(m, "access_token") == "" {
		return nil
	}
	return &Session{str(m, "homeserver"), str(m, "user_id"), str(m, "access_token"), str(m, "device_id")}
}

func (s *Session) J() J {
	return J{"homeserver": s.Homeserver, "user_id": s.UserID, "access_token": s.AccessToken, "device_id": s.DeviceID}
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func doJSON(method, u, token string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return &APIError{0, err.Error()}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		msg := strings.TrimSpace(string(data))
		var e struct{ Error, Errcode string }
		if json.Unmarshal(data, &e) == nil && (e.Error != "" || e.Errcode != "") {
			msg = e.Error
			if msg == "" {
				msg = e.Errcode
			}
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return &APIError{resp.StatusCode, msg}
	}
	if out != nil {
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		return d.Decode(out)
	}
	return nil
}

func trimURL(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// matrixLogin creates this bridge's own Matrix session.
func matrixLogin(homeserver, loginToken, user, password string) (*Session, error) {
	hs := trimURL(homeserver)
	body := J{"initial_device_display_name": sessionDeviceName}
	if loginToken != "" {
		body["type"], body["token"] = "m.login.token", loginToken
	} else {
		body["type"] = "m.login.password"
		body["identifier"] = J{"type": "m.id.user", "user": user}
		body["password"] = password
	}
	var r struct {
		UserID      string `json:"user_id"`
		AccessToken string `json:"access_token"`
		DeviceID    string `json:"device_id"`
	}
	if err := doJSON("POST", hs+"/_matrix/client/v3/login", "", body, &r); err != nil {
		return nil, err
	}
	return &Session{hs, r.UserID, r.AccessToken, r.DeviceID}, nil
}

func matrixLogout(s *Session) {
	if s == nil {
		return
	}
	_ = doJSON("POST", s.Homeserver+"/_matrix/client/v3/logout", s.AccessToken, J{}, nil)
}

// Provisioner on the gateway (/provision/v1).
type Provisioner struct{ gateway, token string }

func (p Provisioner) url() string { return trimURL(p.gateway) + "/provision/v1/bridge" }

// claim assigns a bridge slot, or new tokens for the existing one (same bot).
func (p Provisioner) claim() (J, error) {
	var b J
	err := doJSON("POST", p.url(), p.token, J{}, &b)
	return b, err
}

// status of the user's bridge, or nil if there is none.
func (p Provisioner) status() (J, error) {
	var b J
	err := doJSON("GET", p.url(), p.token, nil, &b)
	if e, ok := err.(*APIError); ok && e.Status == 404 {
		return nil, nil
	}
	return b, err
}

func (p Provisioner) revoke() error {
	err := doJSON("DELETE", p.url(), p.token, nil, nil)
	if e, ok := err.(*APIError); ok && e.Status == 404 {
		return nil
	}
	return err
}

// PairingLink: the QR code from the tjena app,
// tjena-bridge://login?hs=…&gw=…&user=@me:tjena.eu&token=LOGIN_TOKEN
type PairingLink struct{ Homeserver, Gateway, User, Token string }

func parsePairingLink(raw string) *PairingLink {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "tjena-bridge" || u.Host != "login" {
		return nil
	}
	q := u.Query()
	l := &PairingLink{q.Get("hs"), q.Get("gw"), q.Get("user"), q.Get("token")}
	if l.Homeserver == "" {
		l.Homeserver = defaultHomeserver
	}
	if l.Gateway == "" {
		l.Gateway = defaultGateway
	}
	if !isHTTPURL(l.Homeserver) || !isHTTPURL(l.Gateway) {
		return nil
	}
	return l
}
