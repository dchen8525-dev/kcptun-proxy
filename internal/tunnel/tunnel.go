// Package tunnel is the KCP + smux transport shared by both executables.
//
// One KCP session per process is multiplexed by smux v2: every local TCP
// connection becomes one stream, whose first bytes are a SOCKS OPEN frame
// carrying the target address. This is the same shape as kcptun, with the
// kcptun tuning flags pinned to the fast3 profile (docs/InternalProfile.md).
package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"

	"kcptun-proxy/internal/logx"
	"kcptun-proxy/internal/socks"
	"kcptun-proxy/internal/stats"
)

// InternalProfile is fixed on both ends. Exposing these as flags would give
// the user 15 ways to make the two ends disagree.
const (
	KCPNoDelay  = 1
	KCPInterval = 10 // ms
	KCPResend   = 2
	KCPNC       = 1
	KCPSndWnd   = 1024
	KCPRcvWnd   = 1024
	KCPDSCP     = 46
	SocketBuf   = 4 << 20
	SmuxFrame   = 8192
	SmuxSession = 16 << 20
	SmuxStream  = 4 << 20
	OpenTimeout = 15 * time.Second
	DialTimeout = 10 * time.Second
)

type Options struct {
	Key          []byte // 16 bytes, AES-128-GCM
	KCPMTU       int    // UDP payload (datagram budget minus 28)
	DataShards   int
	ParityShards int
	Log          *logx.Logger
	Stats        *stats.Reporter
	Trace        *Trace
}

func (o Options) shards() (int, int) {
	if o.ParityShards == 0 {
		return 0, 0
	}
	return o.DataShards, o.ParityShards
}

func (o Options) block() (kcp.BlockCrypt, error) {
	return kcp.NewAESGCMCrypt(o.Key)
}

func RandomKey() ([]byte, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func KeyHex(b []byte) string { return hex.EncodeToString(b) }

func smuxConfig() *smux.Config {
	c := smux.DefaultConfig()
	c.Version = 2
	c.KeepAliveInterval = 10 * time.Second
	c.KeepAliveTimeout = 30 * time.Second
	c.MaxFrameSize = SmuxFrame
	c.MaxReceiveBuffer = SmuxSession
	c.MaxStreamBuffer = SmuxStream
	return c
}

func tune(s *kcp.UDPSession, mtu int) {
	s.SetStreamMode(true)
	s.SetWriteDelay(false)
	s.SetACKNoDelay(true)
	s.SetNoDelay(KCPNoDelay, KCPInterval, KCPResend, KCPNC)
	s.SetWindowSize(KCPSndWnd, KCPRcvWnd)
	s.SetMtu(mtu)
	// Accepted sessions cannot set DSCP on the shared listener socket; the
	// listener sets it once instead. Failure here is expected, not an error.
	_ = s.SetDSCP(KCPDSCP)
	_ = s.SetReadBuffer(SocketBuf)
	_ = s.SetWriteBuffer(SocketBuf)
}

func halfCloseWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// Relay copies in both directions. counted.Write is tx (bytes put on the
// tunnel), counted.Read is rx (bytes taken off it). Both executables use this
// same function, so their stats lines describe the same two directions.
func Relay(plain net.Conn, stream net.Conn, rep *stats.Reporter) {
	counted := stats.Wrap(stream, rep)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(counted, plain)
		_ = counted.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(plain, counted)
		halfCloseWrite(plain)
	}()
	wg.Wait()
}

// --------------------------------------------------------------- server ----

type Server struct {
	ln       *kcp.Listener
	o        Options
	sessions atomic.Int64
}

func Listen(addr string, o Options) (*Server, error) {
	block, err := o.block()
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	d, p := o.shards()
	ln, err := kcp.ListenWithOptions(addr, block, d, p)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	_ = ln.SetReadBuffer(SocketBuf)
	_ = ln.SetWriteBuffer(SocketBuf)
	_ = ln.SetDSCP(KCPDSCP)
	o.Log.Info("server: KCP listening on %s (fec=%d:%d mtu=%d)", ln.Addr(), d, p, o.KCPMTU+28)
	return &Server{ln: ln, o: o}, nil
}

func (s *Server) Addr() net.Addr { return s.ln.Addr() }
func (s *Server) Close() error   { return s.ln.Close() }

func (s *Server) Serve() error {
	for {
		conn, err := s.ln.AcceptKCP()
		if err != nil {
			return err
		}
		tune(conn, s.o.KCPMTU)
		n := s.sessions.Add(1)
		s.o.Stats.SetTunnels(int(n))
		s.o.Stats.SetUp(int(n))
		s.o.Trace.Event("session_open", map[string]any{"remote": conn.RemoteAddr().String(), "sessions": n})
		go s.serveSession(conn)
	}
}

func (s *Server) serveSession(conn *kcp.UDPSession) {
	defer func() {
		_ = conn.Close()
		n := s.sessions.Add(-1)
		s.o.Stats.SetTunnels(int(n))
		s.o.Stats.SetUp(int(n))
		s.o.Trace.Event("session_close", map[string]any{"sessions": n})
	}()
	sess, err := smux.Server(conn, smuxConfig())
	if err != nil {
		s.o.Log.Error("smux server: %v", err)
		return
	}
	defer sess.Close()
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			s.o.Log.Debug("accept stream: %v", err)
			return
		}
		go s.serveStream(st)
	}
}

func classifyDialError(err error) socks.OpenError {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return socks.OpenTimeout
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return socks.OpenDNS
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return socks.OpenRefused
	}
	return socks.OpenGeneral
}

func (s *Server) serveStream(st *smux.Stream) {
	defer st.Close()
	_ = st.SetReadDeadline(time.Now().Add(OpenTimeout))
	t, err := socks.ReadOpen(st)
	if err != nil {
		s.o.Log.Warn("bad OPEN: %v", err)
		return
	}
	_ = st.SetReadDeadline(time.Time{})

	remote, err := net.DialTimeout("tcp", t.Address(), DialTimeout)
	if err != nil {
		code := classifyDialError(err)
		s.o.Log.Debug("dial %s failed: %v (code %d)", t, err, code)
		_ = socks.WriteOpenReply(st, code)
		s.o.Trace.Event("open_error", map[string]any{"target": t.String(), "code": int(code)})
		return
	}
	defer remote.Close()
	if err := socks.WriteOpenReply(st, socks.OpenOK); err != nil {
		return
	}
	s.o.Stats.AddLive(1)
	defer s.o.Stats.AddLive(-1)
	s.o.Trace.Event("stream_open", map[string]any{"target": t.String()})
	s.o.Log.Debug("open %s", t)
	Relay(remote, st, s.o.Stats)
	s.o.Trace.Event("stream_close", map[string]any{"target": t.String()})
}

// --------------------------------------------------------------- client ----

type Client struct {
	peer  string
	o     Options
	mu    sync.Mutex
	gen   atomic.Uint64
	sess  *smux.Session
	kconn *kcp.UDPSession
}

func NewClient(peer string, o Options) *Client { return &Client{peer: peer, o: o} }

func (c *Client) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Invalidate any CloseChan watcher belonging to the session being torn
	// down; otherwise it can zero the counters of a freshly dialed successor.
	c.gen.Add(1)
	if c.sess != nil {
		_ = c.sess.Close()
	}
	if c.kconn != nil {
		_ = c.kconn.Close()
	}
	c.sess, c.kconn = nil, nil
	c.o.Stats.SetUp(0)
	c.o.Stats.SetTunnels(0)
}

func (c *Client) session() (*smux.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != nil && !c.sess.IsClosed() {
		return c.sess, nil
	}
	if c.sess != nil {
		_ = c.sess.Close()
		_ = c.kconn.Close()
		c.sess, c.kconn = nil, nil
	}
	block, err := c.o.block()
	if err != nil {
		return nil, err
	}
	d, p := c.o.shards()
	conn, err := kcp.DialWithOptions(c.peer, block, d, p)
	if err != nil {
		return nil, fmt.Errorf("dial KCP %s: %w", c.peer, err)
	}
	tune(conn, c.o.KCPMTU)
	sess, err := smux.Client(conn, smuxConfig())
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("smux client: %w", err)
	}
	c.sess, c.kconn = sess, conn
	gen := c.gen.Add(1)
	c.o.Stats.SetUp(1)
	c.o.Stats.SetTunnels(1)
	c.o.Trace.Event("session_open", map[string]any{"peer": c.peer, "fec_d": d, "fec_p": p})
	boundSession := c.sess
	go func() {
		<-boundSession.CloseChan()
		if c.gen.Load() == gen {
			c.o.Stats.SetUp(0)
			c.o.Stats.SetTunnels(0)
		}
		c.o.Trace.Event("session_close", map[string]any{"peer": c.peer})
	}()
	return sess, nil
}

func (c *Client) Close() { c.reset() }

// Open returns the stream and the server's OPEN result. A stale smux session
// is retried exactly once with a fresh KCP dial.
func (c *Client) Open(t socks.Target) (net.Conn, socks.OpenError, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		sess, err := c.session()
		if err != nil {
			last = err
			c.reset()
			continue
		}
		st, err := sess.OpenStream()
		if err != nil {
			last = err
			c.reset()
			continue
		}
		_ = st.SetDeadline(time.Now().Add(OpenTimeout))
		if err := socks.WriteOpen(st, t); err != nil {
			last = err
			st.Close()
			c.reset()
			continue
		}
		code, err := socks.ReadOpenReply(st)
		if err != nil {
			last = err
			st.Close()
			c.reset()
			continue
		}
		_ = st.SetDeadline(time.Time{})
		c.o.Trace.Event("stream_open", map[string]any{"target": t.String(), "code": int(code)})
		if code != socks.OpenOK {
			st.Close()
			return nil, code, nil
		}
		return st, socks.OpenOK, nil
	}
	if last == nil {
		last = errors.New("could not establish a KCP session")
	}
	return nil, socks.OpenGeneral, last
}
