package stats

import (
	"bytes"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var lineRE = regexp.MustCompile(`^stats ([0-9]+\.[0-9])s live=(\d+) up=(\d+) tunnels=(\d+) tx=(\d+)B rx=(\d+)B tx_rate=(\d+)B/s rx_rate=(\d+)B/s$`)

func TestLineFormat(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	r.AddTx(1234)
	r.AddRx(5678)
	r.AddLive(2)
	r.SetUp(1)
	r.SetTunnels(1)
	line := r.Line()
	m := lineRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("unparsable stats line %q", line)
	}
	if m[5] != "1234" || m[6] != "5678" || m[2] != "2" || m[3] != "1" || m[4] != "1" {
		t.Fatalf("wrong fields: %v", m)
	}
	if _, err := strconv.ParseFloat(m[1], 64); err != nil {
		t.Fatal(err)
	}
}

func TestUptimeMonotonicAndRates(t *testing.T) {
	r := New(io.Discard)
	r.AddTx(1000)
	first := r.Line()
	time.Sleep(20 * time.Millisecond)
	second := r.Line()
	a := lineRE.FindStringSubmatch(first)
	b := lineRE.FindStringSubmatch(second)
	if a == nil || b == nil {
		t.Fatalf("lines %q / %q", first, second)
	}
	ua, _ := strconv.ParseFloat(a[1], 64)
	ub, _ := strconv.ParseFloat(b[1], 64)
	if ub < ua {
		t.Fatalf("uptime went backwards: %v -> %v", ua, ub)
	}
	rate, _ := strconv.ParseInt(b[7], 10, 64)
	if rate != 0 {
		t.Fatalf("no new bytes but tx_rate=%d", rate)
	}
}

func TestAddLiveNeverNegative(t *testing.T) {
	r := New(io.Discard)
	r.AddLive(-1)
	if !strings.Contains(r.Line(), "live=0") {
		t.Fatalf("live went negative: %s", r.Line())
	}
	r.AddLive(3)
	r.AddLive(-1)
	if !strings.Contains(r.Line(), "live=2") {
		t.Fatalf("live=%s", r.Line())
	}
}

func TestStartStop(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	r.Start()
	time.Sleep(600 * time.Millisecond)
	r.Stop()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 1 {
		t.Fatal("ticker emitted no stats line")
	}
	for _, l := range lines {
		if lineRE.FindStringSubmatch(l) == nil {
			t.Fatalf("bad line %q", l)
		}
	}
	before := buf.Len()
	time.Sleep(600 * time.Millisecond)
	if buf.Len() != before {
		t.Fatal("stats still emitted after Stop()")
	}
}

func TestWrapCountsBothDirections(t *testing.T) {
	r := New(io.Discard)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	w := Wrap(a, r)

	// tx: Write on the wrapped conn.
	readDone := make(chan struct{})
	go func() {
		_, _ = io.ReadFull(b, make([]byte, 2))
		close(readDone)
	}()
	if _, err := w.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	<-readDone

	// rx: Read on the wrapped conn.
	go func() { _, _ = b.Write([]byte("ok")) }()
	buf := make([]byte, 2)
	if _, err := io.ReadFull(w, buf); err != nil {
		t.Fatal(err)
	}
	tx, rx, _, _ := r.Counters()
	if tx != 2 || rx != 2 {
		t.Fatalf("tx=%d rx=%d", tx, rx)
	}
	if err := w.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
}
