package main

// Every call that leaves the process (network, ssh, sftp, ssh-agent, child
// processes, the kernel's NFS and mount machinery) goes through one of the
// bounded helpers here or carries its own deadline. The knobs:
//
//	connect_timeout    TCP connect, each ssh handshake, sftp subsystem start,
//	                   each ssh-agent exchange during a handshake
//	op_timeout         an sftp request with no reply from the server for this
//	                   long (the session has been silent the whole time)
//	op_max_time        an sftp request still running after this long, however
//	                   busy the session is
//	keepalive_timeout  a keepalive with no reply
//	mount_timeout      mount, and the check that the mount point responds
//	unmount_timeout    each umount attempt
//
// Prompts for a passphrase or password are the one deliberate exception:
// they wait for a person, and only happen during the first connection.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// opTimeoutError reports a remote operation that was given up on. The session
// it ran on has been torn down by the time the caller sees it.
type opTimeoutError struct {
	op  string
	why string
}

func (e *opTimeoutError) Error() string { return e.op + ": " + e.why }

func (e *opTimeoutError) Timeout() bool { return true }

// activity records when the server last sent anything on an sftp session.
// It is what lets op_timeout mean "the server went quiet" rather than "the
// operation took long": a listing of a huge directory is many round trips and
// may legitimately take minutes, but the server answers all the way through.
type activity struct{ last atomic.Int64 }

func newActivity() *activity {
	a := &activity{}
	a.touch()
	return a
}

func (a *activity) touch() { a.last.Store(time.Now().UnixNano()) }

func (a *activity) since() time.Time { return time.Unix(0, a.last.Load()) }

// activityReader marks the session active whenever bytes arrive from it.
type activityReader struct {
	r io.Reader
	a *activity
}

func (r activityReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.a.touch()
	}
	return n, err
}

// bounded runs one remote operation on the session identified by gen and
// gives up on it when the server has been silent for op_timeout, or when it
// has run for op_max_time regardless.
//
// A stalled reply does not look like a dead connection: sshd still answers
// keepalives while its sftp-server is stuck, say in a stat of a path on a
// hung or locked filesystem. And because sftp-server handles one request at
// a time, everything queued behind the stuck request is stuck too. So on
// timeout the whole session is dropped. That fails the abandoned operation
// (and anything else in flight) promptly instead of leaking it, and the
// usual reconnection brings up a fresh sftp-server.
func (c *Conn) bounded(gen uint64, op string, fn func() error) error {
	quiet := c.cfg.OpTimeout.D()
	hard := c.cfg.OpMaxTime.D()
	if quiet <= 0 && hard <= 0 {
		return fn()
	}
	act := c.activityFor(gen)

	done := make(chan error, 1)
	go func() { done <- fn() }()

	start := time.Now()
	first := quiet
	if first <= 0 || (hard > 0 && hard < first) {
		first = hard
	}
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-t.C:
		}
		why, wait := overdue(time.Now(), start, act, quiet, hard)
		if why != "" {
			c.drop(gen, fmt.Sprintf("%s: %s, dropping the ssh session", op, why))
			return &opTimeoutError{op: op, why: why}
		}
		t.Reset(wait)
	}
}

// overdue decides whether an operation started at start should be abandoned
// now, and if not, how long until it should be checked again.
func overdue(now, start time.Time, act *activity, quiet, hard time.Duration) (string, time.Duration) {
	wait := time.Duration(1<<63 - 1)
	if hard > 0 {
		elapsed := now.Sub(start)
		if elapsed >= hard {
			return fmt.Sprintf("still running after %s", hard), 0
		}
		wait = hard - elapsed
	}
	if quiet > 0 {
		last := start
		if act != nil {
			if l := act.since(); l.After(last) {
				last = l
			}
		}
		silent := now.Sub(last)
		if silent >= quiet {
			return fmt.Sprintf("no reply from the server within %s", quiet), 0
		}
		if quiet-silent < wait {
			wait = quiet - silent
		}
	}
	return "", wait
}

// keepalive sends one keepalive and waits at most timeout for the answer.
// x/crypto's SendRequest has no deadline of its own: on a link that silently
// stopped delivering packets it would wait until the kernel gives up on the
// TCP connection, which can take a quarter of an hour.
func keepalive(client *ssh.Client, timeout time.Duration) error {
	res := make(chan error, 1)
	go func() {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		res <- err
	}()
	if timeout <= 0 {
		return <-res
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-res:
		return err
	case <-t.C:
		// The goroutine is released when the caller drops the session.
		return fmt.Errorf("no keepalive reply within %s", timeout)
	}
}

var errConnClosed = errors.New("connection closed")

// lockDial takes the dial lock, waiting at most wait (0 = until it is free or
// the connection is closed). NFS requests that find the session down queue
// here behind the dial in progress; without a limit they would pile up for as
// long as a slow multi-hop dial takes.
func (c *Conn) lockDial(wait time.Duration) error {
	if wait <= 0 {
		select {
		case c.dialing <- struct{}{}:
			return nil
		case <-c.done:
			return errConnClosed
		}
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case c.dialing <- struct{}{}:
		return nil
	case <-c.done:
		return errConnClosed
	case <-t.C:
		return fmt.Errorf("still connecting to %s after %s", c.cfg.Addr(), wait)
	}
}

func (c *Conn) unlockDial() { <-c.dialing }

// closerFunc adapts a function to io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// statWithin stats a local path but gives up after d. A dead NFS mount left
// on the mount point by a crashed run blocks stat in the kernel, and nothing
// can interrupt that; the goroutine is abandoned, which is fine for the
// startup check this is used for.
func statWithin(path string, d time.Duration) (os.FileInfo, error) {
	type result struct {
		fi  os.FileInfo
		err error
	}
	res := make(chan result, 1)
	go func() {
		fi, err := os.Stat(path)
		res <- result{fi, err}
	}()
	if d <= 0 {
		r := <-res
		return r.fi, r.err
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case r := <-res:
		return r.fi, r.err
	case <-t.C:
		return nil, fmt.Errorf("no response after %s; is a stale NFS mount still there? try: umount -f %s", d, path)
	}
}
