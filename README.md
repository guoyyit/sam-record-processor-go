# samproc — SAM record processor

A local, dependency-free toolkit for processing sequencing alignment
records in SAM text format. It supports coordinate sorting of shuffled,
multi-shard inputs (including unmapped, secondary and supplementary
records), multi-way merging, duplicate marking, and bounded-memory
per-position coverage queries. All inputs and outputs are local files;
no external services are used.

## Build

```sh
go build ./...
go test ./...
```

## Commands

```sh
samproc sort     [-m BYTES] [-j N] [-t DIR] -o OUT IN [IN...]
samproc merge    [-m BYTES] [-j N] [-t DIR] -o OUT IN [IN...]
samproc markdup  [-m BYTES] [-j N] [-t DIR] -o OUT IN
samproc coverage [-m BYTES] [-j N] [-dup] -r REF -s START -e END IN
```

Common flags:

- `-m` in-RAM cap in bytes; `K`/`M`/`G` suffixes accepted (default
  `256M`). A negative value disables the cap.
- `-j` worker concurrency. `1` forces serial execution. Defaults to the
  number of CPUs.
- `-t` work directory for spill chunks, cascade-merge files and restart
  journals. Defaults to the OS temp directory.
- `-o` output path.

`coverage` emits one JSON object per reference position (JSON lines)
and additionally accepts `-dup` to include duplicate-flagged records.

## Parsing and validation

The parser handles the SAM header (`@HD`, `@SQ`, other record types are
preserved verbatim) and all 11 mandatory alignment fields plus optional
`TAG:TYPE:VALUE` fields. Every FLAG bit is decoded and checked. CIGAR is
parsed operation by operation.

Errors are never silently accepted; every parse/semantic failure reports
the 1-based record line and the reason. Rejected cases include:

- malformed fields (count, numeric ranges, MAPQ outside 0..255, illegal
  CIGAR/SEQ/QUAL characters, malformed optional tags);
- FLAG contradictions, e.g. READ1+READ2 together, SECONDARY+SUPPLEMENTARY
  together, UNMAP on a secondary/supplementary, PROPER_PAIR on an
  unpaired or unmapped fragment, mate/read bits without PAIRED;
- RNAME/RNEXT referencing a sequence not declared in `@SQ`;
- POS/RNAME consistency (RNAME `*` requires POS 0), mapped records with
  CIGAR `*`, alignment extending beyond `@SQ LN`;
- length mismatches between CIGAR and SEQ and between SEQ and QUAL.

## Sorting order

The sort key is:

1. records with a usable coordinate (RNAME ≠ `*`, POS > 0) first, then
   records without one at the end. SAM allows an unmapped read to retain
   RNAME/POS for placement; such a record sorts by that coordinate,
   while a fully unplaced record (`*`/0) sorts last;
2. RNAME lexicographic;
3. POS ascending;
4. the full serialized line bytes (total-order tie-break).

Because the final tie-break is deterministic, equal inputs and config
produce byte-identical output, whether execution is serial or
concurrent. Unmapped records that legitimately carry a coordinate are
placed by that coordinate per SAM semantics; unplaced records go last.

## External merge sort and resource limits

Inputs are streamed record by record. Records are buffered until their
retained bytes reach `-m`, then sorted and spilled as complete,
self-contained SAM chunk files. Chunk sequence numbers are assigned in
input/stream order on the reader side; workers only sort and write the
chunk they are handed, so scheduling cannot change results.

The final result is produced by a bounded-fan-in merge. Each pass keeps
at most `F = clamp(floor(-m / 64KiB), 2, 256)` readers open; when more
than `F` chunks exist, intermediate atomic cascade files are produced.
Consequently resident memory is bounded by the buffered sort batches
plus `F` merge readers and fixed-size write buffers, independently of
total input size.

## Merging

`merge` accepts already coordinate-sorted SAM files and produces one
globally ordered, legal SAM. Records are emitted byte-for-byte
unchanged; nothing is dropped across distinct shards. Each input is
checked to be monotonic in the global order and an out-of-order input is
rejected. The output satisfies the sort invariant and can be fed back
into later merge/sort runs.

## Duplicate marking

- Unmapped, secondary and supplementary records never participate and
  are never marked.
- A paired record with a mapped mate is grouped with the fragment's
  other end; the key is the pair of ends
  `(RNAME,POS,orientation)` and `(RNEXT,PNEXT,mate orientation)`, with
  end order canonicalized so both mates build the same key.
- All other eligible records use the single end key
  `(RNAME,POS,orientation)`.
- Among duplicate candidate fragments, the representative is the one
  with the greatest total base quality (sum of Phred33 qualities over
  the fragment, single end or both mates). Every record of every other
  candidate gets FLAG `0x400`; the representative keeps the bit clear.
- Equal-quality ties are broken by the raw line bytes for a
  deterministic choice.

## Coverage view

`coverage` streams one result per position in an inclusive
`[START,END]` interval. Each position reports depth, a `Covered` flag,
and per-observation base, Phred33 quality and strand:

- aligned bases (`M`,`=`,`X`) contribute observations; a deletion (`D`)
  covers the site without a base (so `Covered=true` with zero
  observations), and a spliced gap (`N`) is not a gap base;
- inserted bases (`I`) are reported as `InsertedBases` attached to the
  preceding reference position and never shift downstream coordinates;
- depth zero at a site (`Covered=false`) is distinct from an interval
  with no record at all (every position has `Covered=false`);
- duplicate-flagged records are excluded unless `-dup` is set;
- memory is bounded by the records overlapping one sweep position,
  independent of interval length or input size.

## Concurrency

Chunk sorting and writing run on a worker pool of size `-j`. The merge
ordering and tie-breaking form a total order independent of scheduling,
and chunk identities are assigned serially while reading, so concurrent
and serial runs produce identical bytes.

## Interruption and restart

All durable artifacts live under `-t`:

- spill chunks are written as `name.tmp`, fsynced, and atomically
  renamed, and each completed chunk is recorded in a `journal.txt`;
- intermediate cascade merges are complete atomic files;
- the final merge checkpoints periodically (fsync plus a journal
  `PART=<popped>@<offset>` marker).

On restart with the same inputs, output and work directory, completed
chunks and cascade files are reused; the final pass truncates to the
last checkpoint and replays its total order, skipping the already
emitted prefix. Partial `.tmp` files and journal-less run directories
from hard crashes are rebuilt or removed, never merged into a corrupt
output. Reruns therefore equal a single uninterrupted run byte for byte.

## Layout

- `internal/sam` — header/record/CIGAR/FLAG parsing, validation and
  serialization.
- `internal/samproc` — external sort, cascade merge, duplicate marking,
  coverage, journals/restart and configuration.
- `cmd/samproc` — command line interface.
