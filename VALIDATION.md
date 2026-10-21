# Fork validation

Date: 2026-10-21. Upstream baseline: `c6acd6a`.
Protocol reference: MEGA SDK v10.21.0 (`d1c0cb76cb98598d93b9fb0bb89612daeb0fb39c`).

## Status

Core download/upload live acceptance passed with the supplied controlled content
and replacement upload account. MirrorLeechX is unchanged. The standalone fork
is ready for the application integration phase for these tested transfer paths.
Official-SDK runtime comparison and Pro allowance accounting remain unverified.

| Check | Result |
| --- | --- |
| Baseline upstream tests | Passed before edits |
| Deterministic tests and `go vet` | Passed |
| Windows race detector, Go 1.26.10 | Passed |
| Linux compilation, Go 1.26.10 | Passed |
| GitHub CI: Ubuntu Go 1.24/1.26 and Windows Go 1.26 | Passed: race tests, vet, fuzz; Ubuntu 1.26 vulnerability scan clean |
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
| Nested subfolder links | Passed live using uploaded nested fixture, including empty directories and Unicode filenames |
| Live small file upload | Passed: export and anonymous/authenticated re-download with matching SHA-256 |
| Live nested folder upload | Passed: secure share, anonymous/authenticated re-download, complete hierarchy and hashes |
| Live original downloaded content upload | Passed: 185,549,130-byte file and 587,954,396-byte folder; exported links re-downloaded anonymously, every MAC/length/hierarchy/SHA-256 matched |
| Official-SDK download comparison | Not run; SDK used as source reference |
| Pro transfer-allowance accounting | Not established by this account's download success |

Public file SHA-256 (anonymous and authenticated):
`10a972c2ed059730a75e98da9089cf5c5c36f12b69a02ad0c58ab770ffd56094`.

A 9,096,151,801-byte folder was resolved successfully, then its full download was
stopped at the user's request in favor of the smaller fixture. Its partial staging
directory was removed. It is not counted as a completed download test.

The first account permitted login/read operations but rejected isolated folder
creation with API -16. No upload succeeded and no existing account content was
modified. The replacement account permits uploads. Live testing found two issues
that permissive fixtures had missed: legacy `s2` share creation is rejected on
secure-key accounts, and folder listings without `r=1` omit nested descendants.
Both are fixed and covered by stricter fixtures. The nested live acceptance test
then passed in 110.13 seconds, including anonymous/authenticated folder downloads
and a selected subfolder download.

The original downloaded file and 13-file folder subsequently passed full live
upload/export/anonymous-download acceptance in 642.42 seconds total. Test cleanup
removed only the uniquely created remote sandbox and reported no errors. Account
passwords, session files, public link keys and downloaded content are not committed.

Linked-file SHA-256: `c2efd503ebe619cdbc8695cad1d678f2f6f5d3bd1cdee0cd39e139e77a8afa03`.

All 13 files in the authenticated folder were also SHA-256 compared against the
anonymous folder output; every file matched.

## Failure and concurrency coverage

Fixtures cover modern/legacy public links, folder authentication with `n` and `sid`,
unordered trees, linked files/subfolders, missing/disconnected nodes, malformed
attributes/session data, unsafe and conflicting filenames, zero-byte files,
empty directories, chunk boundaries, corruption, missing chunks, short bodies,
expired URL refresh, quota errors, positive quota advisory codes, last-worker
errors, stalled response-body deadlines, cancellation cleanup, and concurrent operations/publication.

An independent local MEGA fixture validates uploaded AES-CTR payloads, packed keys,
folder owner authentication and every descendant's share key. Recursive uploads
round-trip through anonymous public downloads, including Unicode and empty files.
Both legacy and secure-key folder sharing are exercised. An independent Python
cryptography HKDF-SHA256/AES-GCM vector verifies secure repository decoding.
Repository tests cover unrelated/private/unknown field preservation, existing-key
reuse, version-conflict retries, corruption, identity/version mismatches, malformed
TLV/share records and generation overflow. These fixtures complement the live
small file and nested folder round trips.

## Measurements

Observed live runs on this machine/network:

- Anonymous public file: 51.546 s, 3.43 MiB/s.
- Authenticated public file: 35.764 s, 4.95 MiB/s.
- Anonymous 13-file folder: 111.353 s, 5.04 MiB/s (8 chunk workers).
- Authenticated 13-file folder: 220.709 s, 2.54 MiB/s (8 chunk workers).
- Original file upload/export: 275.218 s; anonymous re-download/hash check: 23.955 s.
- Original folder upload/export: 232.283 s; anonymous re-download/tree/hash check: 108.722 s.
- Content uploads used 4 workers; round-trip downloads used 8 workers and 30-second request deadlines.

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

The requested live file/folder upload/export/public-download round trips passed.
Pin the validated fork commit when implementing MirrorLeechX integration, and
validate the application's queue/progress/cancellation/error mapping separately.
Linux runtime tests already pass in GitHub CI.

CI run for the implementation baseline: https://github.com/iamLiquidX/go-mega/actions/runs/39560692334

CI run for the secure folder-share and recursive listing fixes:
https://github.com/iamLiquidX/go-mega/actions/runs/39565137538 (all jobs passed).
