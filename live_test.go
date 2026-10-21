package mega

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func liveSession(t *testing.T) *Mega {
	t.Helper()
	if os.Getenv("MEGA_LIVE") != "1" {
		t.Skip("set MEGA_LIVE=1 for controlled account tests")
	}
	m := New()
	m.SetHTTPS(true)
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sessionPath := os.Getenv("MEGA_SESSION_FILE")
	if sessionPath != "" {
		err := m.LoadSession(ctx, sessionPath)
		if err == nil {
			t.Log("reused saved account session")
			return m
		}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ESID) {
			t.Fatalf("restore session: %v", err)
		}
	}
	email, password := os.Getenv("MEGA_TEST_EMAIL"), os.Getenv("MEGA_TEST_PASSWORD")
	if email == "" || password == "" {
		t.Fatal("set MEGA_TEST_EMAIL/MEGA_TEST_PASSWORD or a valid MEGA_SESSION_FILE")
	}
	if err := m.MultiFactorLoginContext(ctx, email, password, os.Getenv("MEGA_TEST_MFA")); err != nil {
		t.Fatalf("live login: %v", err)
	}
	if sessionPath != "" {
		if err := m.SaveSession(sessionPath); err != nil {
			t.Fatal("save session:", err)
		}
	}
	t.Log("authenticated account; credentials/session are not logged")
	return m
}

func TestLiveAccount(t *testing.T) {
	m := liveSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	info, err := m.AccountInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Email == "" || info.StorageTotal == 0 {
		t.Fatal("incomplete account information")
	}
	t.Logf("account metadata verified; plan=%d", info.Plan)
	if err = m.FetchNodesContext(ctx); err != nil {
		t.Fatal("fetch account nodes:", err)
	}
	if m.FS.GetRoot() == nil {
		t.Fatal("missing account root")
	}
}

func compareLiveTree(t *testing.T, want, got string) {
	t.Helper()
	expected := map[string][32]byte{}
	actual := map[string][32]byte{}
	scan := func(root string, out map[string][32]byte) {
		t.Helper()
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				out[relative] = [32]byte{}
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[relative] = sha256.Sum256(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	scan(want, expected)
	scan(got, actual)
	if len(expected) != len(actual) {
		t.Fatalf("tree entry count: want=%d got=%d", len(expected), len(actual))
	}
	for path, hash := range expected {
		if got, ok := actual[path]; !ok || got != hash {
			t.Fatalf("tree mismatch at %s", path)
		}
	}
}

func TestLiveRoundTrip(t *testing.T) {
	m := liveSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := m.FetchNodesContext(ctx); err != nil {
		t.Fatal(err)
	}
	sandbox, err := m.CreateDirContext(ctx, fmt.Sprintf("go-mega-acceptance-%d", time.Now().UnixNano()), m.FS.GetRoot())
	if err != nil {
		t.Fatal("create test root:", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 45*time.Second)
		defer done()
		if err := m.DeleteContext(cleanup, sandbox, true); err != nil {
			t.Errorf("remove isolated live test root %s: %v", sandbox.GetHash(), err)
		}
	})
	local := filepath.Join(t.TempDir(), "roundtrip")
	if err = os.MkdirAll(filepath.Join(local, "nested", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"zero.bin": {}, "nested/日本語.txt": []byte("Go MEGA public folder test\n"), "nested/chunks.bin": bytes.Repeat([]byte{0, 1, 2, 255}, (2<<20)+19)}
	for name, data := range files {
		if err = os.WriteFile(filepath.Join(local, filepath.FromSlash(name)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(local, "nested", "chunks.bin")
		result, err := m.UploadPath(ctx, path, sandbox, nil)
		if err != nil {
			t.Fatal("upload/export file:", err)
		}
		for _, authenticated := range []bool{false, true} {
			client := New()
			client.SetHTTPS(true)
			defer client.Close()
			if authenticated {
				client.sid = m.sid
			}
			download, err := client.DownloadPublic(ctx, result.Link, t.TempDir(), nil)
			if err != nil {
				t.Fatalf("download file (account=%t): %v", authenticated, err)
			}
			expected, _ := os.ReadFile(path)
			actual, _ := os.ReadFile(download.Path)
			if sha256.Sum256(expected) != sha256.Sum256(actual) {
				t.Fatal("file hash mismatch")
			}
		}
	})
	t.Run("folder", func(t *testing.T) {
		result, err := m.UploadPath(ctx, local, sandbox, nil)
		if err != nil {
			t.Fatal("upload/export folder:", err)
		}
		for _, authenticated := range []bool{false, true} {
			client := New()
			client.SetHTTPS(true)
			defer client.Close()
			if authenticated {
				client.sid = m.sid
			}
			download, err := client.DownloadPublic(ctx, result.Link, t.TempDir(), nil)
			if err != nil {
				t.Fatalf("download folder (account=%t): %v", authenticated, err)
			}
			compareLiveTree(t, local, download.Path)
		}
		children, err := m.FS.GetChildren(result.Node)
		if err != nil {
			t.Fatal(err)
		}
		for _, child := range children {
			if child.GetName() == "nested" {
				client := New()
				defer client.Close()
				download, err := client.DownloadPublic(ctx, result.Link+"/folder/"+child.GetHash(), t.TempDir(), nil)
				if err != nil {
					t.Fatal("linked subfolder:", err)
				}
				compareLiveTree(t, filepath.Join(local, "nested"), download.Path)
			}
		}
	})
}
