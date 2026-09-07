# Arbitrary-Precision Seq Migration

## Invariants

- `entry.Seq` is immutable and value-comparable.
- Zero is the only unassigned sentinel; stored entries use positive sequences.
- Positive sequences are canonical unsigned decimal integers with no fixed-width limit.
- Ordering is numeric, and successor calculation never wraps.
- JSON writes sequences as decimal strings. Readers accept both the new string form and legacy `uint64` JSON numbers.
- Half-open ranges remain `[SeqS, SeqE)` and use `Seq.Next()` for exclusive ends.

## Storage migration

JSONL files require no rewrite: the decoder reads legacy numeric sequences, while new lines use strings.

bbolt entry and anchor keys move from an eight-byte `uint64` encoding to a versioned, numerically ordered encoding composed of a fixed marker, a big-endian magnitude length, and the big-endian magnitude. `Init` transactionally rewrites legacy keys before serving the owner/session. Metadata reads legacy numeric `LastSeq` and is rewritten using the string codec.

The compatibility direction is forward-only. After this version appends a JSONL string sequence or migrates a bbolt session, older tape-go binaries cannot read that new data. Operators must back up persistent stores before the first upgraded open when rollback is required.

## A2A versions

`taskstore.TaskVersion` is a separate persisted domain. New A2A records carry their own task version. Legacy profile-v1 records recover their historical version from a checked Tape Seq conversion; new records never cast Seq to `int64`.

## Compatibility boundary

Source compatibility is intentionally broken wherever callers used raw integer sequence literals. Persisted JSONL and bbolt data remain readable and are migrated in place as described above.

Known downstream consumers must migrate before updating their tape-go dependency. In particular, Anra at `39faef1` still stores projected sequences as `uint64`, compares them with integer operators, constructs ranges from integer literals, and exposes `SubmitResult.Seq` as `uint64`. Its matching migration should use `entry.Seq`, `Cmp`, `Next`, and decimal strings at external JSON boundaries.
