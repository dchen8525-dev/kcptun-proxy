// Package socks implements the local proxy entry and the tunnel's OPEN frame.
//
// One TCP port speaks three protocols, matching FCP's local entry:
//   - SOCKS5 CONNECT (no auth)
//   - HTTP CONNECT
//   - absolute-form HTTP (GET http://host/path HTTP/1.1), rewritten to origin-form
//
// The parsed target is sent to the server as the first bytes of the smux stream
// (OPEN), so the server does not need a second handshake.
package socks

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

const (
	AtypIPv4   byte = 1
	AtypDomain byte = 3
	AtypIPv6   byte = 4

	maxHeadBytes  = 64 << 10
	maxDomainLen  = 255
	socks5Version = 0x05
	socks5Connect = 0x01
)

var (
	ErrMalformed    = errors.New("malformed proxy request")
	ErrHeadTooLarge = errors.New("proxy request head too large")
	ErrUnsupported  = errors.New("unsupported proxy command or method")
	ErrBadOpen      = errors.New("bad OPEN frame")
)

// ---------------------------------------------------------------- target ----

type Target struct {
	Atyp byte
	Addr []byte
	Port uint16
}

func NewTarget(host string, port int) (Target, error) {
	if port < 1 || port > 65535 {
		return Target{}, fmt.Errorf("bad port %d", port)
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return Target{Atyp: AtypIPv4, Addr: append([]byte(nil), v4...), Port: uint16(port)}, nil
		}
		return Target{Atyp: AtypIPv6, Addr: append([]byte(nil), ip.To16()...), Port: uint16(port)}, nil
	}
	if host == "" || len(host) > maxDomainLen || !hostCharsOK(host) {
		return Target{}, fmt.Errorf("bad hostname %q", host)
	}
	return Target{Atyp: AtypDomain, Addr: []byte(host), Port: uint16(port)}, nil
}

func (t Target) Host() string {
	switch t.Atyp {
	case AtypIPv4, AtypIPv6:
		return net.IP(t.Addr).String()
	default:
		return string(t.Addr)
	}
}

func (t Target) Address() string {
	return net.JoinHostPort(t.Host(), strconv.Itoa(int(t.Port)))
}

func (t Target) String() string { return t.Address() }

func (t Target) valid() bool {
	if t.Port == 0 || len(t.Addr) == 0 {
		return false
	}
	switch t.Atyp {
	case AtypIPv4:
		return len(t.Addr) == 4
	case AtypIPv6:
		return len(t.Addr) == 16
	case AtypDomain:
		return len(t.Addr) <= maxDomainLen && hostCharsOK(string(t.Addr))
	}
	return false
}

func hostCharsOK(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return len(s) > 0
}

func ipv6CharsOK(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') ||
			(c >= 'A' && c <= 'F') || c == ':' || c == '.' {
			continue
		}
		return false
	}
	return len(s) > 0
}

// ------------------------------------------------------------ OPEN frame ----

var openMagic = [4]byte{'K', 'C', 'P', 'X'}

const (
	openVersion   byte = 1
	openHeaderLen      = 7 // magic4 + ver1 + atyp1 + addrlen1
	openReplyLen       = 8
)

type OpenError byte

const (
	OpenOK           OpenError = 0
	OpenRefused      OpenError = 1
	OpenDNS          OpenError = 2
	OpenTimeout      OpenError = 3
	OpenNotSupported OpenError = 4
	OpenTooMany      OpenError = 5
	OpenGeneral      OpenError = 255
)

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// WriteOpen encodes the target as the first bytes of the stream.
func WriteOpen(w io.Writer, t Target) error {
	if !t.valid() {
		return fmt.Errorf("%w: invalid target %+v", ErrBadOpen, t)
	}
	buf := make([]byte, openHeaderLen+len(t.Addr)+2)
	copy(buf[0:4], openMagic[:])
	buf[4] = openVersion
	buf[5] = t.Atyp
	buf[6] = byte(len(t.Addr))
	copy(buf[7:], t.Addr)
	binary.BigEndian.PutUint16(buf[7+len(t.Addr):], t.Port)
	return writeAll(w, buf)
}

func ReadOpen(r io.Reader) (Target, error) {
	hdr := make([]byte, openHeaderLen)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return Target{}, err
	}
	if !bytes.Equal(hdr[0:4], openMagic[:]) || hdr[4] != openVersion {
		return Target{}, ErrBadOpen
	}
	atyp := hdr[5]
	n := int(hdr[6])
	if n == 0 || n > maxDomainLen {
		return Target{}, ErrBadOpen
	}
	switch atyp {
	case AtypIPv4:
		if n != 4 {
			return Target{}, ErrBadOpen
		}
	case AtypIPv6:
		if n != 16 {
			return Target{}, ErrBadOpen
		}
	case AtypDomain:
	default:
		return Target{}, ErrBadOpen
	}
	rest := make([]byte, n+2)
	if _, err := io.ReadFull(r, rest); err != nil {
		return Target{}, err
	}
	t := Target{Atyp: atyp, Addr: append([]byte(nil), rest[:n]...), Port: binary.BigEndian.Uint16(rest[n:])}
	if !t.valid() {
		return Target{}, ErrBadOpen
	}
	return t, nil
}

func WriteOpenReply(w io.Writer, code OpenError) error {
	buf := make([]byte, openReplyLen)
	copy(buf[0:4], openMagic[:])
	buf[4] = openVersion
	buf[5] = byte(code)
	return writeAll(w, buf)
}

func ReadOpenReply(r io.Reader) (OpenError, error) {
	buf := make([]byte, openReplyLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return OpenGeneral, err
	}
	if !bytes.Equal(buf[0:4], openMagic[:]) || buf[4] != openVersion {
		return OpenGeneral, ErrBadOpen
	}
	return OpenError(buf[5]), nil
}

// ------------------------------------------------------------ local entry ----

type Entry struct {
	Kind   string // "socks5" | "connect" | "forward"
	Target Target
	Prefix []byte // rewritten HTTP head (absolute-form only); otherwise nil
}

var forwardMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "DELETE": true,
	"OPTIONS": true, "PATCH": true,
}

var replySocks5Fail = []byte{socks5Version, 0x01, 0x00, AtypIPv4, 0, 0, 0, 0, 0, 0}
var replySocks5OK = []byte{socks5Version, 0x00, 0x00, AtypIPv4, 0, 0, 0, 0, 0, 0}

const httpOK = "HTTP/1.1 200 Connection Established\r\n\r\n"
const http502 = "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"
const http405 = "HTTP/1.1 405 Method Not Allowed\r\nAllow: CONNECT\r\nConnection: close\r\n\r\n"

// AcceptReply returns the bytes to send to the local client once the tunnel
// OPEN has succeeded (code==0) or failed. Absolute-form HTTP gets no success
// reply: the origin server's response comes back through the tunnel.
func AcceptReply(e Entry, code OpenError) []byte {
	if e.Kind == "forward" {
		if code == OpenOK {
			return nil
		}
		return []byte(http502)
	}
	if e.Kind == "connect" {
		if code == OpenOK {
			return []byte(httpOK)
		}
		return []byte(http502)
	}
	if code == OpenOK {
		return append([]byte(nil), replySocks5OK...)
	}
	rep := byte(0x01)
	switch code {
	case OpenRefused:
		rep = 0x05
	case OpenDNS:
		rep = 0x04
	case OpenTimeout:
		rep = 0x06
	case OpenNotSupported:
		rep = 0x08
	case OpenTooMany:
		rep = 0x02
	}
	out := append([]byte(nil), replySocks5Fail...)
	out[1] = rep
	return out
}

// ParseEntry detects the protocol from the first byte and consumes exactly the
// request head. w is the local connection: SOCKS5 needs its greeting reply
// written before the request arrives. Any bytes still buffered in br are NOT
// consumed here -- the caller must relay from br, not from the raw conn.
func ParseEntry(br *bufio.Reader, w io.Writer) (Entry, error) {
	first, err := br.Peek(1)
	if err != nil {
		return Entry{}, err
	}
	if first[0] == socks5Version {
		return parseSocks5(br, w)
	}
	return parseHTTP(br)
}

func parseSocks5(br *bufio.Reader, w io.Writer) (Entry, error) {
	g := make([]byte, 2)
	if _, err := io.ReadFull(br, g); err != nil {
		return Entry{}, err
	}
	if g[0] != socks5Version || g[1] == 0 {
		return Entry{}, ErrMalformed
	}
	methods := make([]byte, int(g[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return Entry{}, err
	}
	noAuth := false
	for _, m := range methods {
		if m == 0x00 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		_ = writeAll(w, []byte{socks5Version, 0xFF})
		return Entry{}, fmt.Errorf("%w: no SOCKS5 method without auth offered", ErrUnsupported)
	}
	if err := writeAll(w, []byte{socks5Version, 0x00}); err != nil {
		return Entry{}, err
	}

	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return Entry{}, err
	}
	if hdr[0] != socks5Version || hdr[2] != 0x00 {
		return Entry{}, ErrMalformed
	}
	if hdr[1] != socks5Connect {
		return Entry{}, fmt.Errorf("%w: SOCKS5 command 0x%02x (only CONNECT)", ErrUnsupported, hdr[1])
	}
	atyp := hdr[3]
	var addr []byte
	switch atyp {
	case AtypIPv4:
		addr = make([]byte, 4)
	case AtypIPv6:
		addr = make([]byte, 16)
	case AtypDomain:
		n, err := br.ReadByte()
		if err != nil {
			return Entry{}, err
		}
		if n == 0 {
			return Entry{}, ErrMalformed
		}
		addr = make([]byte, int(n))
	default:
		return Entry{}, fmt.Errorf("%w: bad ATYP 0x%02x", ErrMalformed, atyp)
	}
	if _, err := io.ReadFull(br, addr); err != nil {
		return Entry{}, err
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return Entry{}, err
	}
	t := Target{Atyp: atyp, Addr: addr, Port: binary.BigEndian.Uint16(pb)}
	if !t.valid() {
		return Entry{}, ErrMalformed
	}
	return Entry{Kind: "socks5", Target: t}, nil
}

func readHead(br *bufio.Reader) ([]byte, error) {
	var buf bytes.Buffer
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		if buf.Len()+len(line) > maxHeadBytes {
			return nil, ErrHeadTooLarge
		}
		buf.Write(line)
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			return buf.Bytes(), nil
		}
	}
}

func parseHTTP(br *bufio.Reader) (Entry, error) {
	head, err := readHead(br)
	if err != nil {
		return Entry{}, err
	}
	norm := strings.ReplaceAll(string(head), "\r\n", "\n")
	lines := strings.Split(norm, "\n")
	if len(lines) == 0 {
		return Entry{}, ErrMalformed
	}
	tok := strings.Fields(lines[0])
	if len(tok) != 3 || (tok[2] != "HTTP/1.0" && tok[2] != "HTTP/1.1") {
		return Entry{}, ErrMalformed
	}
	method, uri, version := tok[0], tok[1], tok[2]

	if strings.EqualFold(method, "CONNECT") {
		host, port, err := parseAuthority(uri, 0)
		if err != nil || port == 0 {
			return Entry{}, fmt.Errorf("%w: bad CONNECT authority %q", ErrMalformed, uri)
		}
		t, err := NewTarget(host, port)
		if err != nil {
			return Entry{}, err
		}
		return Entry{Kind: "connect", Target: t}, nil
	}

	if !forwardMethods[strings.ToUpper(method)] {
		return Entry{}, fmt.Errorf("%w: method %q (write %s)", ErrUnsupported, method, http405)
	}
	if !strings.HasPrefix(uri, "http://") {
		return Entry{}, fmt.Errorf("%w: absolute-form URL must start with http://", ErrMalformed)
	}
	rest := uri[len("http://"):]
	slash := strings.IndexByte(rest, '/')
	authority, path := rest, "/"
	if slash >= 0 {
		authority, path = rest[:slash], rest[slash:]
	}
	host, port, err := parseAuthority(authority, 80)
	if err != nil {
		return Entry{}, err
	}
	t, err := NewTarget(host, port)
	if err != nil {
		return Entry{}, err
	}
	prefix := rewriteForward(lines, method, path, version)
	return Entry{Kind: "forward", Target: t, Prefix: prefix}, nil
}

func rewriteForward(lines []string, method, path, version string) []byte {
	out := []string{method + " " + path + " " + version}
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		name := line
		if i := strings.IndexByte(line, ':'); i >= 0 {
			name = line[:i]
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
			"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
			continue
		}
		out = append(out, line)
	}
	out = append(out, "Connection: close", "", "")
	return []byte(strings.Join(out, "\r\n"))
}

func parseAuthority(s string, defPort int) (string, int, error) {
	if s == "" {
		return "", 0, fmt.Errorf("empty authority")
	}
	if strings.HasPrefix(s, "[") {
		close := strings.IndexByte(s, ']')
		if close < 2 || !ipv6CharsOK(s[1:close]) {
			return "", 0, fmt.Errorf("bad IPv6 authority %q", s)
		}
		host := s[1:close]
		port := defPort
		if close+1 < len(s) {
			if s[close+1] != ':' {
				return "", 0, fmt.Errorf("bad IPv6 authority %q", s)
			}
			p, err := parsePort(s[close+2:])
			if err != nil {
				return "", 0, err
			}
			port = p
		}
		return host, port, nil
	}
	if strings.Count(s, ":") > 1 {
		return "", 0, fmt.Errorf("bad authority %q", s)
	}
	host, portStr := s, ""
	if i := strings.IndexByte(s, ':'); i >= 0 {
		host, portStr = s[:i], s[i+1:]
	}
	if !hostCharsOK(host) || len(host) > maxDomainLen {
		return "", 0, fmt.Errorf("bad host %q", host)
	}
	port := defPort
	if portStr != "" {
		p, err := parsePort(portStr)
		if err != nil {
			return "", 0, err
		}
		port = p
	}
	return host, port, nil
}

func parsePort(s string) (int, error) {
	if s == "" || len(s) > 5 {
		return 0, fmt.Errorf("bad port %q", s)
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("bad port %q", s)
		}
		n = n*10 + int(s[i]-'0')
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("bad port %q", s)
	}
	return n, nil
}
