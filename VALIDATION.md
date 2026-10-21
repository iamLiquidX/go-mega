# Fork validation

Date: 2026-10-21. Upstream baseline: `c6acd6a`.
Protocol reference: MEGA SDK v10.21.0 (`d1c0cb76cb98598d93b9fb0bb89612daeb0fb39c`).

## Status

Library implementation and deterministic acceptance harness are complete for this
iteration. MirrorLeechX is unchanged. Application integration remains gated on
live upload acceptance with a replacement test account.

| Check | Result |
| --- | --- |
| Baseline upstream tests | Passed before edits |
| Deterministic tests and `go vet` | Passed |
| Windows race detector, Go 1.26.10 | Passed |
| Linux compilation, Go 1.26.10 | Passed |
| Link-parser fuzz smoke test | Passed, 85,852 executions |
| Encrypted-attribute fuzz smoke test | Passed, 115,436 executions |
| `govulncheck`, Go 1.26.10 | No vulnerabilities found |
| Login and saved-session reuse | Passed |
| Account metadata and account-tree loading | Passed |
| Live anonymous public file | Passed: 185,549,130 bytes, exact length and MAC |
| Live account-authenticated public file | Passed: identical size, MAC and SHA-256 |
| Live anonymous public folder | Passed: 13 files, 587,954,396 bytes, all sizes/MACs/hierarchy |
| Live account-authenticated public folder | Passed: 13 files, all sizes/MACs/hashes match anonymous output |
| Live file within public folder | Passed: 14,862,281 bytes, MAC and SHA-256 match recursive output |
| Nested subfolder links | Deterministic fixture passed; supplied small folder has no nested directories |
| Live file/folder uploads | Deferred by user pending replacement account |
| Official-SDK download comparison | Not run; SDK used as source reference |
| Pro transfer-allowance accounting | Not established by this account's download success |

Public file SHA-256 (anonymous and authenticated):
`10a972c2ed059730a75e98da9089cf5c5c36f12b69a02ad0c58ab770ffd56094`.

A 9,096,151,801-byte folder was resolved successfully, then its full download was
stopped at the user's request in favor of the smaller fixture. Its partial staging
directory was removed. It is not counted as a completed download test.

The first account permitted login/read operations but rejected isolated folder
creation with API -16. No upload succeeded and no existing account content was
modified. Live upload testing must use the replacement account.

Linked-file SHA-256: `c2efd503ebe619cdbc8695cad1d678f2f6f5d3bd1cdee0cd39e139e77a8afa03`.

All 13 files in the authenticated folder were also SHA-256 compared against the
anonymous folder output; every file matched.

## Failure and concurrency coverage

Fixtures cover modern/legacy public links, folder authentication with `n` and `sid`,
unordered trees, linked files/subfolders, missing/disconnected nodes, malformed
attributes/session data, unsafe and conflicting filenames, zero-byte files,
empty directories, chunk boundaries, corruption, missing chunks, short bodies,
expired URL refresh, quota errors, positive quota advisory codes, last-worker
errors, cancellation cleanup, and concurrent operations/publication.

An independent local MEGA fixture validates uploaded AES-CTR payloads, packed keys,
folder owner authentication and every descendant's share key. Recursive uploads
round-trip through anonymous public downloads, including Unicode and empty files.
These are protocol fixtures, not a substitute for live upload acceptance.

## Measurements

Observed live runs on this machine/network:

- Anonymous public file: 51.546 s, 3.43 MiB/s.
- Authenticated public file: 35.764 s, 4.95 MiB/s.
- Anonymous 13-file folder: 111.353 s, 5.04 MiB/s (8 chunk workers).
- Authenticated 13-file folder: 220.709 s, 2.54 MiB/s (8 chunk workers).

These are individual network observations, not a controlled speed comparison.

Allocation profiling identified growing `io.ReadAll` buffers in the bounded chunk
reader. Reading known-size chunks directly reduced allocations in the 1 MiB
verified-download benchmark from about 3.83 MB/op to about 2.36 MB/op in three
short runs each. The benchmark includes output rereading/hashing; this is allocation
volume per operation, not peak memory or a production throughput guarantee.

## Reproduction and remaining gate

See README for live-test environment variables. Credentials and downloaded test
content are not committed. The local reusable session lives under `.git` with an
explicit user-only Windows ACL. Public URLs/keys are supplied through process
environment variables and are not embedded in tests.

Before application integration: run live file/folder upload/export/public-download
round trips with the replacement account; retain or fix failures in this fork;
validate Linux runtime via CI; pin the resulting tested commit in MirrorLeechX.
