package mega

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestLiveDownloadedContentRoundTrip tests user-controlled local fixtures. It is
// opt-in, and only the uniquely created remote sandbox is removed afterward.
func TestLiveDownloadedContentRoundTrip(t *testing.T) {
	file, folder := os.Getenv("MEGA_TEST_LOCAL_FILE"), os.Getenv("MEGA_TEST_LOCAL_FOLDER")
	if os.Getenv("MEGA_LIVE") != "1" || file == "" || folder == "" {
		t.Skip("set MEGA_LIVE and MEGA_TEST_LOCAL_FILE/MEGA_TEST_LOCAL_FOLDER")
	}
	for _, path := range []string{file, folder} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatal(err)
		}
	}
	m := liveSession(t)
	m.SetUploadWorkers(4)
	m.SetDownloadWorkers(8)
	m.SetTimeOut(30 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	if err := m.FetchNodesContext(ctx); err != nil {
		t.Fatal(err)
	}
	sandbox, err := m.CreateDirContext(ctx, fmt.Sprintf("go-mega-content-acceptance-%d", time.Now().UnixNano()), m.FS.GetRoot())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 45*time.Second)
		defer done()
		if err := m.DeleteContext(cleanup, sandbox, true); err != nil {
			t.Errorf("remove isolated content-test root %s: %v", sandbox.GetHash(), err)
		}
	})
	for _, tc := range []struct{ name, path string }{{"file", file}, {"folder", folder}} {
		t.Run(tc.name, func(t *testing.T) {
			start, lastLog := time.Now(), time.Now()
			uploaded, err := m.UploadPath(ctx, tc.path, sandbox, func(p Progress) {
				if time.Since(lastLog) > 30*time.Second {
					t.Logf("uploaded %d/%d bytes; %.2f MiB/s", p.Transferred, p.Total, float64(p.BytesPerSecond)/(1<<20))
					lastLog = time.Now()
				}
			})
			if err != nil {
				t.Fatal("upload/export:", err)
			}
			t.Logf("upload and export completed: bytes=%d elapsed=%s", uploaded.Size, time.Since(start).Round(time.Millisecond))
			anonymous := New()
			anonymous.SetHTTPS(true)
			anonymous.SetDownloadWorkers(8)
			anonymous.SetTimeOut(30 * time.Second)
			defer anonymous.Close()
			lastLog = time.Now()
			start = time.Now()
			downloaded, err := anonymous.DownloadPublic(ctx, uploaded.Link, t.TempDir(), func(p Progress) {
				if time.Since(lastLog) > 30*time.Second {
					t.Logf("anonymous round-trip downloaded %d/%d bytes", p.Transferred, p.Total)
					lastLog = time.Now()
				}
			})
			if err != nil {
				t.Fatal("anonymous re-download:", err)
			}
			compareLiveTree(t, tc.path, downloaded.Path)
			t.Logf("anonymous re-download verified every MAC, file length, hierarchy and SHA-256; elapsed=%s", time.Since(start).Round(time.Millisecond))
		})
	}
}
