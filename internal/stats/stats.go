// Package stats emits the same machine-readable line FCP's CLI emits, on
// stdout, every 500ms, independent of --log-level.
//
//	stats 12.4s live=1 up=1 tunnels=1 tx=123B rx=456B tx_rate=1B/s rx_rate=2B/s
package stats

import (
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const Period = 500 * time.Millisecond

type Reporter struct {
	tx       atomic.Int64
	rx       atomic.Int64
	live     atomic.Int64
	up       atomic.Int64
	tunnels  atomic.Int64
	started  time.Time
	out      io.Writer
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	mu     sync.Mutex
	prevTx int64
	prevRx int64
	prevAt time.Time
}

func New(out io.Writer) *Reporter {
	if out == nil {
		out = os.Stdout
	}
	return &Reporter{started: time.Now(), out: out, prevAt: time.Now(), stopCh: make(chan struct{})}
}

func (r *Reporter) AddTx(n int) { r.tx.Add(int64(n)) }
func (r *Reporter) AddRx(n int) { r.rx.Add(int64(n)) }
func (r *Reporter) AddLive(d int) {
	v := r.live.Add(int64(d))
	if v < 0 {
		r.live.Store(0)
	}
}
func (r *Reporter) SetUp(n int)      { r.up.Store(int64(n)) }
func (r *Reporter) SetTunnels(n int) { r.tunnels.Store(int64(n)) }

func (r *Reporter) Counters() (tx, rx, live int64, uptime time.Duration) {
	return r.tx.Load(), r.rx.Load(), r.live.Load(), time.Since(r.started)
}

// Line computes one sample and advances the rate baseline. Safe to call from
// tests without starting the ticker.
func (r *Reporter) Line() string {
	now := time.Now()
	r.mu.Lock()
	dt := now.Sub(r.prevAt).Seconds()
	if dt <= 0 {
		dt = Period.Seconds()
	}
	tx, rx := r.tx.Load(), r.rx.Load()
	txRate := int64(float64(tx-r.prevTx) / dt)
	rxRate := int64(float64(rx-r.prevRx) / dt)
	r.prevTx, r.prevRx, r.prevAt = tx, rx, now
	r.mu.Unlock()

	return fmt.Sprintf("stats %.1fs live=%d up=%d tunnels=%d tx=%dB rx=%dB tx_rate=%dB/s rx_rate=%dB/s",
		time.Since(r.started).Seconds(), r.live.Load(), r.up.Load(), r.tunnels.Load(),
		tx, rx, txRate, rxRate)
}

func (r *Reporter) Start() {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(Period)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				_, _ = io.WriteString(r.out, r.Line()+"\n")
			case <-r.stopCh:
				return
			}
		}
	}()
}

func (r *Reporter) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.wg.Wait()
}

// CountedConn wraps a tunnel stream so both relay directions feed the same
// counters without the caller having to instrument io.Copy.
type CountedConn struct {
	net.Conn
	rep *Reporter
}

func Wrap(c net.Conn, rep *Reporter) *CountedConn { return &CountedConn{Conn: c, rep: rep} }

func (c *CountedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.rep.AddRx(n)
	return n, err
}

func (c *CountedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.rep.AddTx(n)
	return n, err
}

// CloseWrite is needed for a clean half-close; smux.Stream implements it.
func (c *CountedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}
