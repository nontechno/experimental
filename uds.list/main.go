// udslist: list Unix domain sockets on Linux with their inodes and owning
// processes (PID, command name, executable).
//
// Data sources:
//   - /proc/net/unix          kernel table of all UDS in the current net namespace
//   - /proc/<pid>/fd/*        symlinks "socket:[inode]" map inodes to processes
//   - /proc/<pid>/{comm,exe,cmdline}
//
// Run as root (or with CAP_SYS_PTRACE) to see sockets owned by other users.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

const soAcceptCon = 0x00010000 // __SO_ACCEPTCON: socket is listening

// Socket is one row of /proc/net/unix.
type Socket struct {
	Inode     uint64  `json:"inode"`
	Type      string  `json:"type"`
	State     string  `json:"state"`
	Listening bool    `json:"listening"`
	Kind      string  `json:"kind"` // path | abstract | unnamed
	Path      string  `json:"path,omitempty"`
	RefCount  uint64  `json:"refcount"`
	Owners    []Owner `json:"owners"`
}

// Owner is a process holding an fd that refers to a socket.
type Owner struct {
	PID     int    `json:"pid"`
	Comm    string `json:"comm"`
	Exe     string `json:"exe,omitempty"`
	Cmdline string `json:"cmdline,omitempty"`
	FDs     []int  `json:"fds"`
}

var typeNames = map[uint64]string{1: "STREAM", 2: "DGRAM", 5: "SEQPACKET"}

var stateNames = map[uint64]string{
	1: "UNCONNECTED",
	2: "CONNECTING",
	3: "CONNECTED",
	4: "DISCONNECTING",
}

func main() {
	var (
		listening = flag.Bool("l", false, "only listening sockets")
		asJSON    = flag.Bool("json", false, "output JSON")
		pidFilter = flag.Int("p", 0, "only sockets owned by this PID")
		nameSub   = flag.String("n", "", "only sockets whose path or owner command contains this substring")
		owned     = flag.Bool("owned", false, "hide sockets with no discoverable owner")
		wide      = flag.Bool("w", false, "show full command line instead of exe path")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\nLists Unix domain sockets with inode, PID and owning application.\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	socks, err := readSockets("/proc/net/unix")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	byInode := make(map[uint64]*Socket, len(socks))
	for _, s := range socks {
		byInode[s.Inode] = s
	}

	skipped, err := attachOwners(byInode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if skipped > 0 && os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "warning: could not inspect %d process(es); run as root for complete results\n", skipped)
	}

	var out []*Socket
	for _, s := range socks {
		if matches(s, *listening, *pidFilter, *nameSub, *owned) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Inode < out[j].Inode })

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	printTable(out, *wide)
}

// readSockets parses /proc/net/unix.
//
// Format: Num RefCount Protocol Flags Type St Inode [Path]
func readSockets(path string) ([]*Socket, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var socks []*Socket
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		if first { // header
			first = false
			continue
		}
		s, ok := parseLine(sc.Text())
		if ok {
			socks = append(socks, s)
		}
	}
	return socks, sc.Err()
}

func parseLine(line string) (*Socket, bool) {
	// Take the first 7 whitespace-separated fields; whatever remains is the
	// path (which may itself contain spaces).
	fields := make([]string, 0, 7)
	rest := line
	for len(fields) < 7 {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return nil, false
		}
		idx := strings.IndexAny(rest, " \t")
		if idx < 0 {
			fields = append(fields, rest)
			rest = ""
			break
		}
		fields = append(fields, rest[:idx])
		rest = rest[idx:]
	}
	if len(fields) < 7 {
		return nil, false
	}
	path := strings.TrimSpace(rest)

	ref, _ := strconv.ParseUint(fields[1], 16, 64)
	flags, _ := strconv.ParseUint(fields[3], 16, 64)
	typ, _ := strconv.ParseUint(fields[4], 16, 64)
	st, _ := strconv.ParseUint(fields[5], 16, 64)
	inode, err := strconv.ParseUint(fields[6], 10, 64)
	if err != nil {
		return nil, false
	}

	tn, ok := typeNames[typ]
	if !ok {
		tn = fmt.Sprintf("TYPE(%d)", typ)
	}
	listening := flags&soAcceptCon != 0
	sn, ok := stateNames[st]
	if !ok {
		sn = fmt.Sprintf("ST(%d)", st)
	}
	if listening {
		sn = "LISTEN"
	}

	kind := "unnamed"
	switch {
	case strings.HasPrefix(path, "@"):
		kind = "abstract"
	case path != "":
		kind = "path"
	}

	return &Socket{
		Inode:     inode,
		Type:      tn,
		State:     sn,
		Listening: listening,
		Kind:      kind,
		Path:      path,
		RefCount:  ref,
	}, true
}

// attachOwners walks /proc/<pid>/fd and fills in Owners for each socket.
// It returns the number of processes whose fd table could not be read.
func attachOwners(byInode map[uint64]*Socket) (skipped int, err error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil {
			continue
		}
		fdDir := fmt.Sprintf("/proc/%d/fd", pid)
		fds, rerr := os.ReadDir(fdDir)
		if rerr != nil {
			if !os.IsNotExist(rerr) { // process exiting is not a "skip"
				skipped++
			}
			continue
		}

		var info *Owner             // lazily populated, shared across this pid's sockets
		perSock := map[uint64]int{} // inode -> index into that socket's Owners

		for _, fd := range fds {
			target, lerr := os.Readlink(fdDir + "/" + fd.Name())
			if lerr != nil {
				continue
			}
			inode, ok := parseSocketLink(target)
			if !ok {
				continue
			}
			s, ok := byInode[inode]
			if !ok {
				continue // TCP/UDP/etc., or a different net namespace
			}
			fdNum, _ := strconv.Atoi(fd.Name())

			idx, seen := perSock[inode]
			if !seen {
				if info == nil {
					info = describeProc(pid)
				}
				cp := *info
				cp.FDs = nil
				s.Owners = append(s.Owners, cp)
				idx = len(s.Owners) - 1
				perSock[inode] = idx
			}
			s.Owners[idx].FDs = append(s.Owners[idx].FDs, fdNum)
		}
	}
	for _, s := range byInode {
		sort.Slice(s.Owners, func(i, j int) bool { return s.Owners[i].PID < s.Owners[j].PID })
	}
	return skipped, nil
}

// parseSocketLink extracts the inode from "socket:[12345]".
func parseSocketLink(link string) (uint64, bool) {
	const prefix = "socket:["
	if !strings.HasPrefix(link, prefix) || !strings.HasSuffix(link, "]") {
		return 0, false
	}
	n, err := strconv.ParseUint(link[len(prefix):len(link)-1], 10, 64)
	return n, err == nil
}

func describeProc(pid int) *Owner {
	o := &Owner{PID: pid}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		o.Comm = strings.TrimSpace(string(b))
	}
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		o.Exe = strings.TrimSuffix(exe, " (deleted)")
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		o.Cmdline = strings.TrimSpace(strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", " "))
	}
	return o
}

func matches(s *Socket, listening bool, pid int, sub string, owned bool) bool {
	if listening && !s.Listening {
		return false
	}
	if owned && len(s.Owners) == 0 {
		return false
	}
	if pid != 0 {
		found := false
		for _, o := range s.Owners {
			if o.PID == pid {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if sub != "" {
		sub = strings.ToLower(sub)
		hit := strings.Contains(strings.ToLower(s.Path), sub)
		for _, o := range s.Owners {
			if strings.Contains(strings.ToLower(o.Comm), sub) ||
				strings.Contains(strings.ToLower(o.Exe), sub) {
				hit = true
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func printTable(socks []*Socket, wide bool) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "INODE\tTYPE\tSTATE\tPID\tCOMM\tFD\tPATH\tAPP")
	for _, s := range socks {
		path := s.Path
		if path == "" {
			path = "-"
		}
		if len(s.Owners) == 0 {
			fmt.Fprintf(tw, "%d\t%s\t%s\t-\t-\t-\t%s\t-\n", s.Inode, s.Type, s.State, path)
			continue
		}
		for _, o := range s.Owners {
			app := o.Exe
			if wide && o.Cmdline != "" {
				app = o.Cmdline
			}
			if app == "" {
				app = "-"
			}
			comm := o.Comm
			if comm == "" {
				comm = "-"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
				s.Inode, s.Type, s.State, o.PID, comm, joinInts(o.FDs), path, app)
		}
	}
	tw.Flush()
}

func joinInts(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}
