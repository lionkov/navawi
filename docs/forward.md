# The way forward

This note records the design discussion of November 2026 about turning
navawi from a research prototype into a storage system fit for real use.
It states what was decided, what was measured, what was learned from
other systems, and what remains open. Its main subject is the cleaner,
which reclaims the space of dead blocks.

Nothing here is final. The measurements are small and synthetic, and the
last section says what evidence is required before the open questions
are settled.

## Roadmap

Six steps were proposed. Steps 1 and 4 came first; the rest are in
discussion.

| Step | Subject | State |
|---|---|---|
| 1 | Crash safety, with a crash-replay test harness | Implemented |
| 2 | Checkpoint record with named roots | Not started; prerequisite of step 3 |
| 3 | Cleaner (space reclamation) | In design; this note |
| 4 | `Store` handle in place of package globals | Implemented |
| 5 | Splice between files, snapshots, diff, send and receive, child checksums | Not started |
| 6 | Front ends: S3 gateway, NBD, 9P or FUSE | Not started |

At the time of writing, steps 1 and 4 are in the working tree and not
yet committed.

## Constraints

Two constraints govern every design below.

- **A committed block's ID is its disk offset.** A read resolves a block
  with no map lookup. No design may put an ID-to-location table, on disk
  or in memory, on the read path. This rules out the logical addresses
  and remap tables that most log-structured systems use to move blocks.
- **Hot-path cost outweighs convenience.** Synchronization or lookups
  added to reads and writes need a strong reason.

## Steps 1 and 4 as built

**Step 4.** `Open` or `OpenDisk` returns a `*Store`; the package globals
are gone. A `Disk` interface (`ReadAt`, `WriteAt`, `Sync`, `Close`) sits
under the store, so a test can supply a disk that records writes.

**Step 1.** The durability protocol is:

- Every fragment is fsynced, so `Sync` means durable.
- Each fragment carries a CRC-32C, verified at mount. Each block carries
  one in its header, verified when the block is read from disk.
- The log ends at the first position that holds no valid fragment. The
  next write replaces a torn tail. `Open` never writes.
- Fragments are aligned to 8 bytes, so a fragment's end marker cannot
  overlap the segment's end trailer.
- A segment is sealed in two synced steps, and counts as sealed only if
  its end trailer is complete. The seal is durable before anything is
  written to the next segment.

`TestCrash` runs random splices against a model on a recording disk and
reopens the store from every state a crash could leave: writes cut
short, torn by sector, and every lost, torn or complete combination of
unsynced writes. Each recovered store is then written to and crashed
again.

Limits left for later steps:

- The on-disk format changed and carries no version number. Disks
  written before step 1 are unreadable. The version belongs in the
  checkpoint of step 2.
- A damaged fragment in the active segment looks like a torn tail and
  silently ends the log there. A checkpoint that records the log end
  would close the gap.
- Mount walks every segment to find the active one.
- `mkdir` through navafs fails with "permission denied". The fault
  predates this work and is not yet diagnosed.

## The cleaner

### The question

The log only grows: `ErrFull` is final. Step 3 must reclaim dead blocks.

The working intuition was this: because the log keeps related blocks
close, dead space will form adjacent regions, and fragmentation will be
slight. The discussion tested that intuition by measurement and against
the literature on garbage collection, in memory and on disk.

The constraint on block IDs makes moving a live block expensive. The
cleaner must rewrite every parent that points to the block; a parent is
immutable, so the rewrite copies the path to a root; and a block shared
with a retained snapshot has several such paths.

In the current code, subtrees are shared only among versions of one
file and through application blocks that hold root IDs. Step 5's splice
between files would add a third kind of sharing.

### What was measured

**Liveness maps of the real store.** The store ran with default settings
(16 MB segments, 8 KB index nodes, 57 KB leaves) on a 1 GB memory disk.
The log was written once to the end. A trace from the live roots then
classified every block on disk.

| Workload | Segments wholly dead | Segments at most 10% live | Dead bytes in runs of 1 MB or more |
|---|---|---|---|
| 128 MB file, random 1–64 KB overwrites, sync every 64 writes | 7 of 58 | 39 | 73% |
| Same, sync every write | 6 | 40 | 71% |
| Same, 90% of writes to 10% of the file | 0 | 44 | 66% |
| Same, four snapshots retained | 0 | 25 | 58% |
| Whole files of 1 KB–4 MB created and deleted | 24 | 39 | 96% |
| Same, deletes favour young files | 3 | 32 | 95% |
| Eight files appended in records of 0.1–4 KB | 0 | 58 | 41% |

Findings:

- The intuition holds for adjacency. Most dead bytes lie in runs of
  1 MB or more, and at most 5% lie in runs under 64 KB.
- It fails at the segment grain. A residue of live leaves, 1–10% of a
  segment, pins most old segments. Freeing only whole segments recovers
  41% of them at best and none in three workloads.
- Index nodes are a small share of what is written: 0.6% of bytes when
  commits are batched, 7.8% when every write is synced. The fan-out is
  512, and blocks superseded before a commit never reach the disk. The
  block cache is the store's nursery.
- The live index of a 128 MB file is 0.04 MB, about 1/3500 of the data.
- Leaf rewriting dominates some workloads. Appending 2 KB records
  rewrote the 57 KB tail leaf at every sync: 881 MB written for 92 MB of
  data.

**Steady-state simulation.** One pass of the log flatters the result,
because no space has yet been reused. A block-level simulator modelled a
2 GB disk with the same block sizes, warmed it with three capacities of
writes, and measured three more. It compared two mechanisms. *Cleaning*
evacuates the live blocks of a victim segment, as Sprite LFS does.
*Threading* writes new fragments into existing holes of at least a given
size and moves nothing. Each cell gives write amplification, then
megabytes per second of user writes on a hard disk (150 MB/s, 8 ms per
seek).

| Workload, policy | 50% full | 70% | 80% | 90% |
|---|---|---|---|---|
| Random leaf overwrites, cleaning (greedy) | 1.27, 55 | 1.94, 36 | 2.84, 25 | 5.83, 12 |
| Random leaf overwrites, threading (holes ≥ 64 KB) | 1.00, 26 | 1.00, 13 | 1.53, 13 | 4.59, 11 |
| 90/10 skew, cleaning (cost-benefit, separate log for survivors) | 1.24, 56 | 1.47, 48 | 1.99, 35 | 3.57, 20 |
| 90/10 skew, threading (holes ≥ 64 KB) | 1.00, 18 | 1.00, 12 | 1.27, 12 | 3.03, 12 |
| Whole files, cleaning (greedy) | 1.24, 62 | 1.90, 37 | 2.86, 24 | 6.77, 10 |
| Whole files, threading (holes ≥ 256 KB) | 1.00, 84 | 1.00, 58 | 1.01, 46 | 2.89, 21 |

Findings:

- For whole-file workloads, threading copies nothing up to 80% full and
  is faster than cleaning at every fill level, on a hard disk as well.
- For random overwrites inside large files, the holes shrink to one to
  four leaves. Threading still copies nothing up to 70% full, which is
  what matters on flash. On a hard disk the short write runs
  (121–261 KB) make it about half as fast as cleaning.
- Under skew, cleaning with cost-benefit selection and a separate log
  for survivors is the best hard-disk policy.
- At 90% full every policy is slow.
- Threading scatters file data: the mean file extent fell from about
  300 KB under cleaning to 150–270 KB.

**Limits of this evidence.** The workloads are synthetic. The liveness
maps come from one pass over a 1 GB store. The steady-state figures come
from a simulator, not from navawi's code, and the simulator ignores the
cost of finding and rewriting parents when a block moves. Its greedy
figures agree with the analytic values of Lomet and Luo (1.9, 2.7 and
5.3 at 70, 80 and 90% full), which lends it some credit and no more.

### What other systems do

**No surveyed system relocates shared blocks with physical pointers
unless it keeps a reverse map or an indirection.**

- ZFS has physical pointers and never delivered block-pointer rewrite.
  A block reached from many snapshots and clones needs a table from old
  locations to new for the whole pass. For device removal ZFS added an
  indirection confined to the removed device's address range.
- HAMMER2 is the closest relative of navawi: physical references, shared
  subtrees, no reference counts and no back pointers. It frees space by
  tracing all metadata from every root, and it never relocates.
- btrfs keeps a reference count and back references for every extent,
  updates them at every copy-on-write, and relocates through them. An
  experimental remap tree now replaces pointer correction with an
  indirection for relocated block groups.
- Backlog (Macko et al.) shows that a log-structured reverse map can be
  cheap: about 0.01 extra block writes per block operation, and 2.5–6.3%
  of the data size.
- WAFL stores a physical and a virtual address in each pointer.
  bcachefs gives every extent one owner, found through a backpointer,
  and represents snapshots as versions of keys in one tree.
- Sprite LFS, F2FS, NILFS2 and RAMCloud relocate through an inode map,
  a node address table, a block address table and a hash table.

**Liveness.** Four methods exist: trace from the roots; account for
deaths as they happen; ask a block's owner whether it still points to
the block; and consult an indirection table. Two matter here.

- The trace is cheap in navawi, because an index entry holds the child's
  ID and the size of its data. A mark reads index nodes only and never a
  leaf.
- ZFS accounts for deaths by birth time. A superseded block born after
  the latest snapshot is free at once; an older one goes on a dead list
  for the snapshot's deletion. While navawi's log is append-only, a
  block's offset is its birth time. The rule requires a linear history
  of versions; arbitrary sharing defeats it in every system surveyed.

**Policy.**

- Sprite's write cost is 2/(1−u) for a victim with live fraction u.
  Greedy selection is optimal only when all data is alike; under skew it
  leaves cold segments to hold free space captive.
- The Sprite cost-benefit formula as published could not be reproduced
  by Rumble. Using the age of the segment, (1−u)·segmentAge/(1+u),
  repairs it. The simulator above used the published form.
- Placement matters more than selection. On the Alibaba block traces,
  separating blocks rewritten by the cleaner from new writes captured
  most of what the elaborate lifetime predictors achieve (SepBIT).
- Matthews et al. found cleaning cheaper than hole-plugging up to about
  85% full for random updates, and the reverse beyond. F2FS writes into
  holes only when fewer than 5% of its sections are free, and reuses
  freed space only after the next checkpoint.
- The systems surveyed reserve space for the cleaner: 5% in F2FS, 8% in
  bcachefs.

**In-memory collectors.**

- What transfers: choosing regions by the liveness totals of the last
  mark (G1, C4, Immix); mark state kept in side tables; refusing to move
  objects with many referrers; and the incoming-reference lists of the
  persistent-object-store collectors (Maheshwari and Liskov, PMOS),
  which are logged and never updated in place.
- What does not: forwarding pointers written into the old copy (Cheney,
  Shenandoah), since blocks are immutable; load barriers; and the quick
  release of C4 and ZGC, which depends on separating an address from its
  storage.
- Generational collection transfers in the wrong direction. Immutable
  blocks point only to older blocks, so the youngest part of the log can
  be collected exactly with no remembered set. The youngest segments
  are the fullest; the garbage that pins segments is old.

### Proposed design

The proposal is a cleaner that moves almost nothing.

1. **Liveness by marking.** Trace from the named roots of the step 2
   checkpoint, reading index nodes only. Blocks are immutable, so a mark
   of a frozen root set needs no barrier; blocks born after the freeze
   count as live. The result is a map of free extents, stored with the
   checkpoint. Lists of superseded IDs, written at commit, can later
   tell the cleaner where to look; the mark remains the authority.
2. **Reuse holes in place.** Write new fragments into free extents above
   a size threshold, emptiest region first. Nothing moves, IDs stay
   offsets, and snapshots cost nothing. The unit of reclamation is the
   dead extent, not the segment.
3. **Evacuate rarely, by copy-on-write.** Evacuate a region only where
   that is cheap (at most about 10% live) or where a hard disk under
   random overwrites needs long runs. Walk down from the roots and
   rewrite each victim block and its ancestors as an ordinary commit
   does. A table from old IDs to new lets a shared block move once. The
   table exists in memory for the duration of the pass; it never reaches
   the disk and no read consults it. Survivors go to a region apart from
   new writes. The victim's space is free once the commit that replaced
   the roots is durable.
4. **Choose by cost.** For each candidate region, compare the seeks that
   threading would cost with the copying that evacuation would cost, and
   take the cheaper, as Matthews's adaptive method does.

A reverse map in the manner of Backlog remains possible. It is hard to
justify while a full mark reads so little.

### Open problems

- **Recovery of a threaded log.** A fragment written into a hole is not
  at the next offset, and the hole may hold old fragments with valid
  checksums. Each fragment needs a sequence number and the location of
  its successor, and recovery must follow that chain from the
  checkpoint. A hole may be reused only after the checkpoint that
  records it is durable. This replaces the torn-tail rule of step 1 and
  belongs with step 2.
- **Offsets cease to be birth times** once holes are reused. Any rule
  that reads an ID as an age needs a commit number from elsewhere; the
  block header has four spare bytes.
- **Many small files.** A root reached from an application block must be
  read to learn whether it is a leaf. Ten million small files mean ten
  million reads per mark. A leaf flag in the low bits of the ID, which
  alignment leaves free, would remove them. The case is unmeasured.
- **Leaf rewriting.** No cleaner removes the cost of rewriting a 57 KB
  leaf for a small change. Reducing it would lower the cleaner's load
  more than any policy.
- **Read locality** under threading, on hard disks above all.
- **The free-extent map** must coalesce neighbours at once and must not
  be a flat list: bbolt's puts took up to 8 seconds with a million free
  entries in an array.

### Decisions pending

1. Is the transient in-memory table of point 3 acceptable under the
   constraint on IDs? Without it, evacuation cannot handle shared
   blocks, and the cleaner is points 1 and 2 alone.
2. Which medium comes first? On flash, points 1 and 2 are nearly the
   whole cleaner. Hard disks under database-style load need point 3.
3. May the cleaner give a retained snapshot a new root ID, the name in
   the root table staying fixed? If not, blocks pinned by snapshots stay
   where they are, as in ZFS.

## Evidence required before deciding

The small tests show which mechanisms exist and roughly where they cross
over. They are not a sound basis for thresholds, or for deciding how
much of the evacuation machinery to build. Those decisions wait for
realistic workloads, a real file system above all.

Candidate sources, most realistic first:

1. **A real file system on navawi.** Either navafs over 9P driven by
   real programs (unpack and build a source tree, git operations,
   SQLite), which first needs the `mkdir` fault fixed; or navawi as a
   block device under ext4 or XFS, which needs the NBD front end of
   step 6 and a Linux machine.
2. **Git-history replay.** Checking out successive commits of a large
   repository reproduces years of real create, rewrite and delete
   activity; Conway et al. aged file systems this way. A harness can
   apply the same operations to navawi directly, with no mount.
3. **Published block traces** replayed against one large navawi file as
   if it were a disk image: the MSR Cambridge server traces and the
   Alibaba Cloud traces that SepBIT used. Their availability and size
   have not been checked.
4. **Application workloads** with real fsync patterns, such as SQLite or
   a page store. This is the case where threading and cleaning diverge
   most.

Two methods of measurement:

- **Record and replay.** Run the real store on a large disk under a real
  workload, log each block's birth, size and death, and feed the log to
  the policy simulator in place of its synthetic generators. This tests
  policies against real block lifetimes before any cleaner code exists.
- **Build points 1 and 2, then measure the real code** at steady state.
  This is slower, and it also measures mark cost and recovery.

The proposed order is record and replay first, with git-history replay
and one block trace, and the mounted file system once a front end works.
The choice of workloads is not settled. Workloads from the systems
navawi is meant for outrank any public trace.

## Sources

- Rosenblum and Ousterhout, "The Design and Implementation of a
  Log-Structured File System", 1992.
- Rumble, Kejriwal and Ousterhout, "Log-structured Memory for DRAM-based
  Storage", FAST 2014; Rumble's thesis, 2014.
- Matthews et al., "Improving the Performance of Log-Structured File
  Systems with Adaptive Methods", SOSP 1997.
- Lee et al., "F2FS: A New File System for Flash Storage", FAST 2015.
- Wang et al., "Separating Data via Block Invalidation Time Inference
  for Write Amplification Reduction in Log-Structured Storage" (SepBIT),
  FAST 2022.
- Lomet and Luo, "Efficiently Reclaiming Space in a Log Structured
  Store", 2020.
- Macko, Seltzer and Smith, "Tracking Back References in a
  Write-Anywhere File System" (Backlog), FAST 2010.
- Rodeh, "B-trees, Shadowing, and Clones", ACM TOS 2008.
- Conway et al., "File Systems Fated for Senescence? Nonsense, Says
  Science!", FAST 2017.
- Kesavan et al., "Efficient Free Space Reclamation in WAFL", FAST 2017.
- Maheshwari and Liskov, "Partitioned Garbage Collection of a Large
  Object Store", SIGMOD 1997.
- Tene, Iyengar and Wolf, "C4: The Continuously Concurrent Compacting
  Collector", ISMM 2011.
- Detlefs et al., "Garbage-First Garbage Collection", ISMM 2004.
- Johnstone and Wilson, "The Memory Fragmentation Problem: Solved?",
  ISMM 1998.
- OpenZFS sources and issue 3582 (block-pointer rewrite); the HAMMER2
  design and freemap notes; the btrfs relocation sources; the bcachefs
  "Principles of Operation".
