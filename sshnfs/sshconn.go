package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	act          *activity // when the current session last heard from the server

	// dialing serialises dials so a burst of NFS requests produces one
	// connection. A channel rather than a mutex so waiting can time out.
	dialing chan struct{}

	done      chan struct{}
	closeOnce sync.Once

	// onReconnect is called after a successful redial so caches keyed on the
	// old session can be dropped. Guarded by mu: it is installed after the
	// first connection, when a reconnect may already be under way.
	onReconnect func()
}

// secretStore keeps what was entered during the first connection, so a
// reconnect never has to prompt. It is shared by every mount in the process,
// so mounts using the same key ask for its passphrase once.
type secretStore struct {
	mu        sync.Mutex
	signers   map[string]ssh.Signer // parsed private keys by path
	passwords map[string]string     // by user@host:port
}

var secrets = &secretStore{signers: map[string]ssh.Signer{}, passwords: map[string]string{}}

func (s *secretStore) signer(path string) (ssh.Signer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.signers[path]
	return v, ok
}

func (s *secretStore) setSigner(path string, v ssh.Signer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signers[path] = v
}

func (s *secretStore) password(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.passwords[key]
}

func (s *secretStore) setPassword(key, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.passwords[key] = v
}

// SetOnReconnect installs the hook run after every successful redial.
func (c *Conn) SetOnReconnect(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onReconnect = fn
}

// dialBackoff is how long a failed dial is remembered. Without it, an
// unreachable host makes every NFS request queue behind its own full
// connect timeout, and the backlog grows faster than it drains.
const dialBackoff = time.Second

// maxReconnectDelay caps the reconnection backoff when reconnect_max_delay is
// 0, which means "no ceiling of my own".
const maxReconnectDelay = time.Hour

func NewConn(cfg *Config, log *Logger) *Conn {
	return &Conn{
		cfg:     cfg,
		log:     log,
		done:    make(chan struct{}),
		dialing: make(chan struct{}, 1),
	}
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
	// A request waits for a dial in progress no longer than it would wait
	// for a reply; the background reconnector, which is the retry loop, waits
	// for its turn.
	wait := c.cfg.OpTimeout.D()
	if ignoreBackoff {
		wait = 0
	}
	if err := c.lockDial(wait); err != nil {
		return nil, 0, err
	}
	defer c.unlockDial()

	if cl, gen, ok := c.current(); ok {
		return cl, gen, nil
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, 0, errConnClosed
	}
	if !ignoreBackoff && c.lastErr != nil && time.Since(c.lastFail) < dialBackoff {
		err := c.lastErr
		c.mu.Unlock()
		return nil, 0, err
	}
	reconnect := c.gen > 0
	c.mu.Unlock()

	sshClient, sftpClient, extra, act, err := c.dial()
	if err != nil {
		c.mu.Lock()
		c.lastFail, c.lastErr = time.Now(), err
		c.mu.Unlock()
		return nil, 0, err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		teardown(sshClient, sftpClient, extra)
		return nil, 0, errConnClosed
	}
	c.ssh, c.sftp, c.extra, c.act = sshClient, sftpClient, extra, act
	c.lastErr = nil
	c.gen++
	gen := c.gen
	hook := c.onReconnect
	c.mu.Unlock()

	go c.watch(sshClient, gen)
	if reconnect {
		c.log.Infof("reconnected to %s@%s", c.cfg.User, c.cfg.Addr())
		if hook != nil {
			hook()
		}
	}
	return sftpClient, gen, nil
}

// Invalidate tears down the session identified by gen so the next Client call
// redials. Calls referring to an older generation are ignored.
func (c *Conn) Invalidate(gen uint64) {
	c.drop(gen, "ssh connection lost")
}

// drop is Invalidate with a reason for the log.
func (c *Conn) drop(gen uint64, why string) {
	c.mu.Lock()
	if c.gen != gen || c.sftp == nil {
		c.mu.Unlock()
		return
	}
	sftpClient, sshClient, extra := c.sftp, c.ssh, c.extra
	c.sftp, c.ssh, c.extra, c.act = nil, nil, nil, nil
	c.mu.Unlock()

	if c.cfg.Reconnect {
		c.log.Warnf("%s; reconnecting in %s", why, c.cfg.ReconnectDelay)
	} else {
		c.log.Warnf("%s; will reconnect on the next request", why)
	}
	teardown(sshClient, sftpClient, extra)
	c.scheduleReconnect()
}

// teardown closes a session without waiting on the peer. The transport goes
// first: closing the sftp client first sends a channel close and then waits
// for the server to finish, which a stuck sftp-server never does. Once the
// transport is gone every pending sftp request fails at once, so the sftp
// client's own Close has nothing left to wait for; it still runs in the
// background in case a jump host is slow to tear its channel down.
//
// With jump hosts the outermost connection, the only real socket, goes
// first. Closing an inner client sends a channel close over its parent, and
// that write can block on a wedged link; once the socket is closed every
// such write fails immediately instead.
func teardown(sshClient *ssh.Client, sftpClient *sftp.Client, extra []io.Closer) error {
	closeAll(extra)
	var err error
	if sshClient != nil {
		err = sshClient.Close()
	}
	if sftpClient != nil {
		go sftpClient.Close()
	}
	return err
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
	if ceiling <= 0 {
		// "No ceiling" still needs one: doubling without end passes a day
		// after some fifteen failures, which is the same as giving up, and
		// eventually overflows into a negative delay that retries flat out.
		ceiling = maxReconnectDelay
	}

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
		if delay *= 2; delay > ceiling {
			delay = ceiling
		}
	}
}

func (c *Conn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	c.mu.Lock()
	c.closed = true
	sftpClient, sshClient, extra := c.sftp, c.ssh, c.extra
	c.sftp, c.ssh, c.extra, c.act = nil, nil, nil, nil
	c.mu.Unlock()

	return teardown(sshClient, sftpClient, extra)
}

// closeAll shuts down jump-host clients outermost first, so the real socket
// is gone before anything tries to send a goodbye through it (see teardown).
func closeAll(closers []io.Closer) {
	for _, c := range closers {
		c.Close()
	}
}

// activityFor returns the activity tracker of the session gen, or nil if
// that session is no longer current (its requests then fail on their own).
func (c *Conn) activityFor(gen uint64) *activity {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return nil
	}
	return c.act
}

// watch keeps the transport alive and notices a dead peer promptly.
func (c *Conn) watch(sshClient *ssh.Client, gen uint64) {
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
			if err := keepalive(sshClient, c.cfg.KeepAliveTimeout.D()); err != nil {
				c.drop(gen, fmt.Sprintf("ssh connection lost (%v)", err))
				return
			}
		}
	}
}

func (c *Conn) dial() (*ssh.Client, *sftp.Client, []io.Closer, *activity, error) {
	target := c.cfg.target()
	timeout := target.ConnectTimeout

	nc, extra, err := c.dialTransport(target)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// abort unblocks anything waiting on this transport. Behind jump hosts nc
	// is a channel, and closing it alone needs the far end to cooperate; the
	// outermost socket going away does not.
	abort := closerFunc(func() error {
		closeAll(extra)
		return nc.Close()
	})

	sshClient, err := c.handshake(nc, abort, target, timeout)
	if err != nil {
		abort.Close()
		return nil, nil, nil, nil, err
	}

	opts := []sftp.ClientOption{
		sftp.MaxPacket(c.cfg.MaxPacket),
		sftp.MaxConcurrentRequestsPerFile(c.cfg.MaxConcurrent),
		sftp.UseConcurrentReads(true),
		sftp.UseConcurrentWrites(true),
		sftp.UseFstat(true),
	}
	var sftpClient *sftp.Client
	act := newActivity()
	err = withTimeout(abort, timeout, func() error {
		var e error
		sftpClient, e = newSFTPClient(sshClient, act, opts)
		return e
	})
	if err != nil {
		sshClient.Close()
		abort.Close()
		return nil, nil, nil, nil, fmt.Errorf("start sftp subsystem: %w", err)
	}
	return sshClient, sftpClient, extra, act, nil
}

// newSFTPClient is sftp.NewClient with the server's output routed through an
// activity tracker, which is what op_timeout watches.
func newSFTPClient(client *ssh.Client, act *activity, opts []sftp.ClientOption) (*sftp.Client, error) {
	s, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	if err := s.RequestSubsystem("sftp"); err != nil {
		s.Close()
		return nil, err
	}
	pw, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	pr, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	return sftp.NewClientPipe(activityReader{r: pr, a: act}, pw, opts...)
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
		conn, err := dialProxyCommand(c.cfg.ProxyCommand, target, c.log, c.detachHelpers())
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
	// Outermost first; see teardown.
	closeClients := func() error {
		for _, cl := range clients {
			cl.Close()
		}
		return nil
	}
	fail := func(err error) (net.Conn, []io.Closer, error) {
		closeClients()
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
			nc, err = dialProxyCommand(hop.ProxyCommand, hop, c.log, c.detachHelpers())
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
			err = withTimeout(closerFunc(closeClients), hop.ConnectTimeout, func() error {
				var e error
				nc, e = prev.Dial("tcp", hop.Addr())
				return e
			})
			if err != nil {
				return fail(fmt.Errorf("jump host %s via %s: %w", hop.Addr(), c.cfg.jumpHops[i-1].Addr(), err))
			}
		}
		hopConn := nc
		abort := closerFunc(func() error {
			closeClients()
			return hopConn.Close()
		})
		client, err := c.handshake(nc, abort, hop, hop.ConnectTimeout)
		if err != nil {
			nc.Close()
			return fail(fmt.Errorf("jump host %s: %w", hop.Addr(), err))
		}
		clients = append(clients, client)
	}

	last := clients[len(clients)-1]
	lastHop := c.cfg.jumpHops[len(c.cfg.jumpHops)-1]
	var conn net.Conn
	err := withTimeout(closerFunc(closeClients), target.ConnectTimeout, func() error {
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
// abort is closed if it takes longer than timeout; it must unblock nc.
func (c *Conn) handshake(nc net.Conn, abort io.Closer, e *endpoint, timeout time.Duration) (*ssh.Client, error) {
	clientCfg, cleanup, err := c.clientConfig(e)
	if err != nil {
		return nil, err
	}
	// The agent connection is only needed for the handshake itself.
	defer cleanup()
	var client *ssh.Client
	err = withTimeout(abort, timeout, func() error {
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

// clientConfig builds the ssh client config for one endpoint. cleanup must be
// called once the handshake is over.
func (c *Conn) clientConfig(e *endpoint) (*ssh.ClientConfig, func(), error) {
	auths, cleanup, err := c.authMethods(e)
	if err != nil {
		return nil, nil, err
	}
	hostKey, algos, err := c.hostKeyCallback(e)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return &ssh.ClientConfig{
		User:              e.User,
		Auth:              auths,
		HostKeyCallback:   hostKey,
		HostKeyAlgorithms: algos,
		Timeout:           e.ConnectTimeout,
	}, cleanup, nil
}

// authMethods lists the ways to authenticate to e. cleanup releases the
// ssh-agent connection, if one was opened.
func (c *Conn) authMethods(e *endpoint) (methods []ssh.AuthMethod, cleanup func(), err error) {
	// Kept apart from the named result, which the error returns below set to
	// nil before deferred calls run.
	closeAgent := func() {}
	defer func() {
		if err != nil {
			closeAgent()
		}
	}()

	if e.UseAgent {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			// The agent is a local process, but a wedged one would block the
			// handshake in a way closing the network connection cannot undo.
			// So the connect and every exchange with it share the handshake's
			// deadline, and the connection is closed afterwards rather than
			// leaked on every dial.
			timeout := e.ConnectTimeout
			if conn, err := net.DialTimeout("unix", sock, timeout); err == nil {
				if timeout > 0 {
					conn.SetDeadline(time.Now().Add(timeout))
				}
				methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
				closeAgent = func() { conn.Close() }
				c.log.Debugf("using ssh-agent at %s", sock)
			} else {
				c.log.Warnf("ssh-agent at %s unusable: %v", sock, err)
			}
		}
	}

	// Prompting is only allowed while making the very first connection, when
	// the user is watching. A reconnect runs in the background, or on an NFS
	// request with the dial lock held; a prompt there would block every
	// request behind a question nobody sees. Everything entered during the
	// first connection is remembered, so reconnects do not need to ask.
	interactive := c.neverConnected()

	explicitKeys := len(e.IdentityFiles) > 0
	for _, path := range identityFiles(e) {
		signer, err := c.loadKey(path, e.Passphrase, interactive)
		if err != nil {
			if explicitKeys {
				// Explicitly requested: a failure here is fatal.
				return nil, nil, err
			}
			c.log.Debugf("skipping key %s: %v", path, err)
			continue
		}
		c.log.Debugf("using key %s", path)
		methods = append(methods, ssh.PublicKeys(signer))
	}

	password := e.Password
	secretKey := e.User + "@" + e.Addr()
	if password == "" {
		password = secrets.password(secretKey)
	}
	if password == "" && e.AskPassword {
		if !interactive {
			return nil, nil, fmt.Errorf("%s needs a password, which can only be prompted for on the first connection (use SSHNFS_PASSWORD or a key)", secretKey)
		}
		p, perr := promptPassword(fmt.Sprintf("%s@%s password: ", e.User, e.Host))
		if perr != nil {
			return nil, nil, perr
		}
		password = p
		// Remembered per hop, so a jump host's password is not asked for
		// again when the chain is rebuilt.
		secrets.setPassword(secretKey, p)
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
		return nil, nil, fmt.Errorf("no usable ssh authentication method for %s (try -i, -agent or -ask-password)", e.Addr())
	}
	return methods, closeAgent, nil
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

// neverConnected reports whether no session has been established yet, which
// is the only time prompting on the terminal is allowed.
func (c *Conn) neverConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen == 0
}

// detachHelpers reports whether a ProxyCommand started now may be put in a
// process group of its own: unless this is the first connection and there is
// a terminal it might need to prompt on.
func (c *Conn) detachHelpers() bool {
	return !c.neverConnected() || !term.IsTerminal(int(syscall.Stdin))
}

// loadKey returns the signer for a key file, parsing (and if need be
// prompting for) it only the first time in this process.
func (c *Conn) loadKey(path, passphrase string, interactive bool) (ssh.Signer, error) {
	if s, ok := secrets.signer(path); ok {
		return s, nil
	}
	s, err := loadPrivateKey(path, passphrase, interactive)
	if err != nil {
		return nil, err
	}
	secrets.setSigner(path, s)
	return s, nil
}

// loadPrivateKey parses a key file. An encrypted key with no passphrase
// given is prompted for only when interactive is set.
func loadPrivateKey(path, passphrase string, interactive bool) (ssh.Signer, error) {
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
		if !interactive {
			return nil, fmt.Errorf("key %s is encrypted and no passphrase is known; prompting is only possible on the first connection (use SSHNFS_PASSPHRASE or ssh-agent)", path)
		}
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

// hostKeyCallback verifies host keys against known_hosts. It also returns the
// host key algorithms to ask for: those of the keys already on file for this
// host. Without that the server picks from its own preference list, and a
// host known only by its ed25519 key that prefers ecdsa fails with a scary
// "key mismatch" although nothing is wrong. nil means no preference.
func (c *Conn) hostKeyCallback(e *endpoint) (ssh.HostKeyCallback, []string, error) {
	if e.Insecure {
		c.log.Warnf("host key verification disabled for %s", e.Host)
		return ssh.InsecureIgnoreHostKey(), nil, nil
	}
	path := expandUser(e.KnownHostsFile)
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, nil, fmt.Errorf("known_hosts %s: %w (use -known-hosts or -insecure)", path, err)
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
	}, knownHostAlgorithms(cb, e.Addr()), nil
}

// knownHostAlgorithms lists the host key algorithms matching the keys on file
// for address. knownhosts has no lookup call, but checking a key that is on
// file for nobody fails with an error listing the keys that are.
func knownHostAlgorithms(cb ssh.HostKeyCallback, address string) []string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil
	}
	probe, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil
	}
	var kerr *knownhosts.KeyError
	if err := cb(address, &net.TCPAddr{IP: net.IPv4zero}, probe); !errors.As(err, &kerr) {
		return nil
	}
	var algos []string
	seen := map[string]bool{}
	add := func(a string) {
		if !seen[a] {
			seen[a] = true
			algos = append(algos, a)
		}
	}
	for _, k := range kerr.Want {
		switch t := k.Key.Type(); t {
		case ssh.KeyAlgoRSA:
			// One RSA key serves all three signature algorithms.
			add(ssh.KeyAlgoRSASHA512)
			add(ssh.KeyAlgoRSASHA256)
			add(ssh.KeyAlgoRSA)
		default:
			add(t)
		}
	}
	return algos
}

// promptState is the terminal state to put back if the process must exit
// while a prompt has echo turned off; exiting without it leaves the user's
// shell typing blind.
type promptState struct {
	fd int
	st *term.State
}

var activePrompt atomic.Pointer[promptState]

// abortPrompt restores the terminal if a prompt is waiting, and reports
// whether one was. Prompts only happen during the first connections, before
// anything is mounted, so the caller can simply exit.
func abortPrompt() bool {
	p := activePrompt.Swap(nil)
	if p == nil {
		return false
	}
	term.Restore(p.fd, p.st)
	fmt.Fprintln(os.Stderr)
	return true
}

func promptPassword(prompt string) (string, error) {
	fd := int(syscall.Stdin)
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal, cannot prompt for a secret")
	}
	if st, err := term.GetState(fd); err == nil {
		activePrompt.Store(&promptState{fd: fd, st: st})
		defer activePrompt.Store(nil)
	}
	// Let queued log lines out first, so the prompt is the last thing on
	// the screen rather than buried under them.
	stderr.Flush(time.Second)
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
