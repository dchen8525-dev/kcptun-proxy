// Package proxy is the client's local TCP entry. Keeping it out of cmd/ lets
// the end-to-end test drive the exact same accept loop the executable uses.
package proxy

import (
	"bufio"
	"io"
	"net"
	"time"

	"kcptun-proxy/internal/logx"
	"kcptun-proxy/internal/socks"
	"kcptun-proxy/internal/stats"
	"kcptun-proxy/internal/tunnel"
)

// bufferedConn keeps the bytes ParseEntry already pulled off the socket. The
// relay MUST read from br, not from the raw conn, or the first request body /
// TLS ClientHello bytes would be lost.
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.br.Read(p) }
func (c *bufferedConn) CloseWrite() error {
	if tc, ok := c.Conn.(*net.TCPConn); ok {
		return tc.CloseWrite()
	}
	return c.Conn.Close()
}

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

type Server struct {
	ln  net.Listener
	cl  *tunnel.Client
	rep *stats.Reporter
	log *logx.Logger
	tr  *tunnel.Trace
}

func Listen(addr string, cl *tunnel.Client, rep *stats.Reporter, log *logx.Logger, tr *tunnel.Trace) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	// The local entry has no authentication at all, so a non-loopback bind
	// turns this machine into an open proxy for anyone who can reach the port.
	if ta, ok := ln.Addr().(*net.TCPAddr); ok && !ta.IP.IsLoopback() {
		log.Warn("proxy: listening on %s (not loopback); the local entry speaks unauthenticated SOCKS5/HTTP and is reachable by anyone who can reach this address", ln.Addr())
	}
	return &Server{ln: ln, cl: cl, rep: rep, log: log, tr: tr}, nil
}

func (s *Server) Addr() net.Addr { return s.ln.Addr() }
func (s *Server) Close() error   { return s.ln.Close() }

func (s *Server) Serve() error {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	s.rep.AddLive(1)
	defer s.rep.AddLive(-1)
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))

	br := bufio.NewReaderSize(c, 64<<10)
	entry, err := socks.ParseEntry(br, c)
	if err != nil {
		s.log.Warn("local request from %s: %v", c.RemoteAddr(), err)
		return
	}

	st, code, err := s.cl.Open(entry.Target)
	if err != nil {
		s.log.Warn("open %s: %v", entry.Target, err)
		code = socks.OpenGeneral
	}
	if st == nil {
		if reply := socks.AcceptReply(entry, code); len(reply) > 0 {
			_ = writeAll(c, reply)
		}
		return
	}
	defer st.Close()
	_ = c.SetDeadline(time.Time{})

	if reply := socks.AcceptReply(entry, socks.OpenOK); len(reply) > 0 {
		if err := writeAll(c, reply); err != nil {
			return
		}
	}
	if len(entry.Prefix) > 0 {
		if err := writeAll(st, entry.Prefix); err != nil {
			return
		}
	}
	s.tr.Event("local_open", map[string]any{"kind": entry.Kind, "target": entry.Target.String()})
	tunnel.Relay(&bufferedConn{Conn: c, br: br}, st, s.rep)
	s.tr.Event("local_close", map[string]any{"kind": entry.Kind, "target": entry.Target.String()})
}
