package main

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Who may open the web UI. WEBUI_ALLOW:
//
//	private (default)  this machine, home networks (10/8, 172.16/12, 192.168/16),
//	                   CGNAT/Tailscale (100.64/10), link-local, IPv6 ULA
//	any                no address check (only behind a proxy that does its own)
//	CIDR list          e.g. "192.168.1.0/24, fd00::/8" (this machine always works)
type netGuard struct {
	prefixes []netip.Prefix // nil = any
}

var privateNets = []string{
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"100.64.0.0/10",
	"169.254.0.0/16", "fe80::/10",
	"fc00::/7",
}

func parseAllow(s string) (*netGuard, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "any", "all", "*":
		return &netGuard{}, nil
	case "", "private", "lan":
		s = strings.Join(privateNets, ",")
	default:
		s += ",127.0.0.0/8,::1/128"
	}
	g := &netGuard{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			a, err := netip.ParseAddr(p)
			if err != nil {
				return nil, errors.New("WEBUI_ALLOW: " + p + " is not an address or network")
			}
			p = netip.PrefixFrom(a, a.BitLen()).String()
		}
		pr, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, errors.New("WEBUI_ALLOW: " + p + " is not an address or network")
		}
		g.prefixes = append(g.prefixes, pr.Masked())
	}
	return g, nil
}

func (g *netGuard) allowed(remoteAddr string) bool {
	if g.prefixes == nil {
		return true
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap().WithZone("")
	for _, p := range g.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (g *netGuard) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.allowed(r.RemoteAddr) {
			http.Error(w, "tjena Bridge: this address may not open the web UI (see WEBUI_ALLOW).", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// localHost reports whether the page was opened by an IP address or a local
// name (localhost, nas, bridge.local, …). Choosing the first password is only
// allowed then: a site on the internet that points its own name at this
// address (DNS rebinding) can't claim an unprotected web UI.
func localHost(hostHeader string) bool {
	h := hostHeader
	if host, _, err := net.SplitHostPort(hostHeader); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.Trim(strings.ToLower(h), "[]"), ".")
	if h == "" {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	if !strings.Contains(h, ".") {
		return true // localhost, nas, homeserver
	}
	for _, suf := range []string{".local", ".lan", ".home", ".home.arpa", ".internal", ".localhost", ".localdomain", ".ts.net", ".fritz.box", ".speedport.ip"} {
		if strings.HasSuffix(h, suf) {
			return true
		}
	}
	return false
}
