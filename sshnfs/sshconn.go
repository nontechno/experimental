package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"
)

// Conn owns the SSH transport and the SFTP session on top of it. It hands out
// a *sftp.Client together with a generation number; when a caller sees a
// connection level error it reports the generation back and Conn redials
// lazily on the next request.
type Conn struct {
	cfg *Config
	log *Logger

	mu           sync.Mutex
	ssh          *ssh.Client
	sftp         *sftp.Client
	extra        []io.Closer // jump-host clients owned by this session
	gen          uint64
	closed       bool
	lastFail     time.Time
	lastErr      error
	reconnecting bool
	dialing      sync.Mutex

	done      chan struct{}
	closeOnce sync.Once

	// onReconnect is called after a successful redial so caches keyed on the
	// old session can be dropped.
	onReconnect func()
}

// dialBackoff is how long a failed dial is remembered. Without it, an
// unreachable host makes every NFS request queue behind its own full
// connect timeout, and the backlog grows faster than it drains.
const dialBackoff = time.Second

func NewConn(cfg *Config, log *Logger) *Conn {
	return &Conn{cfg: cfg, log: log, done: make(chan struct{})}
}

// Client returns a live sftp client, dialing if necessary.
func (c *Conn) Client() (*sftp.Client, uint64, error) {
	if cl, gen, ok := c.current(); ok {
		return cl, gen, nil
	}
	return c.connect(false)
}

// current returns the established session, if there is one.
func (c *Conn) current() (*sftp.Client, uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.sftp == nil {
		return nil, 0, false
	}
	return c.sftp, c.gen, true
}

func (c *Conn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// connect establishes a session. ignoreBackoff is set by the background
// reconnector, which is itself the retry loop and must not be short-circuited
// by the cached failure it just recorded.
func (c *Conn) connect(ignoreBackoff bool) (*sftp.Client, uint64, error) {
	// Serialise dials so a burst of NFS requests produces one connection.
	c.dialing.Lock()
	defer c.dialing.Unlock()

	if cl, gen, ok := c.current(); ok {
		return cl, gen, nil
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, 0, errors.New("connection closed")
	}
	if !ignoreBackoff && c.lastErr != nil && time.Since(c.lastFail) < dialBackoff {
		err := c.lastErr
		c.mu.Unlock()
		return nil, 0, err
	}
	reconnect := c.gen > 0
	c.mu.Unlock()

	sshClient, sftpClient, extra, err := c.dial()
	if err != nil {
		c.mu.Lock()
		c.lastFail, c.lastErr = time.Now(), err
		c.mu.Unlock()
		return nil, 0, err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		sftpClient.Close()
		sshClient.Close()
		closeAll(extra)
		return nil, 0, errors.New("connection closed")
	}
	c.ssh, c.sftp, c.extra = sshClient, sftpClient, extra
	c.lastErr = nil
	c.gen++
	gen := c.gen
	c.mu.Unlock()

	go c.watch(sshClient, sftpClient, gen)
	if reconnect {
		c.log.Infof("reconnected to %s@%s", c.cfg.User, c.cfg.Addr())
		if c.onReconnect != nil {
			c.onReconnect()
		}
	}
	return sftpClient, gen, nil
}

// Invalidate tears down the session identified by gen so the next Client call
// redials. Calls referring to an older generation are ignored.
func (c *Conn) Invalidate(gen uint64) {
	c.mu.Lock()
	if c.gen != gen || c.sftp == nil {
		c.mu.Unlock()
		return
	}
	sftpClient, sshClient, extra := c.sftp, c.ssh, c.extra
	c.sftp, c.ssh, c.extra = nil, nil, nil
	c.mu.Unlock()

	if c.cfg.Reconnect {
		c.log.Warnf("ssh connection lost, reconnecting in %s", c.cfg.ReconnectDelay)
	} else {
		c.log.Warnf("ssh connection lost, will reconnect on the next request")
	}
	if sftpClient != nil {
		sftpClient.Close()
	}
	if sshClient != nil {
		sshClient.Close()
	}
	closeAll(extra)
	c.scheduleReconnect()
}

// scheduleReconnect starts the background reconnector, unless it is disabled,
// already running, or no longer needed.
func (c *Conn) scheduleReconnect() {
	if !c.cfg.Reconnect {
		return
	}
	c.mu.Lock()
	if c.closed || c.sftp != nil || c.reconnecting {
		c.mu.Unlock()
		return
	}
	c.reconnecting = true
	c.mu.Unlock()
	go c.reconnectLoop()
}

// reconnectLoop redials after a pause, backing off up to the configured
// ceiling. It exists so an idle mount comes back on its own rather than
// waiting for a request to notice the link is gone.
func (c *Conn) reconnectLoop() {
	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		c.mu.Unlock()
	}()

	delay := c.cfg.ReconnectDelay.D()
	if delay <= 0 {
		delay = time.Second
	}
	ceiling := c.cfg.ReconnectMaxDelay.D()

	for attempt := 1; ; attempt++ {
		select {
		case <-c.done:
			return
		case <-time.After(delay):
		}
		if _, _, ok := c.current(); ok {
			return // a request got there first
		}
		if c.isClosed() {
			return
		}
		if _, _, err := c.connect(true); err == nil {
			return
		} else {
			c.log.Warnf("reconnect attempt %d failed: %v", attempt, err)
		}
		if max := c.cfg.ReconnectMaxAttempts; max > 0 && attempt >= max {
			c.log.Errorf("giving up after %d reconnection attempts", attempt)
			return
		}
		if delay *= 2; ceiling > 0 && delay > ceiling {
			delay = ceiling
		}
	}
}

func (c *Conn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	c.mu.Lock()
	c.closed = true
	sftpClient, sshClient, extra := c.sftp, c.ssh, c.extra
	c.sftp, c.ssh, c.extra = nil, nil, nil
	c.mu.Unlock()

	var err error
	if sftpClient != nil {
		err = sftpClient.Close()
	}
	if sshClient != nil {
		if e := sshClient.Close(); err == nil {
			err = e
		}
	}
	closeAll(extra)
	return err
}

// closeAll shuts down jump-host clients in reverse order, innermost first.
func closeAll(closers []io.Closer) {
	for i := len(closers) - 1; i >= 0; i-- {
		closers[i].Close()
	}
}

// watch keeps the transport alive and notices a dead peer promptly.
func (c *Conn) watch(sshClient *ssh.Client, sftpClient *sftp.Client, gen uint64) {
	done := make(chan struct{})
	go func() {
		sshClient.Wait()
		close(done)
	}()

	interval := c.cfg.KeepAlive.D()
	var tick <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-done:
			c.Invalidate(gen)
			return
		case <-tick:
			if _, _, err := sshClient.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				c.Invalidate(gen)
				return
			}
		}
	}
}

func (c *Conn) dial() (*ssh.Client, *sftp.Client, []io.Closer, error) {
	target := c.cfg.target()
	timeout := target.ConnectTimeout

	nc, extra, err := c.dialTransport(target)
	if err != nil {
		return nil, nil, nil, err
	}
	closeExtra := func() {
		for i := len(extra) - 1; i >= 0; i-- {
			extra[i].Close()
		}
	}

	sshClient, err := c.handshake(nc, target, timeout)
	if err != nil {
		nc.Close()
		closeExtra()
		return nil, nil, nil, err
	}

	opts := []sftp.ClientOption{
		sftp.MaxPacket(c.cfg.MaxPacket),
		sftp.MaxConcurrentRequestsPerFile(c.cfg.MaxConcurrent),
		sftp.UseConcurrentReads(true),
		sftp.UseConcurrentWrites(true),
		sftp.UseFstat(true),
	}
	var sftpClient *sftp.Client
	err = withTimeout(nc, timeout, func() error {
		var e error
		sftpClient, e = sftp.NewClient(sshClient, opts...)
		return e
	})
	if err != nil {
		sshClient.Close()
		closeExtra()
		return nil, nil, nil, fmt.Errorf("start sftp subsystem: %w", err)
	}
	return sshClient, sftpClient, extra, nil
}

// dialTransport produces the connection the target's SSH handshake runs over:
// a plain TCP connection, a channel on the last ProxyJump hop, or the pipes of
// a ProxyCommand. The returned closers own anything that must outlive the dial
// and be shut down with the session.
func (c *Conn) dialTransport(target *endpoint) (net.Conn, []io.Closer, error) {
	switch {
	case len(c.cfg.jumpHops) > 0:
		return c.dialThroughJumps(target)
	case c.cfg.ProxyCommand != "" && !isNone(c.cfg.ProxyCommand):
		conn, err := dialProxyCommand(c.cfg.ProxyCommand, target, c.log)
		if err != nil {
			return nil, nil, err
		}
		return conn, nil, nil
	default:
		c.log.Debugf("dialing %s@%s", target.User, target.Addr())
		nc, err := net.DialTimeout("tcp", target.Addr(), target.ConnectTimeout)
		if err != nil {
			return nil, nil, fmt.Errorf("ssh dial %s: %w", target.Addr(), err)
		}
		return nc, nil, nil
	}
}

// dialThroughJumps walks the ProxyJump chain, each hop dialed through the one
// before it, and returns a connection to the target opened by the last hop.
func (c *Conn) dialThroughJumps(target *endpoint) (net.Conn, []io.Closer, error) {
	var (
		clients []*ssh.Client
		extra   []io.Closer
	)
	fail := func(err error) (net.Conn, []io.Closer, error) {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
		return nil, nil, err
	}

	for i, hop := range c.cfg.jumpHops {
		var (
			nc  net.Conn
			err error
		)
		switch {
		case i == 0 && hop.ProxyCommand != "":
			// The first link has no predecessor to tunnel through, so a
			// ProxyCommand of its own can be run locally to reach it.
			c.log.Debugf("dialing jump host %s@%s through its proxy command", hop.User, hop.Addr())
			nc, err = dialProxyCommand(hop.ProxyCommand, hop, c.log)
			if err != nil {
				return fail(fmt.Errorf("jump host %s: %w", hop.Addr(), err))
			}
		case i == 0:
			c.log.Debugf("dialing jump host %s@%s", hop.User, hop.Addr())
			nc, err = net.DialTimeout("tcp", hop.Addr(), hop.ConnectTimeout)
			if err != nil {
				return fail(fmt.Errorf("jump host %s: %w", hop.Addr(), err))
			}
		default:
			if hop.ProxyCommand != "" {
				// Its predecessor in the chain already determines how we get
				// there; running the command as well would bypass the chain.
				c.log.Debugf("ignoring the proxy command for %s: it is reached through %s",
					hop.Addr(), c.cfg.jumpHops[i-1].Addr())
			}
			c.log.Debugf("dialing jump host %s@%s through %s", hop.User, hop.Addr(), c.cfg.jumpHops[i-1].Addr())
			prev := clients[i-1]
			err = withTimeout(prev, hop.ConnectTimeout, func() error {
				var e error
				nc, e = prev.Dial("tcp", hop.Addr())
				return e
			})
			if err != nil {
				return fail(fmt.Errorf("jump host %s via %s: %w", hop.Addr(), c.cfg.jumpHops[i-1].Addr(), err))
			}
		}
		client, err := c.handshake(nc, hop, hop.ConnectTimeout)
		if err != nil {
			nc.Close()
			return fail(fmt.Errorf("jump host %s: %w", hop.Addr(), err))
		}
		clients = append(clients, client)
	}

	last := clients[len(clients)-1]
	lastHop := c.cfg.jumpHops[len(c.cfg.jumpHops)-1]
	var conn net.Conn
	err := withTimeout(last, target.ConnectTimeout, func() error {
		var e error
		conn, e = last.Dial("tcp", target.Addr())
		return e
	})
	if err != nil {
		return fail(fmt.Errorf("connect to %s from jump host %s: %w", target.Addr(), lastHop.Addr(), err))
	}
	// The jump clients must stay up for as long as the session does.
	for _, cl := range clients {
		extra = append(extra, cl)
	}
	return conn, extra, nil
}

// handshake runs the SSH client handshake over an established transport.
func (c *Conn) handshake(nc net.Conn, e *endpoint, timeout time.Duration) (*ssh.Client, error) {
	clientCfg, err := c.clientConfig(e)
	if err != nil {
		return nil, err
	}
	var client *ssh.Client
	err = withTimeout(nc, timeout, func() error {
		// ssh.ClientConfig.Timeout only covers a TCP connect, and connections
		// from a jump host or a ProxyCommand have no deadline support at all,
		// so withTimeout is what bounds this.
		sshConn, chans, reqs, e := ssh.NewClientConn(nc, e.Addr(), clientCfg)
		if e != nil {
			return e
		}
		client = ssh.NewClient(sshConn, chans, reqs)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ssh handshake %s: %w", e.Addr(), err)
	}
	return client, nil
}

func (c *Conn) clientConfig(e *endpoint) (*ssh.ClientConfig, error) {
	auths, err := c.authMethods(e)
	if err != nil {
		return nil, err
	}
	hostKey, err := c.hostKeyCallback(e)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{
		User:            e.User,
		Auth:            auths,
		HostKeyCallback: hostKey,
		Timeout:         e.ConnectTimeout,
	}, nil
}

func (c *Conn) authMethods(e *endpoint) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if e.UseAgent {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			if conn, err := net.Dial("unix", sock); err == nil {
				methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
				c.log.Debugf("using ssh-agent at %s", sock)
			} else {
				c.log.Warnf("ssh-agent at %s unusable: %v", sock, err)
			}
		}
	}

	explicitKeys := len(e.IdentityFiles) > 0
	for _, path := range identityFiles(e) {
		signer, err := loadPrivateKey(path, e.Passphrase)
		if err != nil {
			if explicitKeys {
				// Explicitly requested: a failure here is fatal.
				return nil, err
			}
			c.log.Debugf("skipping key %s: %v", path, err)
			continue
		}
		c.log.Debugf("using key %s", path)
		methods = append(methods, ssh.PublicKeys(signer))
	}

	password := e.Password
	if password == "" && e.AskPassword {
		p, err := promptPassword(fmt.Sprintf("%s@%s password: ", e.User, e.Host))
		if err != nil {
			return nil, err
		}
		password = p
		if e.Host == c.cfg.Host {
			c.cfg.Password = p // reuse it when reconnecting
		}
	}
	if password != "" {
		methods = append(methods,
			ssh.Password(password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = password
				}
				return answers, nil
			}),
		)
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no usable ssh authentication method for %s (try -i, -agent or -ask-password)", e.Addr())
	}
	return methods, nil
}

func identityFiles(e *endpoint) []string {
	if len(e.IdentityFiles) > 0 {
		out := make([]string, 0, len(e.IdentityFiles))
		for _, p := range e.IdentityFiles {
			out = append(out, expandUser(p))
		}
		return out
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		p := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func loadPrivateKey(path, passphrase string) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	if passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("parse key %s: %w", path, err)
		}
		return signer, nil
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return signer, nil
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		p, perr := promptPassword(fmt.Sprintf("passphrase for %s: ", path))
		if perr != nil {
			return nil, fmt.Errorf("key %s is encrypted: %w", path, perr)
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(p))
		if err != nil {
			return nil, fmt.Errorf("parse key %s: %w", path, err)
		}
		return signer, nil
	}
	return nil, fmt.Errorf("parse key %s: %w", path, err)
}

func (c *Conn) hostKeyCallback(e *endpoint) (ssh.HostKeyCallback, error) {
	if e.Insecure {
		c.log.Warnf("host key verification disabled for %s", e.Host)
		return ssh.InsecureIgnoreHostKey(), nil
	}
	path := expandUser(e.KnownHostsFile)
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("known_hosts %s: %w (use -known-hosts or -insecure)", path, err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := cb(hostname, remote, key); err != nil {
			var kerr *knownhosts.KeyError
			if errors.As(err, &kerr) && len(kerr.Want) == 0 {
				return fmt.Errorf("host %s is not in %s; add it with: ssh-keyscan -p %d %s >> %s",
					hostname, path, e.Port, e.Host, path)
			}
			return err
		}
		return nil
	}, nil
}

func promptPassword(prompt string) (string, error) {
	fd := int(syscall.Stdin)
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal, cannot prompt for a secret")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// isConnError reports whether err means the SSH session is gone, as opposed to
// a normal filesystem error such as ENOENT.
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, sftp.ErrSSHFxConnectionLost) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{
		"connection lost",
		"use of closed network connection",
		"broken pipe",
		"connection reset by peer",
		"session closed",
		"EOF",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
