# go-mega: public transfer fork

This fork adds context-aware public-link downloads and recursive uploads for
MirrorLeechX. It is developed and tested independently before application integration.
See [VALIDATION.md](VALIDATION.md) for measured results and acceptance gaps.

The module path stays `github.com/t3rm1n4l/go-mega` for compatibility. Consumers
must pin this fork with a `replace` directive; do not integrate an unvalidated branch.
The implementation uses Go 1.24+ and has no third-party runtime dependencies.
Use a supported, security-patched Go release (validated with 1.26.10).

## Public downloads

```go
client := mega.New()
client.SetHTTPS(true)
defer client.Close()

// Obtain size/name/tree before applying application limits or queueing payloads.
resource, err := client.ResolvePublic(ctx, publicURL)
if err != nil {
    return err
}
result, err := resource.Download(ctx, outputDirectory, func(p mega.Progress) {
    // Serialized per operation; return promptly.
    report(p.Name, p.Transferred, p.Total, p.BytesPerSecond)
})
```

`DownloadPublic` combines those two steps. Whole public folders and links to a
specific file/subfolder are supported. There is no interactive selection system.
Encrypted folder trees are resolved completely; invalid keys, disconnected nodes,
unsafe filenames and ambiguous local filenames fail explicitly.

Downloads use a private staging directory. Every expected chunk, file length and
MEGA MAC must verify before the resource is published. Existing destinations are
rejected. Failed/cancelled operations remove their staging files. No custom disk
preallocation or old SDK patch is required. Use an application-owned output directory.
Portable filename validation rejects Windows device names and characters invalid
on Windows, even when running on Linux, instead of silently renaming remote files.

## Accounts and uploads

`LoginContext` authenticates without loading the account tree or starting pollers.
Configure/authenticate a client before sharing it with concurrent operations. For
public downloads only the session is used; the account master key is never replaced
with a folder key. `AccountInfo` returns data for the application to format.

For uploads, call `FetchNodesContext` once, then pass `client.FS.GetRoot()` or an
owned directory to `UploadPath`. Directories and empty descendants are preserved.
The result includes the new root and its exported share URL. On partial failure,
inspect the returned root; account data is never automatically deleted.
Live upload acceptance remains deferred pending a usable test account.

`SaveSession` and `LoadSession` support reuse without repeated password login.
Session files contain credentials equivalent to a password: keep them outside
source control, use private permissions, and set an explicit user-only Windows ACL.
Default HTTP requests have a 10-second deadline including body reads; configure
`SetTimeOut` before concurrent operations when slower connections require more time.
Cancel active operation contexts before `Close`. The legacy `Login` API retains
upstream's event-polling behavior; use `LoginContext` for the new workflow.

## Validation

```text
go test ./...
go test -race ./...
go vet ./...
go test -run '^$' -bench '^BenchmarkVerifiedDownload$' -benchmem
```

Live tests require explicit opt-in. Public downloads do not require credentials:

```powershell
$env:MEGA_LIVE = '1'
$env:MEGA_PUBLIC_LINK = '<controlled public file or folder URL>'
go test -run '^TestLivePublicDownload$' -v -timeout 17m
```

Optional environment variables:

- `MEGA_METADATA_ONLY=1`: resolve metadata only.
- `MEGA_DOWNLOAD_DIR`: retain verified output (defaults to a temporary test directory).
- `MEGA_DOWNLOAD_TIMEOUT`: operation deadline such as `60m`; also increase `go test -timeout`.
- `MEGA_DOWNLOAD_WORKERS`: bounded chunk workers (1-30, default 3).
- `MEGA_DOWNLOAD_AUTH=1` and `MEGA_SESSION_FILE`: validate downloads with a saved session.

For account tests, set `MEGA_SESSION_FILE`, or supply `MEGA_TEST_EMAIL` and
`MEGA_TEST_PASSWORD` in the process environment; `MEGA_TEST_MFA` is optional.
`TestLiveAccount` reads account metadata. `TestLiveRoundTrip` creates a uniquely
named remote test folder, performs uploads and public downloads, and deletes only
that test folder in cleanup. Never supply credentials to untrusted test changes.

## Reference

Protocol reference: MEGA SDK v10.21.0,
`d1c0cb76cb98598d93b9fb0bb89612daeb0fb39c`.
Public-link design also reviewed upstream PR #62; the implementation below uses
operation-local scope rather than replacing account state with public-folder keys.

Additional legacy account and filesystem APIs are inherited from
[upstream go-mega](https://github.com/t3rm1n4l/go-mega).
