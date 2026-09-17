Q: Embed etcd in your app/service?

A: Yes. etcd is written in Go and ships a supported package for this, `go.etcd.io/etcd/server/v3/embed`. You import it like any library, and a full etcd member (Raft, the bbolt storage backend, the gRPC API, and optionally peer and client listeners) runs inside your process. k3s ships this way in production, and many projects use it in integration tests.

```go
import (
    "go.etcd.io/etcd/server/v3/embed"
    "go.etcd.io/etcd/server/v3/etcdserver/api/v3client"
)

cfg := embed.NewConfig()
cfg.Name = "node1"
cfg.Dir = "/var/lib/mysvc/etcd"
// cfg.ListenPeerUrls, ListenClientUrls, AdvertiseClientUrls, InitialCluster, TLS...

e, err := embed.StartEtcd(cfg)
if err != nil { log.Fatal(err) }
defer e.Close()

select {
case <-e.Server.ReadyNotify():
case <-time.After(60 * time.Second):
    e.Server.Stop()
    log.Fatal("etcd did not become ready")
}

cli := v3client.New(e.Server) // in-process clientv3.Client, no network hop
```

`v3client` gives you the normal `clientv3` API, so `concurrency.Election`, mutexes, leases, watches, and transactions all work unchanged. You can still expose a client URL so `etcdctl` and other processes can connect.

## Advantages

- **Deployment is simpler.** You ship one binary with one lifecycle and no separate etcd cluster to provision, version, or monitor. This matters most for on-prem, edge, or appliance-style products where you can't assume infrastructure exists.
- **Local access is fast.** The `v3client` path skips TCP, TLS, and gRPC serialization. Linearizable reads still go through Raft's ReadIndex, so the savings are the transport overhead only.
- **You get a proven consensus layer.** Leases, watches, MVCC, and transactions come from one of the most heavily exercised Raft implementations in existence, instead of something you wrote yourself.
- **Your code can see Raft state directly.** Calls like `e.Server.Leader()` and `e.Server.ID()` are local, which makes "do this work only on the leader node" logic straightforward.
- **Configuration can be shared.** etcd can use the same certificates, identity, and config system as the rest of your service, instead of a separately managed PKI.
- **Integration tests become hermetic.** A real single-node etcd in a temp dir starts in about a second, with no Docker required.

## Disadvantages

- **Your service's failures become etcd's failures.** A panic, OOM kill, or crash loop in your code takes down a quorum member. If three replicas of your service share a bug, you lose quorum and the cluster's data availability, not just your service.
- **Garbage collection pauses can trigger elections.** Raft heartbeats (100 ms by default) run on the same Go runtime as your application. A large or GC-heavy heap can cause missed heartbeats, spurious leader elections, and latency spikes. This is the most common real-world problem. You can raise the heartbeat and election timeouts, but that slows failover.
- **Disk contention hurts writes.** etcd's write latency is bounded by WAL fsync. If your service does heavy disk I/O on the same volume, etcd suffers, which is why etcd's guidance recommends dedicated fast disks.
- **Dependency conflicts are real.** etcd server pulls in specific versions of grpc, grpc-gateway, OpenTelemetry, Prometheus client, zap, bbolt, and others, and your `go.mod` must reconcile with them. Historically this has been genuinely painful: gRPC breaking changes have blocked upgrades for months. etcd also registers metrics on the global Prometheus registry and sets gRPC's global logger. The server library also adds tens of MB to your binary.
- **Service scaling is tied to quorum.** Voting members should be 3 or 5, so you can't autoscale your service to 12 replicas and have them all be etcd voters. You end up with a hybrid: a few voters, with the rest as learners or remote clients. That reintroduces the topology split you were trying to avoid.
- **Upgrades are tied to your release cadence.** Every service release is also an etcd member upgrade, so rolling deploys must respect etcd's version-skew rules and on-disk format migrations. Rolling back your service can mean an etcd downgrade, which is restricted.
- **Operations become your job.** Member add, remove, and replace, snapshots and restore, disaster recovery from lost quorum, compaction, defragmentation, and the backend size quota (2 GB default, roughly 8 GB practical ceiling) all need to be handled by your code or runbooks. With an external or managed etcd, someone else may already own this.
- **Your process exposes a bigger attack surface.** It now listens on peer ports, and a vulnerability in etcd's gRPC or HTTP surface is a vulnerability in your service.
- **Observability gets mixed.** etcd's metrics, logs, and pprof profiles are interleaved with yours, which makes it harder to tell whether a latency problem is etcd or your code.

## When it tends to make sense

Embedding works well for a small, fixed number of nodes (1, 3, or 5), for software shipped to environments you don't control, for control-plane style components with modest heaps and modest data, and for tests. It works poorly for horizontally autoscaled services, GC-heavy or large-heap processes, datasets approaching multiple GB, or organizations where a platform team already runs etcd.

If you want replicated state inside your process but etcd's full server feels too heavy, there's a middle ground. You can embed only `go.etcd.io/raft/v3` with your own state machine and storage (bbolt or Pebble). You get the same consensus core with far fewer dependencies and no gRPC surface, at the cost of writing the log storage, snapshotting, and transport yourself. Alternatively, `hashicorp/raft` offers a more batteries-included version of that approach.
@