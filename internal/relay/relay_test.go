package relay

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// upstream serves each accepted connection with handle, numbered from 0.
func upstream(t *testing.T, handle func(n int, c *net.TCPConn)) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := int(accepted.Add(1)) - 1
			go func() {
				defer c.Close()
				handle(n, c.(*net.TCPConn))
			}()
		}
	}()
	return ln.Addr().String(), &accepted
}

// reset drops c with a TCP reset, as QEMU's listener does.
func reset(c *net.TCPConn) {
	c.SetLinger(0)
	c.Close()
}

func start(t *testing.T, target string) *Relay {
	t.Helper()
	r, err := Listen("127.0.0.1:0", target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

// roundTrip sends req through r, closes the sending side and reads the
// whole answer.
func roundTrip(t *testing.T, r *Relay, req []byte) ([]byte, error) {
	t.Helper()
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(r.Port())))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(req); err != nil {
		return nil, err
	}
	c.(*net.TCPConn).CloseWrite()
	return io.ReadAll(c)
}

// echo answers with "re:" and everything the client sent, after the client
// closed its side.
func echo(c *net.TCPConn) {
	b, _ := io.ReadAll(c)
	c.Write(append([]byte("re:"), b...))
}

func TestRelays(t *testing.T) {
	target, accepted := upstream(t, func(_ int, c *net.TCPConn) { echo(c) })
	r := start(t, target)
	got, err := roundTrip(t, r, []byte("ping"))
	if err != nil || string(got) != "re:ping" {
		t.Fatalf("answer = %q, %v", got, err)
	}
	if n := accepted.Load(); n != 1 {
		t.Errorf("upstream accepted %d connections, want 1", n)
	}
}

// TestRedialsResets checks a connection reset before the first byte back
// is redialed, with what the client sent sent again.
func TestRedialsResets(t *testing.T) {
	var requests atomic.Int32
	target, accepted := upstream(t, func(n int, c *net.TCPConn) {
		if n < 3 {
			// Read some of the request first, as a handshake would.
			c.Read(make([]byte, 2))
			reset(c)
			return
		}
		b, _ := io.ReadAll(c)
		if string(b) == "ping" {
			requests.Add(1)
		}
		c.Write([]byte("re:" + string(b)))
	})
	r := start(t, target)
	got, err := roundTrip(t, r, []byte("ping"))
	if err != nil || string(got) != "re:ping" {
		t.Fatalf("answer = %q, %v", got, err)
	}
	if n := accepted.Load(); n != 4 {
		t.Errorf("upstream accepted %d connections, want 4", n)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("upstream got the whole request %d times, want 1", n)
	}
}

// TestRedialsRefused checks a target that isn't listening yet is redialed.
func TestRedialsRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := ln.Addr().String()
	ln.Close()
	r := start(t, target)
	go func() {
		time.Sleep(100 * time.Millisecond)
		ln, err := net.Listen("tcp", target)
		if err != nil {
			return
		}
		t.Cleanup(func() { ln.Close() })
		c, err := ln.Accept()
		if err == nil {
			echo(c.(*net.TCPConn))
			c.Close()
		}
	}()
	got, err := roundTrip(t, r, []byte("ping"))
	if err != nil || string(got) != "re:ping" {
		t.Fatalf("answer = %q, %v", got, err)
	}
}

// TestGivesUp checks a target that keeps resetting fails the client within
// Patience.
func TestGivesUp(t *testing.T) {
	target, _ := upstream(t, func(_ int, c *net.TCPConn) { reset(c) })
	r := start(t, target)
	start := time.Now()
	got, _ := roundTrip(t, r, []byte("ping"))
	if len(got) != 0 {
		t.Errorf("answer = %q, want none", got)
	}
	if d := time.Since(start); d > Patience+time.Second {
		t.Errorf("gave up after %s", d)
	}
}

// TestNoRedialAfterAnswer checks a connection the target has answered on
// isn't redialed, so nothing is sent to it twice.
func TestNoRedialAfterAnswer(t *testing.T) {
	target, accepted := upstream(t, func(_ int, c *net.TCPConn) {
		c.Write([]byte("hello"))
		c.Read(make([]byte, 4))
		reset(c)
	})
	r := start(t, target)
	got, _ := roundTrip(t, r, []byte("ping"))
	if string(got) != "hello" {
		t.Errorf("answer = %q, want hello", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n := accepted.Load(); n != 1 {
		t.Errorf("upstream accepted %d connections, want 1", n)
	}
}

// TestNoRedialOnCleanClose checks a target that closes without answering
// isn't redialed: that is the target's answer, not QEMU dropping it.
func TestNoRedialOnCleanClose(t *testing.T) {
	target, accepted := upstream(t, func(_ int, c *net.TCPConn) { io.ReadAll(c) })
	r := start(t, target)
	got, err := roundTrip(t, r, []byte("ping"))
	if err != nil || len(got) != 0 {
		t.Errorf("answer = %q, %v", got, err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := accepted.Load(); n != 1 {
		t.Errorf("upstream accepted %d connections, want 1", n)
	}
}

// TestLargeUpload checks more than MaxReplay before the first byte back
// still goes through, as docker load sends an archive before answering.
func TestLargeUpload(t *testing.T) {
	target, _ := upstream(t, func(_ int, c *net.TCPConn) {
		b, _ := io.ReadAll(c)
		c.Write([]byte(strconv.Itoa(len(b))))
	})
	r := start(t, target)
	req := bytes.Repeat([]byte("x"), 3*MaxReplay+7)
	got, err := roundTrip(t, r, req)
	if err != nil || string(got) != strconv.Itoa(len(req)) {
		t.Errorf("answer = %q, %v", got, err)
	}
}

// TestHalfClose checks the client still gets everything after the target
// stopped reading, and the target still gets everything after the client
// stopped writing.
func TestHalfClose(t *testing.T) {
	target, _ := upstream(t, func(_ int, c *net.TCPConn) {
		c.Write([]byte("first,"))
		b, _ := io.ReadAll(c)
		c.Write([]byte("then " + string(b)))
	})
	r := start(t, target)
	got, err := roundTrip(t, r, []byte("ping"))
	if err != nil || string(got) != "first,then ping" {
		t.Errorf("answer = %q, %v", got, err)
	}
}

// TestCloseEndsConnections checks Close returns with a connection open.
func TestCloseEndsConnections(t *testing.T) {
	target, _ := upstream(t, func(_ int, c *net.TCPConn) {
		c.Write([]byte("hello"))
		io.ReadAll(c)
	})
	r, err := Listen("127.0.0.1:0", target)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(r.Port())))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.ReadFull(c, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hangs with a connection open")
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("the client's connection is still open")
	}
}
