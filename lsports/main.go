// lsports lists sockets that are listening on IP ports on a Linux host,
// together with the processes that own them and the command line each
// process was started with.
//
// It works by reading /proc/net/{tcp,tcp6,udp,udp6} to get the socket
// inode numbers, then scanning /proc/<pid>/fd/* for symlinks of the form
// "socket:[<inode>]" to attribute each socket to one or more processes.
//
// Sockets owned by other users can only be attributed to a process when
// running as root; otherwise they are still listed, with an empty process
// column.
package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

const procRoot = "/proc"

// TCP socket states as exported by the kernel in /proc/net/tcp.
var tcpStates = map[uint64]string{
	0x01: "ESTABLISHED",
	0x02: "SYN_SENT",
	0x03: "SYN_RECV",
	0x04: "FIN_WAIT1",
	0x05: "FIN_WAIT2",
	0x06: "TIME_WAIT",
	0x07: "CLOSE",
	0x08: "CLOSE_WAIT",
	0x09: "LAST_ACK",
	0x0A: "LISTEN",
	0x0B: "CLOSING",
	0x0C: "NEW_SYN_RECV",
}

// Socket is one row of /proc/net/{tcp,tcp6,udp,udp6}.
type Socket struct {
	Proto string         `json:"proto"`
	Local netip.AddrPort `json:"local"`
	State string         `json:"state"`
	Inode uint64         `json:"inode"`
	UID   uint32         `json:"uid"`
	User  string         `json:"user,omitempty"`
}

// Process is a process holding a file descriptor for a socket.
type Process struct {
	PID     int      `json:"pid"`
	Comm    string   `json:"comm"`
	Exe     string   `json:"exe,omitempty"`
	Cmdline []string `json:"cmdline"`
}

// Entry pairs a socket with every process that has it open. More than one
// process is normal: forking servers share the listening fd, and separate
// processes can bind the same port with SO_REUSEPORT.
type Entry struct {
	Socket
	Procs []Process `json:"processes"`
}

func main() {
	var (
		udp      = flag.Bool("udp", false, "also list bound UDP sockets")
		all      = flag.Bool("a", false, "list all TCP sockets, not just those in LISTEN state")
		asJSON   = flag.Bool("json", false, "emit JSON instead of a table")
		v4only   = flag.Bool("4", false, "IPv4 only")
		v6only   = flag.Bool("6", false, "IPv6 only")
		procPath = flag.String("proc", procRoot, "path to a procfs mount")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags]\n\nLists listening IP sockets and the processes that own them.\n\nflags:\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := run(*procPath, *udp, *all, *asJSON, *v4only, *v6only); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(os.Args[0]), err)
		os.Exit(1)
	}
}

func run(proc string, udp, all, asJSON, v4only, v6only bool) error {
	files := []struct{ path, proto string }{
		{filepath.Join(proc, "net", "tcp"), "tcp"},
		{filepath.Join(proc, "net", "tcp6"), "tcp6"},
	}
	if udp {
		files = append(files,
			struct{ path, proto string }{filepath.Join(proc, "net", "udp"), "udp"},
			struct{ path, proto string }{filepath.Join(proc, "net", "udp6"), "udp6"},
		)
	}

	var sockets []Socket
	for _, f := range files {
		ss, err := readSockets(f.path, f.proto)
		if errors.Is(err, fs.ErrNotExist) {
			continue // e.g. kernel built without IPv6
		}
		if err != nil {
			return err
		}
		for _, s := range ss {
			if !keep(s, all, v4only, v6only) {
				continue
			}
			sockets = append(sockets, s)
		}
	}

	// Attribute sockets to processes with a single pass over /proc.
	wanted := make(map[uint64]bool, len(sockets))
	for _, s := range sockets {
		wanted[s.Inode] = true
	}
	owners, unreadable, err := scanProcesses(proc, wanted)
	if err != nil {
		return err
	}

	users := map[uint32]string{}
	entries := make([]Entry, 0, len(sockets))
	for _, s := range sockets {
		s.User = lookupUser(users, s.UID)
		procs := owners[s.Inode]
		sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
		entries = append(entries, Entry{Socket: s, Procs: procs})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Local.Port() != b.Local.Port() {
			return a.Local.Port() < b.Local.Port()
		}
		if a.Proto != b.Proto {
			return a.Proto < b.Proto
		}
		return a.Local.Addr().Less(b.Local.Addr())
	})

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	printTable(entries)
	if unreadable > 0 && os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "\nnote: %d process(es) were not readable; re-run as root to attribute every socket\n", unreadable)
	}
	return nil
}

func keep(s Socket, all, v4only, v6only bool) bool {
	if strings.HasPrefix(s.Proto, "tcp") && !all && s.State != "LISTEN" {
		return false
	}
	if v4only && !s.Local.Addr().Is4() {
		return false
	}
	if v6only && s.Local.Addr().Is4() {
		return false
	}
	return true
}

// readSockets parses one of the /proc/net socket tables.
//
//	sl  local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode ...
//	 0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 12345 1 ...
func readSockets(path, proto string) ([]Socket, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 {
		lines = lines[1:] // header
	}

	var out []Socket
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		addr, err := parseAddrPort(fields[1])
		if err != nil {
			continue
		}
		st, err := strconv.ParseUint(fields[3], 16, 8)
		if err != nil {
			continue
		}
		uid, err := strconv.ParseUint(fields[7], 10, 32)
		if err != nil {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}

		state := tcpStates[st]
		if state == "" {
			state = fmt.Sprintf("UNKNOWN(0x%02x)", st)
		}
		if strings.HasPrefix(proto, "udp") {
			// UDP has no real state machine; 07 (CLOSE) just means unconnected.
			if st == 0x07 {
				state = "BOUND"
			} else if st == 0x01 {
				state = "CONNECTED"
			}
		}

		out = append(out, Socket{
			Proto: proto,
			Local: addr,
			State: state,
			Inode: inode,
			UID:   uint32(uid),
		})
	}
	return out, nil
}

// parseAddrPort decodes the "HEXADDR:HEXPORT" form used by procfs. The
// address is a sequence of 32-bit host-order words, so each 4-byte group
// is byte-swapped on little-endian machines — which is what the kernel
// emits and what every consumer of this file assumes.
func parseAddrPort(s string) (netip.AddrPort, error) {
	host, portStr, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("malformed address %q", s)
	}
	port, err := strconv.ParseUint(portStr, 16, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	raw, err := hex.DecodeString(host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	if len(raw)%4 != 0 || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, fmt.Errorf("unexpected address width %d in %q", len(raw), s)
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}

	var addr netip.Addr
	if len(raw) == 4 {
		addr = netip.AddrFrom4([4]byte(raw))
	} else {
		addr = netip.AddrFrom16([16]byte(raw))
		if v4 := addr.Unmap(); v4.Is4() {
			addr = v4 // ::ffff:a.b.c.d
		}
	}
	return netip.AddrPortFrom(addr, uint16(port)), nil
}

// scanProcesses walks /proc once and returns, for every wanted socket inode,
// the processes holding a descriptor for it. The second return value counts
// processes whose fd directory could not be read (almost always EACCES).
func scanProcesses(proc string, wanted map[uint64]bool) (map[uint64][]Process, int, error) {
	dirs, err := os.ReadDir(proc)
	if err != nil {
		return nil, 0, err
	}

	owners := make(map[uint64][]Process)
	denied := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}

		fdDir := filepath.Join(proc, d.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				denied++
			}
			continue // permission denied, or the process exited under us
		}

		var inodes []uint64
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			ino, ok := socketInode(target)
			if !ok || !wanted[ino] {
				continue
			}
			inodes = append(inodes, ino)
		}
		if len(inodes) == 0 {
			continue
		}

		p := readProcess(proc, pid)
		for _, ino := range inodes {
			owners[ino] = append(owners[ino], p)
		}
	}
	return owners, denied, nil
}

// socketInode extracts N from an fd symlink target of the form "socket:[N]".
func socketInode(target string) (uint64, bool) {
	const prefix = "socket:["
	if !strings.HasPrefix(target, prefix) || !strings.HasSuffix(target, "]") {
		return 0, false
	}
	n, err := strconv.ParseUint(target[len(prefix):len(target)-1], 10, 64)
	return n, err == nil
}

func readProcess(proc string, pid int) Process {
	p := Process{PID: pid}
	dir := filepath.Join(proc, strconv.Itoa(pid))

	if b, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
		p.Comm = strings.TrimSpace(string(b))
	}
	if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		p.Exe = exe
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		// NUL-separated, usually with a trailing NUL.
		for _, arg := range strings.Split(strings.TrimRight(string(b), "\x00"), "\x00") {
			p.Cmdline = append(p.Cmdline, arg)
		}
	}
	if len(p.Cmdline) == 1 && p.Cmdline[0] == "" {
		p.Cmdline = nil // kernel thread: empty cmdline
	}
	return p
}

func lookupUser(cache map[uint32]string, uid uint32) string {
	if name, ok := cache[uid]; ok {
		return name
	}
	name := strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	cache[uid] = name
	return name
}

func printTable(entries []Entry) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROTO\tLOCAL ADDRESS\tSTATE\tUSER\tPID\tCOMMAND")
	for _, e := range entries {
		local := formatAddrPort(e.Local)
		if len(e.Procs) == 0 {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t-\t?\n", e.Proto, local, e.State, e.User)
			continue
		}
		for _, p := range e.Procs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
				e.Proto, local, e.State, e.User, p.PID, formatCmdline(p))
		}
	}
	w.Flush()
}

func formatAddrPort(ap netip.AddrPort) string {
	addr := ap.Addr()
	host := addr.String()
	if addr.IsUnspecified() {
		host = "*"
	}
	if addr.Is6() && host != "*" {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s:%d", host, ap.Port())
}

// formatCmdline renders argv so that it stays readable on one line while
// remaining unambiguous: arguments containing spaces or quotes are quoted.
func formatCmdline(p Process) string {
	if len(p.Cmdline) == 0 {
		return "[" + p.Comm + "]" // kernel thread, like ps(1)
	}
	parts := make([]string, 0, len(p.Cmdline))
	for _, a := range p.Cmdline {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\") {
			parts = append(parts, strconv.Quote(a))
		} else {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, " ")
}
