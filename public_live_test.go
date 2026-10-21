package mega

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLivePublicDownload(t *testing.T) {
	if os.Getenv("MEGA_LIVE") != "1" || os.Getenv("MEGA_PUBLIC_LINK") == "" {
		t.Skip("set MEGA_LIVE and MEGA_PUBLIC_LINK")
	}
	m := New()
	m.SetHTTPS(true)
	defer m.Close()
	if os.Getenv("MEGA_DOWNLOAD_AUTH") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := m.LoadSession(ctx, os.Getenv("MEGA_SESSION_FILE"))
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	timeout := 15 * time.Minute
	if value := os.Getenv("MEGA_DOWNLOAD_TIMEOUT"); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	if value := os.Getenv("MEGA_DOWNLOAD_WORKERS"); value != "" {
		workers, err := strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = m.SetDownloadWorkers(workers); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resource, err := m.ResolvePublic(ctx, os.Getenv("MEGA_PUBLIC_LINK"))
	if err != nil {
		t.Fatal("resolve public link:", err)
	}
	t.Logf("resolved metadata: name=%s size=%d bytes folder=%t entries=%d", resource.Name(), resource.Size(), resource.IsFolder(), len(resource.Entries()))
	if os.Getenv("MEGA_METADATA_ONLY") == "1" {
		for _, entry := range resource.Entries() {
			t.Logf("entry: path=%s bytes=%d folder=%t", entry.Path, entry.Size, entry.IsFolder)
		}
		return
	}
	destination := os.Getenv("MEGA_DOWNLOAD_DIR")
	if destination == "" {
		destination = t.TempDir()
	}
	start := time.Now()
	lastLog := time.Now()
	result, err := resource.Download(ctx, destination, func(p Progress) {
		if time.Since(lastLog) >= 30*time.Second {
			t.Logf("downloaded %d/%d bytes; %.2f MiB/s", p.Transferred, p.Total, float64(p.BytesPerSecond)/(1<<20))
			lastLog = time.Now()
		}
	})
	if err != nil {
		t.Fatal("verified public download:", err)
	}
	if resource.IsFolder() {
		var count int
		var total int64
		for _, entry := range resource.Entries() {
			relative, err := filepath.Rel(resource.Name(), filepath.FromSlash(entry.Path))
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(filepath.Join(result.Path, relative))
			if err != nil {
				t.Fatal(err)
			}
			if info.IsDir() != entry.IsFolder {
				t.Fatal("entry type mismatch")
			}
			if !entry.IsFolder {
				if info.Size() != entry.Size {
					t.Fatal("entry length mismatch")
				}
				count++
				total += info.Size()
			}
		}
		if total != resource.Size() {
			t.Fatal("folder byte total mismatch")
		}
		t.Logf("verified %d files: exact sizes, MEGA MACs, and folder hierarchy", count)
	}
	if !resource.IsFolder() {
		file, err := os.Open(result.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		hash := sha256.New()
		n, err := io.Copy(hash, file)
		if err != nil {
			t.Fatal(err)
		}
		if n != resource.Size() {
			t.Fatal("file length mismatch")
		}
		t.Logf("verified exact size and MEGA MAC; SHA256=%x", hash.Sum(nil))
	}
	t.Logf("completed in %s; %.2f MiB/s; output=%s", time.Since(start).Round(time.Millisecond), float64(result.Size)/(1<<20)/time.Since(start).Seconds(), filepath.Base(result.Path))
}
