// Package tests drives the exact production path: KCP listener + smux client +
// local proxy accept loop, all in-process. No external binaries, no network.
package tests

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"kcptun-proxy/internal/logx"
	"kcptun-proxy/internal/proxy"
	"kcptun-proxy/internal/stats"
	"kcptun-proxy/internal/tunnel"
)

type stack struct {
	srv *tunnel.Server
	cl  *tunnel.Client
	pl  *proxy.Server
	rep *stats.Reporter
}

func newStack(t *testing.T, parity int) *stack {
	t.Helper()
	key, err := tunnel.RandomKey()
	if err != nil {
		t.Fatal(err)
	}
	log, err := logx.New("off", "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	rep := stats.New(io.Discard)
	o := tunnel.Options{
		Key: key, KCPMTU: 1372, DataShards: 10, ParityShards: parity,
		Log: log, Stats: rep,
	}
	srv, err := tunnel.Listen("127.0.0.1:0", o)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	cl := tunnel.NewClient(srv.Addr().String(), o)
	pl, err := proxy.Listen("127.0.0.1:0", cl, rep, log, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = pl.Serve() }()
	return &stack{srv: srv, cl: cl, pl: pl, rep: rep}
}

func (s *stack) close() {
	_ = s.pl.Close()
	s.cl.Close()
	_ = s.srv.Close()
}

func socks5Dial(proxyAddr, target string) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (net.Conn, error) { _ = c.Close(); return nil, err }
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return fail(err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(c, greeting); err != nil {
		return fail(err)
	}
	if greeting[0] != 5 || greeting[1] != 0 {
		return fail(fmt.Errorf("bad SOCKS5 greeting reply %v", greeting))
	}
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return fail(err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return fail(err)
	}
	req := []byte{5, 1, 0, 3, byte(len(host))}
	req = append(req, host...)
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, uint16(p))
	req = append(req, pb...)
	if _, err := c.Write(req); err != nil {
		return fail(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil {
		return fail(err)
	}
	if reply[1] != 0 {
		return fail(fmt.Errorf("SOCKS5 CONNECT refused with rep=0x%02x", reply[1]))
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func readHead(br *bufio.Reader) (string, error) {
	var b bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}
		b.WriteString(line)
		if line == "\r\n" || line == "\n" {
			return b.String(), nil
		}
		if b.Len() > 64<<10 {
			return "", fmt.Errorf("head too large")
		}
	}
}

func newOrigin(t *testing.T, payload []byte) (*httptest.Server, string) {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Seen-Path", r.URL.Path)
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

func TestHTTPOverSOCKS5(t *testing.T) {
	want := []byte("hello-kcp")
	origin, host := newOrigin(t, want)
	st := newStack(t, 3)
	defer st.close()

	c, err := socks5Dial(st.pl.Addr().String(), host)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET /socks HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !bytes.Equal(body, want) || resp.Header.Get("X-Seen-Path") != "/socks" {
		t.Fatalf("status=%d body=%q path=%q", resp.StatusCode, body, resp.Header.Get("X-Seen-Path"))
	}
	_ = origin
	tx, rx, _, _ := st.rep.Counters()
	if tx == 0 || rx == 0 {
		t.Fatalf("stats did not count traffic: tx=%d rx=%d", tx, rx)
	}
	if line := st.rep.Line(); !strings.HasPrefix(line, "stats ") || !strings.Contains(line, "tx=") {
		t.Fatalf("bad stats line %q", line)
	}
}

func TestHTTPConnect(t *testing.T) {
	want := []byte("connect-ok")
	_, host := newOrigin(t, want)
	st := newStack(t, 3)
	defer st.close()

	c, err := net.DialTimeout("tcp", st.pl.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
	br := bufio.NewReader(c)
	head, err := readHead(br)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(head, "HTTP/1.1 200") {
		t.Fatalf("CONNECT reply %q", head)
	}
	fmt.Fprintf(c, "GET /tunnel HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !bytes.Equal(body, want) {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

func TestAbsoluteFormHTTP(t *testing.T) {
	want := []byte("absolute-ok")
	_, host := newOrigin(t, want)
	st := newStack(t, 3)
	defer st.close()

	c, err := net.DialTimeout("tcp", st.pl.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(c, "GET http://%s/abs?q=1 HTTP/1.1\r\nHost: %s\r\nProxy-Connection: keep-alive\r\nConnection: close\r\n\r\n", host, host)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !bytes.Equal(body, want) || resp.Header.Get("X-Seen-Path") != "/abs" {
		t.Fatalf("status=%d body=%q path=%q", resp.StatusCode, body, resp.Header.Get("X-Seen-Path"))
	}
}

func TestLargePayloadAndFECVariants(t *testing.T) {
	block := bytes.Repeat([]byte("0123456789abcdef"), 64) // 1 KiB
	payload := bytes.Repeat(block, 2048)                  // 2 MiB
	wantSum := sha256.Sum256(payload)
	for _, parity := range []int{3, 0} {
		t.Run(fmt.Sprintf("parity=%d", parity), func(t *testing.T) {
			_, host := newOrigin(t, payload)
			st := newStack(t, parity)
			defer st.close()
			c, err := socks5Dial(st.pl.Addr().String(), host)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(60 * time.Second))
			fmt.Fprintf(c, "GET /big HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
			resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "GET"})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if got := sha256.Sum256(body); got != wantSum || len(body) != len(payload) {
				t.Fatalf("payload mismatch: len=%d want=%d", len(body), len(payload))
			}
		})
	}
}

func TestOpenFailurePropagatesToSOCKS5(t *testing.T) {
	st := newStack(t, 3)
	defer st.close()
	// Port 1 on loopback is not listening; dial must be refused and the client
	// must answer the local SOCKS5 client with a failure reply, not a timeout.
	_, err := socks5Dial(st.pl.Addr().String(), "127.0.0.1:1")
	if err == nil {
		t.Fatal("OPEN to a closed port succeeded")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("unexpected failure %v", err)
	}
}

func TestInProcessSpeedtest(t *testing.T) {
	key, err := tunnel.RandomKey()
	if err != nil {
		t.Fatal(err)
	}
	log, _ := logx.New("off", "", io.Discard)
	o := tunnel.Options{Key: key, KCPMTU: 1372, DataShards: 10, ParityShards: 3,
		Log: log, Stats: stats.New(io.Discard)}
	var lines []string
	if err := tunnel.Speedtest(3, o, func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "up=") || !strings.Contains(joined, "down=") ||
		!strings.Contains(joined, "tx=") {
		t.Fatalf("speedtest output %q", joined)
	}
}
