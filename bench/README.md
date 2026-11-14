# Benchmarking zdapd's ZFS access

zdapd answers every REST request by opening the whole zpool recursively and
filtering the result in Go. This directory holds the tooling to measure what
that costs per endpoint, so the move to
[zfs channel programs](https://openzfs.github.io/openzfs-docs/man/master/8/zfs-program.8.html)
can be driven by numbers.

There are three measurements, from cheapest to most authoritative:

| | what it measures | needs |
|---|---|---|
| `go test ./internal/zfs -bench` | the ZFS layer on its own | a readable pool |
| `cmd/zdap-bench` | ZFS work per REST endpoint | a running zdapd |
| `bench/zfs-ioctl-bench.sh` | real `/dev/zfs` ioctls per endpoint | `strace` |

## The counters

`internal/zfs/zfsstat` holds process-global counters that `internal/zfs`
increments. Keys are prefixed by kind:

- `op.*` — one call out to ZFS: `op.channel_program` and `op.zpool_list` for the
  migrated paths, `op.dataset_open_recursive`, `op.get_user_property` and the
  rest for what still goes through go-libzfs.
- `vol.*` — how much came out: `vol.dataset_handles`, `vol.property_table_loads`,
  `vol.property_values`, `vol.kernel_ioctls_est`.

zdapd attributes the delta of these counters to the route that caused it and
serves the result on `GET /debug/zfs-ops`; `DELETE /debug/zfs-ops` resets the
per-route part. Because the counters are global, a delta only belongs to one
request if nothing else touched ZFS at the same time, so every tool here
requests serially and zdapd reports an `overlapped` count when they did not.

`vol.kernel_ioctls_est` is derived, not observed. It uses the calibration in
`zfsstat`, measured on ZFS 2.4.1 with go-libzfs v2.22.1 by tracing ioctls on
`/dev/zfs`:

```
DatasetOpenSingle(pool)             8 ioctls
DatasetOpen(pool), 34 handles     252 ioctls   (7.4 per handle)
Path(), GetUserProperty()           0 ioctls   (served from the open handle)
```

`bench/zfs-ioctl-bench.sh` re-measures it; if ZFS or go-libzfs is upgraded and
the script and the estimate drift apart, update `IoctlsPerHandleX10`. As of
2026-11-14 the estimate and the traced count agree to within 2%:

```
ENDPOINT                                    ESTIMATED  TRACED
GET  /status                                253        248.6
GET  /resources                             251        250.0
GET  /resources/:resource                   251        250.0
GET  /resources/:resource/snaps             251        250.0
GET  /resources/:resource/clones            251        250.0
GET  /resources/:resource/snaps/:createdAt  251        250.0
```

and the traced ioctls are all metadata reads, in the proportions the model
assumes: 54% `ZFS_IOC_OBJSET_ZPLPROPS`, 18% `ZFS_IOC_DATASET_LIST_NEXT`,
14% `ZFS_IOC_OBJSET_STATS`, 13% `ZFS_IOC_SNAPSHOT_LIST_NEXT`.

## Running it

Start a zdapd:

```bash
NETWORK_ADDRESS=127.0.0.1 API_PORT=43211 ZPOOL=zdap/1/databases CONFIG_DIR=/zdap/1/config go run ./cmd/zdapd/ serve
```

Record a baseline:

```bash
go run ./cmd/zdap-bench -addr http://127.0.0.1:43211 -n 5 -json bench/baseline.json
```

After changing how an endpoint fetches from ZFS, compare against it:

```bash
go run ./cmd/zdap-bench -addr http://127.0.0.1:43211 -n 5 -compare bench/baseline.json
```

Useful flags: `-resource` to pin which resource is exercised, `-n` / `-warmup`
for the request counts, and `-mutating yes-create-and-destroy-clones` to also
benchmark the endpoints that clone and destroy. The mutating run creates real
clones and docker containers and destroys them again in the teardown of each
measured request; it is off by default for that reason.

The ZFS layer on its own, without the daemon:

```bash
ZDAP_BENCH_ZPOOL=zdap/1/databases go test ./internal/zfs/ -run '^$' -bench . -benchtime 10x
```

Ground truth ioctl counts:

```bash
bash bench/zfs-ioctl-bench.sh -p zdap/1/databases -c /zdap/1/config -P 43219 -n 5
```

It starts and stops its own zdapd, so stop any other one on that port first.
(`chmod +x bench/zfs-ioctl-bench.sh` if you would rather run it directly; it
has a shebang but is checked in without the executable bit.)

Check the paths that have already moved off go-libzfs, without the API. The
channel program needs root, so this is also the quickest way to tell a
permission problem from a broken program:

```bash
sudo env ZPOOL=zdap/1/databases CONFIG_DIR=/zdap/1/config go run ./cmd/zdapd check-zfs
```

## Baseline, 2026-11-14

Pool `zdap/1/databases`: 19 filesystems and 15 snapshots, 34 datasets in all,
15 configured resources, no clones.

```
ENDPOINT                                    OK   ms    POOL OPENS  HANDLES  PROP VALS  USERPROPS  PATHS  LIBZFS  IOCTLS
GET  /status                                0/5  42.6  1           34       3094       30         99     167     253
GET  /resources                             5/5  37.1  1           34       3094       450        1485   2476    251
GET  /resources/:resource                   5/5  35.0  1           34       3094       30         99     166     251
GET  /resources/:resource/snaps             5/5  35.7  1           34       3094       30         99     166     251
GET  /resources/:resource/clones            5/5  36.0  1           34       3094       30         99     166     251
GET  /resources/:resource/snaps/:createdAt  5/5  35.4  1           34       3094       30         99     166     251
```

What this says:

- **Every single endpoint opens the entire pool.** 34 dataset handles, each with
  its full native property table loaded: 3094 property values read to use at
  most 8 of them per dataset. That is ~251 ioctls and ~35 ms before any
  endpoint-specific work starts.
- **The open is essentially the whole cost.** At the ZFS layer, `Open` takes
  35.5 ms while `ListSnaps`, `ListClones` and `ListBases` over an already open
  pool take 18–81 µs and zero ioctls, because they only walk handles that are
  already in memory. `OpenAndListAll` (open once, then list snaps and clones
  for 15 resources) is 37.3 ms — the repeated filtering adds about 5%.
- **The filtering is still quadratic in resources.** `GET /resources` calls
  `ListSnaps` and `ListClones` once per resource, so with 15 resources it walks
  the dataset tree 30 times: 1485 `Path()` calls and 450 `zdap:*` property
  reads, against 99 and 30 for a single resource. Cheap today because it is all
  in-memory, but it scales with resources × datasets and it is what forces the
  full open in the first place.
- **Cost grows with the whole pool, not with the request.** A host with 300
  clones pays for all 300 on a request that asks about one resource.

So the ordering for the migration is: replace the recursive open first, the
repeated in-memory filtering second.

## Migrated: the whole read path

`GET /status` went first, then `ListSnaps` and `ListClones` together, which
covers every remaining read endpoint because they all funnelled through those
two functions.

What changed:

- `internal/zfs/listDatasets.lua` lists the pool's children and their snapshots
  with their `zdap:*` properties, in one kernel call.
- `zfs.Listing` (`internal/zfs/listing.go`) is what a request now carries
  around instead of an open `*zfs.Dataset`. `z.Load(ctx)` fetches it once;
  `Names`, `Bases`, `Snaps` and `Clones` filter it as often as wanted, because
  filtering is a map lookup rather than a walk over libzfs handles. Nothing
  needs closing.
- `ZFS.Open`, `ZFS.List`, `ZFS.ListClones`, `ZFS.ListSnaps` and `ZFS.ListBases`
  are gone, along with the `zfs.Dataset` wrapper.
- `ZFS.SetUserProperty` takes a dataset name instead of an open handle, and
  opens that one dataset itself (8 ioctls). Three call sites in `clonepool` and
  `cloning` used to need a handle from a full recursive open just to write one
  property; `servermodel.ServerInternalClone` no longer carries a `*zfs.Dataset`
  at all.
- `getSnaps` groups the resource's clones once instead of once per snapshot.
  That N+1 was invisible on a pool with one snapshot per resource and grew as
  `R + ΣN` full tree walks per request.

Per request, every read endpoint now does one channel program: no dataset
handles, no property table loads, 1 ioctl instead of 229.

Measured against the same pool as the baseline, with zdapd running as root:

```
ENDPOINT                                    OK   ms    POOL OPENS  HANDLES  PROP VALS  USERPROPS  PATHS  LIBZFS  IOCTLS
GET  /status                                5/5  13.9  0           0        0          0          0      2       4
GET  /resources                             5/5  11.1  0           0        0          0          0      1       1
GET  /resources/:resource                   5/5   9.3  0           0        0          0          0      1       1
GET  /resources/:resource/snaps             5/5   9.3  0           0        0          0          0      1       1
GET  /resources/:resource/clones            5/5   9.1  0           0        0          0          0      1       1
GET  /resources/:resource/snaps/:createdAt  5/5   9.3  0           0        0          0          0      1       1
```

against the baseline, on a pool that had shrunk from 34 datasets to 31 in
between:

```
ENDPOINT                                    HANDLES          LIBZFS             IOCTLS            ms
GET  /status                                34 -> 0 (-100%)  167 -> 2 (-99%)    253 -> 4 (-98%)   42.7 -> 13.9 (-68%)
GET  /resources                             34 -> 0 (-100%)  2476 -> 1 (-100%)  251 -> 1 (-100%)  36.7 -> 11.1 (-69%)
GET  /resources/:resource                   34 -> 0 (-100%)  166 -> 1 (-99%)    251 -> 1 (-100%)  35.9 ->  9.3 (-74%)
GET  /resources/:resource/snaps             34 -> 0 (-100%)  166 -> 1 (-99%)    251 -> 1 (-100%)  36.3 ->  9.3 (-74%)
GET  /resources/:resource/clones            34 -> 0 (-100%)  166 -> 1 (-99%)    251 -> 1 (-100%)  35.4 ->  9.1 (-74%)
GET  /resources/:resource/snaps/:createdAt  34 -> 0 (-100%)  166 -> 1 (-99%)    251 -> 1 (-100%)  35.5 ->  9.3 (-74%)
```

`/status` is 2 calls and 4 ioctls because it also runs `zpool list` for the
capacity. Every other endpoint is one channel program. Wall clock is now
dominated by the fork and exec of `zfs program`, not by ZFS work.

Checked against ground truth with `zdapd check-zfs`: 15 bases and 15 snapshots,
matching `zfs list`, with `zdap:resource` and `zdap:created_at` matching
`zfs get -r`, and pool capacity matching `zpool list -Hp`.

Two caveats on the numbers:

- The ioctl figure for a channel program is 1 by construction, one
  `ZFS_IOC_CHANNEL_PROGRAM`, not traced. `bench/zfs-ioctl-bench.sh` starts its
  own unprivileged zdapd, which cannot run channel programs, so it can no
  longer trace the read path. The 3 ioctls for `zpool list` were traced.
- The clone path is verified. A clone left behind by a failed creation carried
  the six properties written at creation time and neither `zdap:healthy` nor
  `zdap:expires_at`, and `check-zfs` read it back as
  `resource="postgres-boris" owner="bench" created=2026-11-14T14.17.50
  snapped=2026-11-14T11.23.49 pooled=false healthy=false port=42855
  expires=none`, matching `zfs get` exactly. That is the case that would have
  aborted the program had it used `zfs.get_prop`. `GET /clones` reported it
  with `broken: true` and `port: 0` because its proxy container was missing,
  and `DELETE /clones/:time` destroyed it.

The mutating endpoints, against `postgres-trade` with a clone pool configured,
all pass. They still go through go-libzfs, so they are the remaining work:

```
ENDPOINT                                    OK   ms      POOL OPENS  HANDLES  PROP VALS  LIBZFS  IOCTLS
POST /resources/:resource/snaps             2/2  1332.3  1           3        273        16      32
POST /resources/:resource/snaps/:createdAt  2/2  1297.9  1           3        273        16      32
POST /resources/:resource/claim             2/2   706.2  0.5         3.5      318        13      35
DEL  /resources/:resource/claims/:claimId   2/2    21.8  0           1         91         3      10
DEL  /resources/:resource/clones/:time      2/2   475.1  2           2        182         6      17
DEL  /resources/:resource/clones            2/2  1427.2  6           6        546        16      49
```

The opens on the write path are of single datasets, not of the pool: creating a
clone opens the base and its snapshot, and destroying one opens each dataset it
walks. 3 to 6 handles, against the 31 a pool open used to materialise. Wall
clock is docker, not ZFS.

An unprivileged zdapd can no longer answer any read endpoint, where before only
`/status` failed. That costs nothing in production, which runs as root to mount
datasets, but it does remove the unprivileged dev loop.

## Where the work is

`internal/zfs` is still the only package that touches ZFS. The read path is now:

```
handler           → z.Load(ctx)                 one channel program
  getSnaps        → core.GetResourceSnaps(lst)  → lst.Snaps()   filter in memory
                  → core.GetResourceClones(lst) → lst.Clones()  filter in memory
  getStatus       → core.ServerStatus(ctx)      → z.Load + z.Space
```

go-libzfs is left only on the write path: `CreateDataset`, `SnapDataset`,
`CloneDataset`, `SetProperties`, `SetUserProperty` and the destroy walk. Those
are the next thing to look at, and they are a different problem: `zfs.sync.*`
in a channel program can clone and destroy, but a channel program cannot mount,
which `CreateDataset` and `CloneDataset` both need.

## Known issues found while benchmarking

- **`GET /status` panicked on this host (fixed).** `UsedSpace`/`FreeSpace`/
  `TotalSpace` called `Dataset.Pool()`, and go-libzfs v2.22.1 sizes its pool
  property array for an older ZFS than 2.4.1, so `Pool.ReloadProperties` wrote
  past the end of it: `index out of range [40] with length 37` at
  `go-libzfs/v2@v2.22.1/zpool.go:529`. Replaced by `ZFS.Space`, which reads the
  same vdev statistics from `zpool list -Hp` in one call.
- **`zfs list -j` is not an option here.** JSON output landed in OpenZFS 2.3,
  and some servers still run Ubuntu 24.04, which ships 2.2.2. Channel programs
  are fine: they have been in OpenZFS since 0.8.0. `Space` deliberately parses
  `-Hp` tab output rather than `-j --json-int` for that reason.
- **Clone creation is broken on this host, unrelated to the migration
  (fixed).** `createClone` set `Domainname` on both the clone and proxy
  containers, to the clone name and the clone name plus `-proxy`. Those are 74
  and 80 bytes, and `setdomainname(2)` caps a domain name at 64, so runc failed
  container init with `setdomainname: invalid argument`. Verified directly with
  docker: 64 bytes starts, 65 gives the same error. Every resource name on this
  host produces a clone name over the limit, so no clone could be created at
  all. Both fields are removed; the containers are still addressable as
  `cloneName`, which is what the proxy's `TARGET_ADDRESS` uses and what docker's
  embedded DNS resolves.
- **A failed clone creation leaves the dataset behind.** `createClone` makes the
  ZFS clone first and the containers second, and the container error path does
  not roll the clone back. That is how the clone used to verify the listing
  above came to exist. Not changed here.
- **Clone expiry was an hour late (fixed, by making zdap timestamps UTC).**
  `clonepool` wrote `zdap:expires_at` with `time.Now().Format(TimestampFormat)`,
  which is local wall clock, and the listing read it back with `time.Parse`,
  which assumes UTC when the layout carries no zone. The instant came back
  shifted by the host's offset, so `pruneExpired`'s
  `ExpiresAt.Before(time.Now())` stayed false for another hour in CET, two in
  CEST: a clone claimed with `ttl=120` survived its expiry for the rest of the
  benchmark, and with `max_clones` reached the pool could not hand out new
  clones. The old libzfs listing parsed it the same way, so this predates the
  migration.

  The write side is now UTC. `zfs.FormatTimestamp` and `zfs.ParseTimestamp` are
  the only places that touch `TimestampFormat`, and `FormatTimestamp`
  normalises to UTC, so a dataset name and a `zdap:*` value denote the same
  instant whatever zone the host is in. `TestTimestampRoundTripIsZoneIndependent`
  and `TestExpiryComparisonHoldsInAnyZone` pin that across four zones, and the
  suite passes under `TZ=UTC`, `TZ=Europe/Stockholm` and
  `TZ=America/Los_Angeles`.

  Nothing changes on a host that is already on UTC, which is every zdap server:
  `time.Now()` there is already UTC wall clock, so both the old and the new
  code produce identical names, properties and API output. This was only ever a
  development-environment bug.

  Two things to know on a host that is **not** on UTC:

  - Timestamps already on disk were written as local wall clock and are now
    read as UTC, so they are off by the offset until they churn. Right after
    the change, a clone whose `zdap:expires_at` said `14.29.08` was no longer
    seen as expired at 14:51, because that value now means 14:29 UTC. New
    clones are correct immediately; old ones lag by the offset once.
  - New dataset names use UTC digits, so they differ from the host's older
    local-time names. Names are opaque identifiers, but
    `GetLatestResourceSnap` sorts by parsed `CreatedAt`, so during an
    offset-wide overlap an older local-named base can sort after a newer
    UTC-named one.

  The API wire format is unchanged on UTC hosts. On a non-UTC host,
  `created_at` and `snapped_at` shift by the offset and start denoting the
  instant that actually happened, rather than local wall clock relabelled as
  UTC. The `:createdAt` round trip holds either way, because the dataset name
  and the property are written from the same value.
- **zdapd exits 0 even when a command fails.** `main` prints `[Error] %v` and
  returns. `check-zfs` exits non-zero itself so it can be scripted; the other
  commands were left as they were.
