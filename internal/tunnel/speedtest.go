package tunnel

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
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

func echoStream(st *smux.Stream) {
	defer st.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := st.Read(buf)
		if n > 0 {
			if writeAll(st, buf[:n]) != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Speedtest runs a real KCP + FEC + smux loopback in this process. It needs
// no server, no --peer and no pre-shared key, and it exercises exactly the
// same tune()/smuxConfig() path the real tunnel uses.
func Speedtest(seconds int, o Options, emit func(string)) error {
	if o.Key == nil {
		k, err := RandomKey()
		if err != nil {
			return err
		}
		o.Key = k
		emit(fmt.Sprintf("speedtest: generated loopback key %s", KeyHex(k)))
	}
	emit(fmt.Sprintf("speedtest: starting %ds in-process loopback (fec=%d:%d mtu=%d)",
		seconds, o.DataShards, o.ParityShards, o.KCPMTU+28))

	serverBlock, err := o.block()
	if err != nil {
		return err
	}
	d, p := o.shards()
	ln, err := kcp.ListenWithOptions("127.0.0.1:0", serverBlock, d, p)
	if err != nil {
		return fmt.Errorf("loopback listen: %w", err)
	}
	defer ln.Close()
	_ = ln.SetReadBuffer(SocketBuf)
	_ = ln.SetWriteBuffer(SocketBuf)
	addr := ln.Addr().String()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.AcceptKCP()
		if err != nil {
			serverErr <- err
			return
		}
		tune(conn, o.KCPMTU)
		sess, err := smux.Server(conn, smuxConfig())
		if err != nil {
			serverErr <- err
			return
		}
		defer sess.Close()
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go echoStream(st)
		}
	}()

	clientBlock, err := o.block()
	if err != nil {
		return err
	}
	conn, err := kcp.DialWithOptions(addr, clientBlock, d, p)
	if err != nil {
		return fmt.Errorf("loopback dial: %w", err)
	}
	defer conn.Close()
	tune(conn, o.KCPMTU)
	sess, err := smux.Client(conn, smuxConfig())
	if err != nil {
		return fmt.Errorf("loopback smux: %w", err)
	}
	defer sess.Close()

	const streams = 4
	payload := make([]byte, 32<<10)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	start := time.Now()
	deadline := start.Add(time.Duration(seconds) * time.Second)
	var tx, rx atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		st, err := sess.OpenStream()
		if err != nil {
			return fmt.Errorf("open stream %d: %w", i, err)
		}
		wg.Add(2)
		go func(st *smux.Stream) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				n, err := st.Write(payload)
				tx.Add(int64(n))
				if err != nil {
					return
				}
			}
			_ = st.CloseWrite()
		}(st)
		go func(st *smux.Stream) {
			defer wg.Done()
			defer st.Close()
			_ = st.SetReadDeadline(deadline.Add(5 * time.Second))
			buf := make([]byte, 32<<10)
			for {
				n, err := st.Read(buf)
				rx.Add(int64(n))
				if err != nil {
					return
				}
			}
		}(st)
	}
	wg.Wait()
	elapsed := time.Since(start)

	select {
	case err := <-serverErr:
		return fmt.Errorf("loopback server: %w", err)
	default:
	}

	up := float64(tx.Load()) * 8 / elapsed.Seconds() / 1e6
	down := float64(rx.Load()) * 8 / elapsed.Seconds() / 1e6
	emit(fmt.Sprintf("speedtest %.1fs streams=%d tx=%dB rx=%dB up=%.2fMbit/s down=%.2fMbit/s",
		elapsed.Seconds(), streams, tx.Load(), rx.Load(), up, down))
	if tx.Load() == 0 || rx.Load() == 0 {
		return fmt.Errorf("loopback carried no data (tx=%d rx=%d)", tx.Load(), rx.Load())
	}
	return nil
}
