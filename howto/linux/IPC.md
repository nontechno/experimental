Here's a comprehensive taxonomy of IPC / external notification mechanisms:

---

## Signals & OS Primitives

- **POSIX signals** — `SIGUSR1`, `SIGUSR2`, `SIGHUP`, `SIGTERM`, etc. (no data blob, just signal number)
- **signalfd** (Linux) — signals delivered as readable file descriptors with metadata
- **eventfd** (Linux) — lightweight kernel counter, readable fd, used for wakeup notifications
- **pidfd** (Linux 5.3+) — file descriptor referring to a process, can be polled for process events
- **kqueue** (BSD/macOS) — kernel event notification (file changes, process events, signals, timers, sockets)
- **inotify / fanotify** (Linux) — filesystem event notifications
- **timerfd** (Linux) — timer expiry delivered as readable fd

---

## Pipes & FIFOs

- **Anonymous pipe** — `pipe(2)`, between related processes (fork)
- **Named pipe / FIFO** — `mkfifo`, any process can open by name
- **socketpair** — bidirectional pipe, like UDS but unnamed

---

## Sockets

- **Unix Domain Socket (UDS)** — stream (`SOCK_STREAM`) or datagram (`SOCK_DGRAM`)
- **TCP** — unicast, reliable
- **UDP** — unicast, unreliable, low latency
- **UDP multicast** — one sender → group of receivers (LAN or routed)
- **UDP broadcast** — `255.255.255.255` or subnet broadcast
- **SCTP** — multi-stream, message-oriented, reliable (rarer)
- **DCCP** — datagram with congestion control (very niche)
- **Raw sockets** — `SOCK_RAW`, arbitrary protocol (e.g., ICMP, custom Ethernet)
- **Netlink socket** (Linux) — kernel↔userspace for network config, routing, auditing, etc.
- **AF_XDP** — kernel bypass, near-zero-copy packet reception
- **vsock** (`AF_VSOCK`) — VM↔hypervisor, no network stack needed

---

## Shared Memory

- **POSIX shared memory** — `shm_open` + `mmap`, requires separate notification mechanism
- **SysV shared memory** — `shmget`/`shmat`, older API, same caveat
- **memfd** (Linux) — anonymous file in memory, sharable via fd passing
- **`mmap` of a file** — processes share mapped region; changes visible instantly
- **huge pages / hugetlbfs** — shared memory with explicit huge page backing

(Shared memory alone doesn't notify — pair with: eventfd, futex, semaphore, pipe, or signal)

---

## Semaphores & Synchronization Primitives (as notification)

- **POSIX named semaphore** — `sem_open`, cross-process wait/post
- **SysV semaphore** — `semget`/`semop`
- **futex** (Linux) — fast userspace mutex/condvar, kernel-assisted wait; `FUTEX_WAKE` across processes via shared memory
- **`pthread_cond_t` in shared memory** — with `PTHREAD_PROCESS_SHARED`

---

## Message Queues

- **POSIX message queue** — `mq_open`, supports async notification via signal or thread
- **SysV message queue** — `msgget`/`msgsnd`/`msgrcv`
- **D-Bus** — Linux desktop IPC bus; method calls, signals, property changes
- **kdbus / dbus-broker** — kernel-accelerated D-Bus
- **Binder** (Android) — kernel IPC used throughout Android (also accessible on Linux)

---

## HTTP-Layer Protocols

- **HTTP short polling** — client repeatedly GETs
- **HTTP long polling** — server holds response open until event
- **SSE (Server-Sent Events)** — HTTP/1.1 chunked stream, server pushes events
- **WebSocket** — full-duplex over HTTP upgrade
- **HTTP/2 server push** — server pushes resources proactively
- **HTTP/3 / QUIC streams** — multiplexed, low-latency, stream-per-message possible
- **gRPC server streaming / bidirectional streaming**
- **GraphQL subscriptions** (over WebSocket or SSE)

---

## Message Brokers / Queues (external process)

- **AMQP** — RabbitMQ, etc.
- **MQTT** — pub/sub for IoT; broker-mediated, very lightweight
- **NATS** — lightweight pub/sub with subjects
- **Kafka / Redpanda** — log-based, consumer-pull but push-notify possible
- **Redis pub/sub / keyspace notifications** — subscribe to channels or key events
- **ZeroMQ (ØMQ)** — brokerless, socket patterns (PUB/SUB, PUSH/PULL, PAIR)
- **nanomsg / NNG** — ZeroMQ successor, similar patterns

---

## Hardware / Physical Layer

- **Serial port** (`/dev/ttyS*`, `/dev/ttyUSB*`) — UART, RS-232, RS-485
- **USB** — via `libusb`, interrupt/bulk/isochronous transfers; `udev` events for plug/unplug
- **Bluetooth Classic** — RFCOMM (serial-like), L2CAP (raw)
- **BLE (Bluetooth Low Energy)** — GATT notifications/indications from peripheral
- **Raw Ethernet** — `AF_PACKET`, `SOCK_RAW`, custom EtherType
- **CAN bus** — `AF_CAN` / `SOCK_RAW CAN_RAW`; automotive/industrial
- **SPI / I²C / GPIO** — via `/dev/spidev*`, `/dev/i2c-*`, sysfs/libgpiod; hardware interrupts
- **PCIe / MMIO** — DMA + MSI/MSI-X interrupts (driver-level)
- **Infiniband / RDMA** — `libibverbs`, completion queue events, extremely low latency

---

## Platform / OS Specific

- **Windows:** named pipes, mailslots, COM/DCOM, WM_* messages (HWND), `ReadDirectoryChangesW`, `WaitForMultipleObjects`, RPC
- **macOS:** XPC services, `NSDistributedNotificationCenter`, Mach ports, Apple Events, `CFRunLoop` sources, IOKit notifications
- **Linux kernel modules:** `netlink`, `ioctl`, character device (`read`/`poll`), `kobject_uevent` → udev

---

## File System as IPC

- **inotify / fanotify / kqueue** — watch a file/dir; writer changes file, reader gets notified
- **Lock files** — polling or inotify on a lock file
- **`/proc` or `/sys` polling** — watch kernel-exported state
- **`memfd` + seal** — pass anonymous file between processes via `SCM_RIGHTS`

---

## Kernel / System-Level

- **`ioctl`** on a device fd — request + response in one syscall
- **`ptrace`** — process tracing/notification (debugger use)
- **`seccomp-unotify`** (Linux 5.0+) — seccomp filter hands syscall to supervisor process for decision
- **`userfaultfd`** (Linux) — page fault notification delivered to handling process
- **perf events** — `perf_event_open`, overflow notification via signal or ring buffer
- **audit subsystem** — kernel sends audit records via netlink
- **`io_uring`** — async I/O with completion ring buffer; can receive notifications without syscall per event

---

## Unusual / Niche

- **ICMP** — "ping" as an out-of-band signal (crude but it works)
- **DNS TXT records + polling** — distributed "push" via DNS changes
- **`SIGPIPE` + broken pipe** — implicit notification that reader died
- **POSIX timers** (`timer_create`) — deliver via signal or thread
- **`prctl(PR_SET_PDEATHSIG)`** — child gets signal when parent dies
- **`close_range` / fd closing** — `inotify` on `/proc/PID/fd` or `pidfd_poll`
- **Clock synchronization jumps** — `CLOCK_REALTIME` discontinuity detected via `timerfd` with `TFD_TIMER_CANCEL_ON_SET`
- **NFC** — `AF_NFC`, tag detection events
- **Infrared** — `lirc` / `/dev/lirc*`, IR remote signals
- **Power supply / ACPI events** — via `udev`, `acpid`, or `/sys/class/power_supply`
- **Thermal / hwmon events** — threshold crossing via netlink or sysfs polling
- **`POSIX_SPAWN_*` + `waitpid`** — child process exit as notification to parent

---

The rough mental model is: **everything is either a file descriptor you can `poll`/`epoll`/`select` on, a signal, a shared memory region needing a separate poke, or a physical medium with a driver that ultimately surfaces as one of those.**
