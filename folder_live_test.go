package mega

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveFolderChildLink(t *testing.T) {
	if os.Getenv("MEGA_LIVE") != "1" || os.Getenv("MEGA_PUBLIC_LINK") == "" {
		t.Skip("set MEGA_LIVE and a controlled public folder link")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	m := New()
	m.SetHTTPS(true)
	defer m.Close()
	link := os.Getenv("MEGA_PUBLIC_LINK")
	resource, err := m.ResolvePublic(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	if !resource.IsFolder() {
		t.Skip("a folder is required")
	}
	var smallest *Entry
	var subfolder *Entry
	for _, entry := range resource.Entries() {
		entry := entry
		if entry.IsFolder {
			if entry.Path != resource.Name() {
				subfolder = &entry
			}
			continue
		}
		if smallest == nil || entry.Size < smallest.Size {
			smallest = &entry
		}
	}
	if subfolder != nil {
		selected, err := m.ResolvePublic(ctx, link+"/folder/"+subfolder.Handle)
		if err != nil {
			t.Fatal(err)
		}
		if !selected.IsFolder() || selected.Name() != filepath.Base(subfolder.Path) {
			t.Fatal("subfolder metadata mismatch")
		}
		t.Log("linked subfolder metadata verified")
	} else {
		t.Log("supplied folder has no nested folder; nested subfolder behavior is covered by deterministic fixtures")
	}
	if smallest == nil {
		t.Skip("no file in folder")
	}
	selected, err := m.ResolvePublic(ctx, link+"/file/"+smallest.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if selected.IsFolder() || selected.Size() != smallest.Size {
		t.Fatal("selected file metadata mismatch")
	}
	result, err := selected.Download(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, err := os.Open(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer downloaded.Close()
	actual := sha256.New()
	if _, err = io.Copy(actual, downloaded); err != nil {
		t.Fatal(err)
	}
	if directory := os.Getenv("MEGA_EXISTING_FOLDER_DIR"); directory != "" {
		expectedFile, err := os.Open(filepath.Join(directory, filepath.FromSlash(smallest.Path)))
		if err != nil {
			t.Fatal(err)
		}
		defer expectedFile.Close()
		expected := sha256.New()
		if _, err = io.Copy(expected, expectedFile); err != nil {
			t.Fatal(err)
		}
		if string(actual.Sum(nil)) != string(expected.Sum(nil)) {
			t.Fatal("selected-file hash differs from recursive download")
		}
	}
	t.Logf("linked file verified: bytes=%d SHA256=%x", smallest.Size, actual.Sum(nil))
}
