package mega

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Progress describes verified writes (download) or acknowledged bytes (upload).
// Callbacks are serialized per operation and must return promptly.
type Progress struct {
	Name           string
	Transferred    int64
	Total          int64
	BytesPerSecond int64
}
type ProgressFunc func(Progress)

// TransferResult identifies the completed local file or directory.
type TransferResult struct {
	Name string
	Path string
	Size int64
}

type progressReporter struct {
	mu                 sync.Mutex
	name               string
	total, transferred int64
	start              time.Time
	callback           ProgressFunc
}

func (p *progressReporter) add(count int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.transferred += count
	if p.callback != nil {
		elapsed := time.Since(p.start).Seconds()
		speed := int64(0)
		if elapsed > 0 {
			speed = int64(float64(p.transferred) / elapsed)
		}
		p.callback(Progress{p.name, p.transferred, p.total, speed})
	}
}

// runChunks joins every worker before returning, including errors in the last job.
func runChunks(ctx context.Context, count, workers int, fn func(context.Context, int) error) error {
	if workers < 1 {
		return EARGS
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var once sync.Once
	var first error
	var wg sync.WaitGroup
	if workers > count {
		workers = count
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if child.Err() != nil {
					return
				}
				id := int(next.Add(1) - 1)
				if id >= count {
					return
				}
				if err := fn(child, id); err != nil {
					once.Do(func() { first = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

// parallelFiles bounds concurrent files in a folder download; each file still
// uses the client's download workers.
const parallelFiles = 3

// maxSpan matches the MEGA SDK's desktop MAX_REQ_SIZE: one GET streams many
// MAC chunks, so a request round-trip is paid per 16 MiB instead of per 1 MiB.
const maxSpan = 16 << 20

// chunkSpan is the inclusive chunk-id range [first, last] fetched by one GET.
type chunkSpan struct{ first, last int }

// spans groups chunks so every worker gets work: about two spans per worker,
// each between one chunk and maxSpan bytes.
func spans(chunks []chunkSize, workers int) []chunkSpan {
	var total int64
	for _, c := range chunks {
		total += int64(c.size)
	}
	target := min(max(total/int64(2*max(workers, 1)), 1<<20), maxSpan)
	var out []chunkSpan
	for i := 0; i < len(chunks); {
		j, size := i, int64(chunks[i].size)
		for j+1 < len(chunks) && size+int64(chunks[j+1].size) <= target {
			j++
			size += int64(chunks[j].size)
		}
		out = append(out, chunkSpan{i, j})
		i = j + 1
	}
	return out
}

// stallReader cancels its request when no bytes arrive within the timeout,
// which replaces a whole-request deadline that would kill large spans.
type stallReader struct {
	io.Reader
	timer   *time.Timer
	timeout time.Duration
}

func (r stallReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.timer.Reset(r.timeout)
	}
	return n, err
}

var errStalled = fmt.Errorf("MEGA transfer stalled: %w", io.ErrUnexpectedEOF)

// streamSpan fetches chunks *next..last in one GET, verifying and writing each
// chunk as it arrives. *next advances past every chunk that was written, so a
// retry resumes at the first missing chunk.
func (d *Download) streamSpan(ctx context.Context, next *int, last int, buf, scratch []byte, write func(int, []byte) error) error {
	start, _, err := d.ChunkLocation(*next)
	if err != nil {
		return err
	}
	end, size, err := d.ChunkLocation(last)
	if err != nil {
		return err
	}
	base := d.url()
	resp, cancel, err := d.m.openTransfer(ctx, rangeURL(base, start, end+int64(size)))
	if errors.Is(err, EEXPIRED) {
		if base, err = d.refreshURL(ctx, base); err == nil {
			resp, cancel, err = d.m.openTransfer(ctx, rangeURL(base, start, end+int64(size)))
		}
	}
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	timeout := d.m.config.timeout
	body := stallReader{resp.Body, time.AfterFunc(timeout, cancel), timeout}
	defer body.timer.Stop()
	for ; *next <= last; *next++ {
		_, size, _ := d.ChunkLocation(*next)
		chunk := buf[:size]
		if _, err = io.ReadFull(body, chunk); err != nil {
			if ctx.Err() == nil && errors.Is(err, context.Canceled) {
				return errStalled
			}
			return transportError(err)
		}
		if err = d.verifyChunk(*next, chunk, scratch); err != nil {
			return err
		}
		if err = write(*next, chunk); err != nil {
			return err
		}
	}
	return nil
}

// downloadSpan retries streamSpan; any verified progress resets the retry budget.
func (d *Download) downloadSpan(ctx context.Context, s chunkSpan, buf, scratch []byte, write func(int, []byte) error) error {
	next := s.first
	var last error
	for attempt := 0; attempt <= d.m.retries; attempt++ {
		if attempt > 0 {
			if err := waitRetry(ctx, attempt); err != nil {
				return err
			}
		}
		before := next
		last = d.streamSpan(ctx, &next, s.last, buf, scratch, write)
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryable(last) {
			return last
		}
		if next > before {
			attempt = 0
		}
	}
	return last
}

func (m *Mega) downloadNode(ctx context.Context, node *Node, destination string, onBytes func(int64)) error {
	d, err := m.NewDownloadContext(ctx, node)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	write := func(id int, data []byte) error {
		position, _, err := d.ChunkLocation(id)
		if err != nil {
			return err
		}
		n, err := f.WriteAt(data, position)
		if err != nil {
			return err
		}
		if n != len(data) {
			return io.ErrShortWrite
		}
		if onBytes != nil {
			onBytes(int64(n))
		}
		return nil
	}
	work := spans(d.chunks, m.dl_workers)
	err = runChunks(ctx, len(work), m.dl_workers, func(ctx context.Context, id int) error {
		// ponytail: per-span buffers; pool them if GC shows up in profiles.
		return d.downloadSpan(ctx, work[id], make([]byte, 1<<20), make([]byte, macScratch), write)
	})
	if err != nil {
		return err
	}
	if err = d.Finish(); err != nil {
		return err
	}
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if stat.Size() != node.size {
		return EINCOMPLETE
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// Publication is short and serialized across clients in this process. Callers
// must own destination directories; external writers must not race publication.
var publicationMu sync.Mutex

func publishDownload(source, destination string) error {
	publicationMu.Lock()
	defer publicationMu.Unlock()
	if _, err := os.Lstat(destination); err == nil {
		return EEXIST
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}

// Download downloads a resolved resource into destination/name. Existing targets
// are rejected. On failure, the private staging tree is removed, never published.
func (r *PublicResource) Download(ctx context.Context, destination string, progress ProgressFunc) (TransferResult, error) {
	if err := ctx.Err(); err != nil {
		return TransferResult{}, err
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		return TransferResult{}, err
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return TransferResult{}, err
	}
	final := filepath.Join(destination, r.Name())
	if _, err := os.Lstat(final); err == nil {
		return TransferResult{}, EEXIST
	} else if !errors.Is(err, os.ErrNotExist) {
		return TransferResult{}, err
	}
	staging, err := os.MkdirTemp(destination, ".mega-download-")
	if err != nil {
		return TransferResult{}, err
	}
	defer os.RemoveAll(staging)
	reporter := &progressReporter{name: r.Name(), total: r.Size(), start: time.Now(), callback: progress}
	reporter.add(0)
	// Entries list parents first, so folders exist before any file lands.
	var files []int
	for i, entry := range r.entries {
		if !entry.IsFolder {
			files = append(files, i)
		} else if err = os.Mkdir(filepath.Join(staging, filepath.FromSlash(entry.Path)), 0700); err != nil {
			return TransferResult{}, fmt.Errorf("MEGA download: %w", err)
		}
	}
	// Small files never fill the chunk workers, so a few files run at once.
	err = runChunks(ctx, len(files), parallelFiles, func(ctx context.Context, id int) error {
		i := files[id]
		return r.client.downloadNode(ctx, r.nodes[i], filepath.Join(staging, filepath.FromSlash(r.entries[i].Path)), reporter.add)
	})
	if err != nil {
		return TransferResult{}, fmt.Errorf("MEGA download: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return TransferResult{}, err
	}
	if _, err = os.Lstat(final); err == nil {
		return TransferResult{}, EEXIST
	} else if !errors.Is(err, os.ErrNotExist) {
		return TransferResult{}, err
	}
	if err = publishDownload(filepath.Join(staging, r.Name()), final); err != nil {
		return TransferResult{}, err
	}
	return TransferResult{r.Name(), final, r.Size()}, nil
}

// DownloadPublic resolves and downloads a public file or folder.
func (m *Mega) DownloadPublic(ctx context.Context, link, destination string, progress ProgressFunc) (TransferResult, error) {
	r, err := m.ResolvePublic(ctx, link)
	if err != nil {
		return TransferResult{}, err
	}
	return r.Download(ctx, destination, progress)
}

// DownloadFileContext downloads an account/public node to a new destination.
// The parent directory must exist. Progress receives acknowledged byte deltas.
func (m *Mega) DownloadFileContext(ctx context.Context, node *Node, destination string, onBytes func(int64)) error {
	if node == nil || !safeName(node.GetName()) {
		return EARGS
	}
	if _, err := os.Lstat(destination); err == nil {
		return EEXIST
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(destination), ".mega-file-")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	defer os.Remove(name)
	if err = m.downloadNode(ctx, node, name, onBytes); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Publish only into an application-owned output directory.
	if err = publishDownload(name, destination); err != nil {
		return err
	}
	return nil
}

// UploadFileContext uploads a regular file. Symlinks and changing files fail.
func (m *Mega) UploadFileContext(ctx context.Context, path string, parent *Node, name string, onBytes func(int64)) (*Node, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, EARGS
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, EREAD
	}
	if name == "" {
		name = filepath.Base(path)
	}
	if !safeName(name) {
		return nil, EARGS
	}
	u, err := m.NewUploadContext(ctx, parent, name, info.Size())
	if err != nil {
		return nil, err
	}
	err = runChunks(ctx, u.Chunks(), m.ul_workers, func(ctx context.Context, id int) error {
		offset, size, err := u.ChunkLocation(id)
		if err != nil {
			return err
		}
		data := make([]byte, size)
		n, err := f.ReadAt(data, offset)
		if err != nil && !(err == io.EOF && n == size) {
			return err
		}
		if n != size {
			return io.ErrUnexpectedEOF
		}
		if err = u.UploadChunkContext(ctx, id, data); err != nil {
			return err
		}
		if onBytes != nil {
			onBytes(int64(n))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, EREAD
	}
	return u.FinishContext(ctx)
}
