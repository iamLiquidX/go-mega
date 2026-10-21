package mega

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func BenchmarkVerifiedDownload(b *testing.B) {
	data := bytes.Repeat([]byte{1}, 1<<20)
	key, encrypted := fixtureFile(data)
	meta, _ := fileMeta(key)
	expected := sha256.Sum256(data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cs" {
			json.NewEncoder(w).Encode([]any{map[string]any{"s": len(data), "at": fixtureAttr(meta.key, "bench.bin"), "g": "http://" + r.Host + "/bytes"}})
			return
		}
		var first, last int
		fmt.Sscanf(r.URL.Path, "/bytes/%d-%d", &first, &last)
		w.Write(encrypted[first : last+1])
	}))
	defer server.Close()
	m := New()
	defer m.Close()
	m.SetAPIUrl(server.URL)
	link := "https://mega.nz/file/" + fixtureHandle + "#" + base64urlencode(key)
	dest := b.TempDir()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		res, err := m.DownloadPublic(context.Background(), link, dest, nil)
		if err != nil {
			b.Fatal(err)
		}
		got, _ := os.ReadFile(res.Path)
		if sha256.Sum256(got) != expected {
			b.Fatal("corrupt benchmark output")
		}
		os.Remove(res.Path)
	}
}
