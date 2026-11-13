package socks

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestTargetRoundTrip(t *testing.T) {
	cases := []Target{
		{Atyp: AtypIPv4, Addr: []byte{93, 184, 216, 34}, Port: 443},
		{Atyp: AtypDomain, Addr: []byte("example.com"), Port: 80},
		{Atyp: AtypIPv6, Addr: net.ParseIP("2001:db8::1").To16(), Port: 8080},
	}
	for _, want := range cases {
		var buf bytes.Buffer
		if err := WriteOpen(&buf, want); err != nil {
			t.Fatal(err)
		}
		got, err := ReadOpen(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if got.Atyp != want.Atyp || !bytes.Equal(got.Addr, want.Addr) || got.Port != want.Port {
			t.Fatalf("got %+v want %+v", got, want)
		}
		if got.Address() != want.Address() {
			t.Fatalf("address %q != %q", got.Address(), want.Address())
		}
	}
}

func TestOpenBadFrames(t *testing.T) {
	bad := [][]byte{
		{'X', 'X', 'X', 'X', 1, AtypIPv4, 4, 1, 2, 3, 4, 0, 80},
		{'K', 'C', 'P', 'X', 9, AtypIPv4, 4, 1, 2, 3, 4, 0, 80},
		{'K', 'C', 'P', 'X', 1, 9, 4, 1, 2, 3, 4, 0, 80},
		{'K', 'C', 'P', 'X', 1, AtypIPv4, 5, 1, 2, 3, 4, 0, 80},
		{'K', 'C', 'P', 'X', 1, AtypIPv4, 0},
		{'K', 'C', 'P', 'X'},
	}
	for i, b := range bad {
		if _, err := ReadOpen(bytes.NewReader(b)); err == nil {
			t.Fatalf("bad frame %d accepted", i)
		}
	}
	if err := WriteOpen(&bytes.Buffer{}, Target{}); err == nil {
		t.Fatal("invalid target written")
	}
}

func TestOpenReply(t *testing.T) {
	for _, code := range []OpenError{OpenOK, OpenRefused, OpenDNS, OpenTimeout, OpenNotSupported, OpenTooMany} {
		var buf bytes.Buffer
		if err := WriteOpenReply(&buf, code); err != nil {
			t.Fatal(err)
		}
		got, err := ReadOpenReply(&buf)
		if err != nil || got != code {
			t.Fatalf("reply %d -> %d %v", code, got, err)
		}
	}
	if _, err := ReadOpenReply(bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Fatal("short reply accepted")
	}
}

func socks5Request(atyp byte, addr []byte, port uint16) []byte {
	req := []byte{socks5Version, socks5Connect, 0, atyp}
	if atyp == AtypDomain {
		req = append(req, byte(len(addr)))
	}
	req = append(req, addr...)
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, port)
	return append(req, pb...)
}

func TestParseEntrySOCKS5(t *testing.T) {
	cases := []struct {
		name string
		atyp byte
		addr []byte
		port uint16
		want string
	}{
		{"domain", AtypDomain, []byte("example.com"), 443, "example.com:443"},
		{"ipv4", AtypIPv4, []byte{93, 184, 216, 34}, 80, "93.184.216.34:80"},
		{"ipv6", AtypIPv6, net.ParseIP("::1").To16(), 8080, "[::1]:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := append([]byte{socks5Version, 1, 0}, socks5Request(tc.atyp, tc.addr, tc.port)...)
			br := bufio.NewReader(bytes.NewReader(raw))
			var out bytes.Buffer
			e, err := ParseEntry(br, &out)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), []byte{socks5Version, 0}) {
				t.Fatalf("greeting reply %v", out.Bytes())
			}
			if e.Kind != "socks5" || e.Target.Address() != tc.want {
				t.Fatalf("entry %+v", e)
			}
			if got := AcceptReply(e, OpenOK); len(got) != 10 || got[1] != 0 {
				t.Fatalf("success reply %v", got)
			}
			if got := AcceptReply(e, OpenRefused); got[1] != 0x05 {
				t.Fatalf("refused reply %v", got)
			}
		})
	}
}

func TestParseEntrySOCKS5Rejects(t *testing.T) {
	// No no-auth method: server must answer 0xFF and fail.
	raw := []byte{socks5Version, 1, 0x02, socks5Version, socks5Connect, 0, AtypDomain, 1, 'a', 0, 80}
	var out bytes.Buffer
	if _, err := ParseEntry(bufio.NewReader(bytes.NewReader(raw)), &out); err == nil {
		t.Fatal("no-auth-less greeting accepted")
	}
	if !bytes.Equal(out.Bytes(), []byte{socks5Version, 0xFF}) {
		t.Fatalf("expected 0xFF reply, got %v", out.Bytes())
	}
	// UDP ASSOCIATE is not supported.
	raw = []byte{socks5Version, 1, 0, socks5Version, 0x03, 0, AtypDomain, 1, 'a', 0, 80}
	if _, err := ParseEntry(bufio.NewReader(bytes.NewReader(raw)), &out); err == nil {
		t.Fatal("UDP ASSOCIATE accepted")
	}
}

func TestParseEntryHTTPConnect(t *testing.T) {
	raw := []byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Connection: keep-alive\r\n\r\n")
	e, err := ParseEntry(bufio.NewReader(bytes.NewReader(raw)), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != "connect" || e.Target.Address() != "example.com:443" {
		t.Fatalf("entry %+v", e)
	}
	if len(e.Prefix) != 0 {
		t.Fatalf("CONNECT should have no prefix, got %q", e.Prefix)
	}
	if got := string(AcceptReply(e, OpenOK)); got != httpOK {
		t.Fatalf("reply %q", got)
	}
	if got := string(AcceptReply(e, OpenRefused)); !strings.Contains(got, "502") {
		t.Fatalf("failure reply %q", got)
	}
}

func TestParseEntryAbsoluteForm(t *testing.T) {
	raw := []byte("GET http://example.com:8080/a/b?q=1 HTTP/1.1\r\n" +
		"Host: example.com:8080\r\nProxy-Connection: keep-alive\r\nX-Keep: yes\r\n\r\n")
	e, err := ParseEntry(bufio.NewReader(bytes.NewReader(raw)), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != "forward" || e.Target.Address() != "example.com:8080" {
		t.Fatalf("entry %+v", e)
	}
	head := string(e.Prefix)
	if !strings.HasPrefix(head, "GET /a/b?q=1 HTTP/1.1\r\n") {
		t.Fatalf("not origin-form: %q", head)
	}
	if strings.Contains(head, "http://") || strings.Contains(head, "Proxy-Connection") ||
		!strings.Contains(head, "X-Keep: yes") || !strings.Contains(head, "Connection: close") ||
		!strings.HasSuffix(head, "\r\n\r\n") {
		t.Fatalf("rewrite wrong: %q", head)
	}
	if AcceptReply(e, OpenOK) != nil {
		t.Fatal("forward success must not emit a local reply")
	}
	if got := string(AcceptReply(e, OpenRefused)); !strings.Contains(got, "502") {
		t.Fatalf("failure reply %q", got)
	}
}

func TestParseEntryHTTPRejects(t *testing.T) {
	cases := [][]byte{
		[]byte("TRACE / HTTP/1.1\r\n\r\n"),
		[]byte("CONNECT bad-authority HTTP/1.1\r\n\r\n"),
		[]byte("GET https://example.com/ HTTP/1.1\r\n\r\n"),
		[]byte("GET /relative HTTP/1.1\r\n\r\n"),
		[]byte("GET http://example.com/ HTTP/2\r\n\r\n"),
	}
	for i, raw := range cases {
		if _, err := ParseEntry(bufio.NewReader(bytes.NewReader(raw)), &bytes.Buffer{}); err == nil {
			t.Fatalf("case %d accepted: %q", i, raw)
		}
	}
}

func TestParseAuthority(t *testing.T) {
	cases := []struct {
		in, host string
		port     int
		def      int
	}{
		{"example.com", "example.com", 80, 80},
		{"example.com:8443", "example.com", 8443, 80},
		{"[2001:db8::1]:443", "2001:db8::1", 443, 80},
		{"[2001:db8::1]", "2001:db8::1", 80, 80},
		{"127.0.0.1:1080", "127.0.0.1", 1080, 0},
	}
	for _, tc := range cases {
		h, p, err := parseAuthority(tc.in, tc.def)
		if err != nil || h != tc.host || p != tc.port {
			t.Fatalf("%q -> %q %d (want %q %d): %v", tc.in, h, p, tc.host, tc.port, err)
		}
	}
	for _, bad := range []string{"", "a:b:c", "bad host:80", "example.com:0", "[::1]x:80"} {
		if _, _, err := parseAuthority(bad, 80); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestNewTarget(t *testing.T) {
	if got, err := NewTarget("127.0.0.1", 1080); err != nil || got.Atyp != AtypIPv4 || len(got.Addr) != 4 {
		t.Fatalf("ipv4: %+v %v", got, err)
	}
	if got, err := NewTarget("::1", 1080); err != nil || got.Atyp != AtypIPv6 || len(got.Addr) != 16 {
		t.Fatalf("ipv6: %+v %v", got, err)
	}
	if got, err := NewTarget("a-b_c.example", 80); err != nil || got.Atyp != AtypDomain {
		t.Fatalf("domain: %+v %v", got, err)
	}
	for _, bad := range []string{"", "bad host", "a/b"} {
		if _, err := NewTarget(bad, 80); err == nil {
			t.Fatalf("bad host %q accepted", bad)
		}
	}
	if _, err := NewTarget("example.com", 0); err == nil {
		t.Fatal("port 0 accepted")
	}
}

func TestParseEntryAbsoluteFormQueryOnly(t *testing.T) {
	raw := []byte("GET http://example.com?x=1 HTTP/1.1\r\nHost: example.com\r\n\r\n")
	e, err := ParseEntry(bufio.NewReader(bytes.NewReader(raw)), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Target.Address() != "example.com:80" {
		t.Fatalf("target %q", e.Target.Address())
	}
	if !strings.HasPrefix(string(e.Prefix), "GET /?x=1 HTTP/1.1\r\n") {
		t.Fatalf("query-only URI lost: %q", e.Prefix)
	}
}

// The forward path relays body bytes verbatim, so the chunked framing headers
// must survive the rewrite; dropping Transfer-Encoding would leave the origin
// parsing chunks as a pipelined request.
func TestParseEntryAbsoluteFormKeepsChunkedFraming(t *testing.T) {
	raw := []byte("POST http://example.com/up HTTP/1.1\r\n" +
		"Host: example.com\r\nContent-Length: 5\r\nTransfer-Encoding: chunked\r\n" +
		"Trailer: X-Sum\r\n\r\n5\r\nhello\r\n0\r\nX-Sum: 1\r\n\r\n")
	e, err := ParseEntry(bufio.NewReader(bytes.NewReader(raw)), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	head := string(e.Prefix)
	if !strings.Contains(head, "Transfer-Encoding: chunked") || !strings.Contains(head, "Trailer: X-Sum") {
		t.Fatalf("framing headers stripped: %q", head)
	}
	if strings.Contains(head, "Content-Length") {
		t.Fatalf("Content-Length kept beside Transfer-Encoding: %q", head)
	}
}

// After the greeting, a rejected CONNECT must be answered with a SOCKS5 reply
// so the client fails fast instead of waiting for a reset.
func TestParseEntrySocks5FailureReply(t *testing.T) {
	raw := []byte{socks5Version, 1, 0, socks5Version, 0x03, 0, AtypDomain, 1, 'a', 0, 80}
	var out bytes.Buffer
	if _, err := ParseEntry(bufio.NewReader(bytes.NewReader(raw)), &out); err == nil {
		t.Fatal("UDP ASSOCIATE accepted")
	}
	reply := out.Bytes()
	if len(reply) != 12 { // 2-byte greeting + 10-byte CONNECT failure
		t.Fatalf("expected greeting + failure reply, got %v", reply)
	}
	if reply[0] != socks5Version || reply[1] != 0x00 || reply[2] != socks5Version || reply[3] != 0x01 {
		t.Fatalf("bad reply %v", reply)
	}
}

// A client that never sends a newline must hit the head limit, not grow the
// read buffer until the process runs out of memory.
func TestParseEntryHeadLimitOnEndlessLine(t *testing.T) {
	raw := bytes.Repeat([]byte("A"), 4<<20)
	_, err := ParseEntry(bufio.NewReaderSize(bytes.NewReader(raw), 64<<10), &bytes.Buffer{})
	if !errors.Is(err, ErrHeadTooLarge) {
		t.Fatalf("endless line: got %v, want ErrHeadTooLarge", err)
	}
}
