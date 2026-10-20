// Package relay forwards loopback connections to an Engine VM's port
// forwards. QEMU's user-mode network listens for them with a backlog of
// one, so of several connections that arrive together most are reset,
// on connect or just after it, before the VM has sent anything. A Relay
// listens with the system's backlog instead, and redials a connection that
// was refused or reset before the first byte came back, sending what the
// client has sent so far again.
package relay

import (
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

const (
	// Patience bounds how long a connection is redialed.
	Patience = 2 * time.Second
	// MaxReplay is how much the client may send before the first byte back
	// and still be redialed. Past it the connection goes on, but a reset
	// ends it.
	MaxReplay = 64 << 10

	firstBackoff = 5 * time.Millisecond
	maxBackoff   = 100 * time.Millisecond
	chunkSize    = 32 << 10
)

// Relay forwards the connections to its port to one target.
type Relay struct {
	ln     net.Listener
	target string
	wg     sync.WaitGroup
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// Listen relays connections to addr, a loopback address, to target.
func Listen(addr, target string) (*Relay, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	r := &Relay{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	r.wg.Add(1)
	go r.serve()
	return r, nil
}

// Port is the port the relay listens on.
func (r *Relay) Port() int { return r.ln.Addr().(*net.TCPAddr).Port }

// Close stops the relay and closes its connections, both sides.
func (r *Relay) Close() {
	r.mu.Lock()
	r.closed = true
	for c := range r.conns {
		c.Close()
	}
	r.mu.Unlock()
	r.ln.Close()
	r.wg.Wait()
}

func (r *Relay) serve() {
	defer r.wg.Done()
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		if !r.track(c) {
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer r.untrack(c)
			r.handle(c)
		}()
	}
}

// track records c for Close. Once the relay is closed it closes c instead
// and reports false.
func (r *Relay) track(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		c.Close()
		return false
	}
	r.conns[c] = struct{}{}
	return true
}

func (r *Relay) untrack(c net.Conn) {
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
	c.Close()
}

// handle relays client until both sides are done.
func (r *Relay) handle(client net.Conn) {
	// One reader on the client for the whole connection, so what it read
	// before the first byte back can be sent again on a redial. It ends
	// when the client closes or untrack closes the client.
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		for {
			b := make([]byte, chunkSize)
			n, err := client.Read(b)
			if n > 0 {
				chunks <- b[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		client.Close()
		for range chunks {
		}
	}()

	c := conn{chunks: chunks}
	up, first, ok := r.establish(&c)
	if !ok {
		return
	}
	defer r.untrack(up)
	if _, err := client.Write(first); err != nil {
		return
	}
	// Each direction ends on its own, so a side that closed only its
	// writing half still gets the rest.
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		if c.clientDone {
			return
		}
		for b := range chunks {
			if _, err := up.Write(b); err != nil {
				up.Close()
				return
			}
		}
		closeWrite(up)
	}()
	if _, err := io.Copy(client, up); err != nil {
		// Not a clean end: drop the client too, which ends its reader.
		client.Close()
	} else {
		closeWrite(client)
	}
	<-sent
}

// conn is a client connection's state before the first byte back.
type conn struct {
	chunks <-chan []byte
	// sent is what the client sent, while it is at most MaxReplay.
	sent       [][]byte
	size       int
	clientDone bool
}

// establish dials the target until it sends a first byte back, sending the
// client's bytes to each attempt. It returns the connection and the first
// bytes.
func (r *Relay) establish(c *conn) (net.Conn, []byte, bool) {
	deadline := time.Now().Add(Patience)
	backoff := firstBackoff
	for {
		up, err := net.DialTimeout("tcp", r.target, time.Until(deadline))
		if err == nil {
			if !r.track(up) {
				return nil, nil, false
			}
			var first []byte
			if first, err = c.waitFirst(up); err == nil {
				return up, first, true
			}
			r.untrack(up)
		}
		if !retryable(err) || c.size > MaxReplay || time.Now().Add(backoff).After(deadline) {
			return nil, nil, false
		}
		time.Sleep(backoff)
		backoff = min(2*backoff, maxBackoff)
	}
}

// waitFirst sends what the client sent so far to up, then forwards the
// client's chunks until up sends its first bytes or fails.
func (c *conn) waitFirst(up net.Conn) ([]byte, error) {
	type result struct {
		b   []byte
		err error
	}
	res := make(chan result, 1)
	go func() {
		b := make([]byte, chunkSize)
		n, err := up.Read(b)
		if n > 0 {
			err = nil
		}
		res <- result{b[:n], err}
	}()
	fail := func(err error) ([]byte, error) {
		// The read fails too once up is closed; wait for it, so it doesn't
		// outlive this attempt.
		up.Close()
		<-res
		return nil, err
	}
	for _, b := range c.sent {
		if _, err := up.Write(b); err != nil {
			return fail(err)
		}
	}
	in := c.chunks
	if c.clientDone {
		closeWrite(up)
		in = nil
	}
	for {
		select {
		case r := <-res:
			return r.b, r.err
		case b, open := <-in:
			if !open {
				c.clientDone = true
				closeWrite(up)
				in = nil
				continue
			}
			c.size += len(b)
			if c.size <= MaxReplay {
				c.sent = append(c.sent, b)
			} else {
				c.sent = nil
			}
			if _, err := up.Write(b); err != nil {
				return fail(err)
			}
		}
	}
}

// retryable reports whether err is QEMU dropping the connection: refused
// or reset.
func retryable(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
}
