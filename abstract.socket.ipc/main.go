//go:build linux

// Single-instance app: the first process becomes the primary, later ones
// forward their arguments to it over a Linux abstract Unix socket.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	appName = "myapp"
	timeout = 5 * time.Second
	maxMsg  = 64 << 10
)

type Request struct {
	PID  int      `json:"pid"`
	Cwd  string   `json:"cwd"`
	Args []string `json:"args"`
}

type Response struct {
	PrimaryPID int    `json:"primary_pid"`
	Message    string `json:"message"`
}

func main() {
	// A leading '@' makes Go use the abstract namespace: no file on disk,
	// and the kernel frees the name when the owner exits.
	addr := fmt.Sprintf("@%s-%d", appName, os.Getuid())

	// Retry covers the primary exiting between our Listen and Dial.
	for attempt := 0; attempt < 3; attempt++ {
		l, err := net.Listen("unix", addr)
		if err == nil {
			if err := runPrimary(l); err != nil {
				log.Fatal(err)
			}
			return
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			log.Fatalf("listen: %v", err)
		}

		conn, err := net.DialTimeout("unix", addr, timeout)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if err := runSecondary(conn.(*net.UnixConn)); err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Fatal("could not become primary or reach it")
}

func runPrimary(l net.Listener) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		l.Close()
	}()

	log.Printf("primary running, pid %d (Ctrl-C to quit)", os.Getpid())
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go handle(conn.(*net.UnixConn))
	}
}

func handle(c *net.UnixConn) {
	defer c.Close()

	// Abstract sockets have no file permissions, so check who connected.
	if err := checkPeer(c); err != nil {
		log.Printf("rejected: %v", err)
		return
	}
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		log.Printf("deadline: %v", err)
		return
	}

	var req Request
	if err := json.NewDecoder(io.LimitReader(c, maxMsg)).Decode(&req); err != nil {
		log.Printf("decode: %v", err)
		return
	}
	log.Printf("from pid %d, cwd %s, args %q", req.PID, req.Cwd, req.Args)

	resp := Response{PrimaryPID: os.Getpid(), Message: fmt.Sprintf("got %d arg(s)", len(req.Args))}
	if err := json.NewEncoder(c).Encode(resp); err != nil {
		log.Printf("encode: %v", err)
	}
}

func runSecondary(c *net.UnixConn) error {
	defer c.Close()

	// Another user could have taken the name first; don't send them data.
	if err := checkPeer(c); err != nil {
		return err
	}
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	req := Request{PID: os.Getpid(), Cwd: cwd, Args: os.Args[1:]}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	var resp Response
	if err := json.NewDecoder(io.LimitReader(c, maxMsg)).Decode(&resp); err != nil {
		return fmt.Errorf("receive: %w", err)
	}
	fmt.Printf("primary (pid %d) replied: %s\n", resp.PrimaryPID, resp.Message)
	return nil
}

// checkPeer logs the SO_PEERCRED identity of the other end and requires that
// it runs as our UID.
func checkPeer(c *net.UnixConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return fmt.Errorf("SO_PEERCRED: %w", credErr)
	}
	log.Printf("peer: %s", describePeer(cred))
	if int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("peer uid %d does not match ours (%d)", cred.Uid, os.Getuid())
	}
	return nil
}

// describePeer resolves the IDs to names and adds process details from /proc.
// The /proc data is informational only: the PID may have been reused, and the
// process controls its own command line. Don't use it for authorization.
func describePeer(cred *syscall.Ucred) string {
	uid := strconv.FormatUint(uint64(cred.Uid), 10)
	gid := strconv.FormatUint(uint64(cred.Gid), 10)

	userName, groupName := "?", "?"
	if u, err := user.LookupId(uid); err == nil {
		userName = u.Username
	}
	if g, err := user.LookupGroupId(gid); err == nil {
		groupName = g.Name
	}
	out := fmt.Sprintf("pid=%d uid=%s(%s) gid=%s(%s)", cred.Pid, uid, userName, gid, groupName)

	// PID 0 means the peer isn't visible in our PID namespace.
	if cred.Pid <= 0 {
		return out
	}
	proc := fmt.Sprintf("/proc/%d/", cred.Pid)

	// Reading exe usually fails for another user's process unless we're privileged.
	if exe, err := os.Readlink(proc + "exe"); err == nil {
		out += fmt.Sprintf(" exe=%q", exe)
	} else {
		out += " exe=?"
	}
	if b, err := os.ReadFile(proc + "cmdline"); err == nil {
		args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
		out += fmt.Sprintf(" cmdline=%q", args)
	} else {
		out += " cmdline=?"
	}
	return out
}
