package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// endpoint is everything needed to authenticate to one SSH host. The target
// and each ProxyJump hop are described the same way, so the dialing code does
// not care which it is looking at.
type endpoint struct {
	User           string
	Host           string
	Port           int
	IdentityFiles  []string
	Passphrase     string
	Password       string
	AskPassword    bool
	UseAgent       bool
	KnownHostsFile string
	Insecure       bool
	ConnectTimeout time.Duration

	// ProxyCommand is set when ssh config gives this hop one of its own.
	// It is honoured only for the first link of a chain; see dialThroughJumps.
	ProxyCommand string
}

func (e *endpoint) Addr() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// target describes the host we actually want to reach.
func (c *Config) target() *endpoint {
	return &endpoint{
		User:           c.User,
		Host:           c.Host,
		Port:           c.Port,
		IdentityFiles:  c.IdentityFiles,
		Passphrase:     c.IdentityPassphrase,
		Password:       c.Password,
		AskPassword:    c.AskPassword,
		UseAgent:       c.UseAgent,
		KnownHostsFile: c.KnownHostsFile,
		Insecure:       c.InsecureHostKey,
		ConnectTimeout: c.ConnectTimeout.D(),
	}
}

func isNone(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "none") }

// parseJumpSpec splits an ssh -J style list into hops. Each is
// [user@]host[:port]; anything omitted is filled in by the caller, from
// ~/.ssh/config first and the main configuration second.
func parseJumpSpec(spec string) ([]*endpoint, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || isNone(spec) {
		return nil, nil
	}
	var hops []*endpoint
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		hop := &endpoint{}
		if i := strings.LastIndex(part, "@"); i >= 0 {
			hop.User = part[:i]
			part = part[i+1:]
		}
		// A bare IPv6 literal must be bracketed, as in ssh.
		if strings.HasPrefix(part, "[") {
			end := strings.Index(part, "]")
			if end < 0 {
				return nil, fmt.Errorf("proxy jump %q: unbalanced [", spec)
			}
			hop.Host = part[1:end]
			rest := part[end+1:]
			if strings.HasPrefix(rest, ":") {
				p, err := strconv.Atoi(rest[1:])
				if err != nil {
					return nil, fmt.Errorf("proxy jump %q: bad port %q", spec, rest[1:])
				}
				hop.Port = p
			}
		} else if i := strings.LastIndex(part, ":"); i >= 0 {
			p, err := strconv.Atoi(part[i+1:])
			if err != nil {
				return nil, fmt.Errorf("proxy jump %q: bad port %q", spec, part[i+1:])
			}
			hop.Host, hop.Port = part[:i], p
		} else {
			hop.Host = part
		}
		if hop.Host == "" {
			return nil, fmt.Errorf("proxy jump %q: empty host", spec)
		}
		hops = append(hops, hop)
	}
	return hops, nil
}

// withTimeout closes conn if fn has not finished in time. Connections handed
// to us by a jump host or a ProxyCommand do not support deadlines, so this is
// how the handshake stays bounded on every transport.
func withTimeout(conn io.Closer, d time.Duration, fn func() error) error {
	if d <= 0 {
		return fn()
	}
	var fired atomic.Bool
	t := time.AfterFunc(d, func() {
		fired.Store(true)
		conn.Close()
	})
	defer t.Stop()
	err := fn()
	if err != nil && fired.Load() {
		return fmt.Errorf("timed out after %s: %w", d, err)
	}
	return err
}

// cmdConn adapts a ProxyCommand's stdin/stdout to a net.Conn. Deadlines are
// accepted and ignored: pipes have none, and withTimeout covers the handshake.
type cmdConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	name   string

	once sync.Once
	err  error
}

func (c *cmdConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *cmdConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *cmdConn) Close() error {
	c.once.Do(func() {
		c.stdin.Close()
		if c.cmd.Process != nil {
			c.cmd.Process.Kill()
		}
		c.stdout.Close()
		c.err = c.cmd.Wait()
	})
	return nil
}

type pipeAddr struct{ name string }

func (a pipeAddr) Network() string { return "proxycommand" }
func (a pipeAddr) String() string  { return a.name }

func (c *cmdConn) LocalAddr() net.Addr              { return pipeAddr{"local"} }
func (c *cmdConn) RemoteAddr() net.Addr             { return pipeAddr{c.name} }
func (c *cmdConn) SetDeadline(time.Time) error      { return nil }
func (c *cmdConn) SetReadDeadline(time.Time) error  { return nil }
func (c *cmdConn) SetWriteDeadline(time.Time) error { return nil }

// dialProxyCommand runs the command and hands back its pipes as a connection.
// The command is executed through /bin/sh, exactly as ssh does, so it may
// contain pipes, quoting and the usual %h/%p/%r tokens.
func dialProxyCommand(command string, target *endpoint, log *Logger) (net.Conn, error) {
	expanded := expandSSHTokens(command, target.Host, target.Host, target.User, target.Port)
	log.Debugf("proxy command: %s", expanded)

	cmd := exec.Command("/bin/sh", "-c", expanded)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("proxy command %q: %w", expanded, err)
	}
	// The command's diagnostics are often the only clue about why a jump
	// failed, so surface them rather than dropping them on the floor.
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Warnf("proxy command: %s", sc.Text())
		}
	}()
	return &cmdConn{cmd: cmd, stdin: stdin, stdout: stdout, name: expanded}, nil
}
