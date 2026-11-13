package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func testClient(server *httptest.Server, mutate ...func(*Options)) *Client {
	opts := Options{
		Prefixes:         []string{server.URL + "/"},
		RetryBaseDelay:   time.Millisecond,
		ProgressInterval: time.Millisecond,
	}
	for _, fn := range mutate {
		fn(&opts)
	}
	return New(opts)
}

func serveContent(content []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "model.bin", time.Time{}, bytes.NewReader(content))
	}
}

func TestFetchVerifyCommitDownloadsFile(t *testing.T) {
	content := []byte("yaomu-release-payload-0123456789")
	server := httptest.NewServer(serveContent(content))
	defer server.Close()

	root := t.TempDir()
	file := File{
		RelPath: "bin/model.bin",
		URL:     server.URL + "/model.bin",
		SHA256:  sha256Hex(content),
		Size:    int64(len(content)),
	}
	client := testClient(server)

	var lastWritten, lastTotal int64
	calls := 0
	if err := client.Fetch(context.Background(), root, file, func(written, total int64) {
		calls++
		lastWritten, lastTotal = written, total
	}); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if lastWritten != file.Size || lastTotal != file.Size {
		t.Fatalf("progress = %d/%d, want %d/%d", lastWritten, lastTotal, file.Size, file.Size)
	}
	if calls == 0 {
		t.Fatal("progress callback was not called")
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "model.bin.part")); err != nil {
		t.Fatalf("expected .part before Commit: %v", err)
	}
	if err := client.Verify(context.Background(), root, file); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if err := client.Commit(root, file); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "bin", "model.bin"))
	if err != nil {
		t.Fatalf("read committed file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("committed content = %q, want %q", got, content)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "model.bin.part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("residual .part after Commit: %v", err)
	}
}

func TestFetchResumesAfterInterruptedResponse(t *testing.T) {
	content := []byte("0123456789abcdef")
	var requests atomic.Int32
	var sawRange atomic.Bool
	var rangeOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			if _, err := w.Write(content[:4]); err != nil {
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		if r.Header.Get("Range") == "bytes=4-" {
			rangeOnce.Do(func() { sawRange.Store(true) })
		}
		http.ServeContent(w, r, "model.bin", time.Time{}, bytes.NewReader(content))
	}))
	defer server.Close()

	root := t.TempDir()
	file := File{RelPath: "model.bin", URL: server.URL + "/model.bin", SHA256: sha256Hex(content), Size: int64(len(content))}
	client := testClient(server)
	if err := client.Fetch(context.Background(), root, file, nil); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if !sawRange.Load() {
		t.Fatalf("retry did not use Range resume (requests=%d)", requests.Load())
	}
	if err := client.Verify(context.Background(), root, file); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if err := client.Commit(root, file); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "model.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("committed content = %q, err = %v", got, err)
	}
}

func TestFetchRestartsWhenServerIgnoresRange(t *testing.T) {
	content := []byte("restart-full-body")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 忽略 Range：总是 200 + 完整内容，客户端必须丢弃旧 .part 从头写入。
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "model.bin.part"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := File{RelPath: "model.bin", URL: server.URL + "/model.bin", SHA256: sha256Hex(content), Size: int64(len(content))}
	client := testClient(server)
	if err := client.Fetch(context.Background(), root, file, nil); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if err := client.Verify(context.Background(), root, file); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "model.bin.part"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("partial content = %q, err = %v", got, err)
	}
}

func TestVerifyRejectsTamperedContent(t *testing.T) {
	content := []byte("tampered-bytes")
	server := httptest.NewServer(serveContent(content))
	defer server.Close()

	root := t.TempDir()
	file := File{
		RelPath: "model.bin",
		URL:     server.URL + "/model.bin",
		SHA256:  sha256Hex([]byte("expected-bytes")),
		Size:    int64(len(content)),
	}
	client := testClient(server)
	if err := client.Fetch(context.Background(), root, file, nil); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	err := client.Verify(context.Background(), root, file)
	var hashErr *HashError
	if !errors.As(err, &hashErr) {
		t.Fatalf("Verify() error = %v, want *HashError", err)
	}
	if !IsPermanent(err) {
		t.Fatal("hash mismatch must be permanent")
	}
}

func TestFetchRejectsOversizedResponse(t *testing.T) {
	content := make([]byte, 32)
	server := httptest.NewServer(serveContent(content))
	defer server.Close()

	root := t.TempDir()
	file := File{
		RelPath: "model.bin",
		URL:     server.URL + "/model.bin",
		SHA256:  sha256Hex([]byte("short")),
		Size:    4,
	}
	client := testClient(server)
	err := client.Fetch(context.Background(), root, file, nil)
	var sizeErr *SizeError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("Fetch() error = %v, want *SizeError", err)
	}
	if !IsPermanent(err) {
		t.Fatal("size mismatch must be permanent")
	}
	if _, statErr := os.Stat(filepath.Join(root, "model.bin.part")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("residual .part after size failure: %v", statErr)
	}
}

func TestFetchCancelRemovesPartialFile(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		if _, err := w.Write([]byte("abcd")); err != nil {
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	root := t.TempDir()
	file := File{RelPath: "model.bin", URL: server.URL + "/model.bin", SHA256: sha256Hex(make([]byte, 10)), Size: 10}
	client := testClient(server)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- client.Fetch(ctx, root, file, nil) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not start streaming")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Fetch() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Fetch did not return after cancel")
	}
	if _, err := os.Stat(filepath.Join(root, "model.bin.part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("residual .part after cancel: %v", err)
	}
}

func TestFetchRetriesTransientStatus(t *testing.T) {
	content := []byte("retry-me")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.ServeContent(w, r, "model.bin", time.Time{}, bytes.NewReader(content))
	}))
	defer server.Close()

	root := t.TempDir()
	file := File{RelPath: "model.bin", URL: server.URL + "/model.bin", SHA256: sha256Hex(content), Size: int64(len(content))}
	client := testClient(server)
	if err := client.Fetch(context.Background(), root, file, nil); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if err := client.Verify(context.Background(), root, file); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestFetchExhaustsRetriesAndCleansPartial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	root := t.TempDir()
	file := File{RelPath: "model.bin", URL: server.URL + "/model.bin", SHA256: sha256Hex([]byte("x")), Size: 1}
	client := testClient(server, func(opts *Options) { opts.MaxAttempts = 2 })
	err := client.Fetch(context.Background(), root, file, nil)
	var transferErr *TransferError
	if !errors.As(err, &transferErr) {
		t.Fatalf("Fetch() error = %v, want *TransferError", err)
	}
	if transferErr.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", transferErr.Attempts)
	}
	if _, statErr := os.Stat(filepath.Join(root, "model.bin.part")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("residual .part after exhausted retries: %v", statErr)
	}
}

func TestFetchRejectsUnapprovedURL(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(Options{Prefixes: []string{"https://approved.example/"}})
	file := File{RelPath: "model.bin", URL: server.URL + "/model.bin", SHA256: sha256Hex([]byte("x")), Size: 1}
	err := client.Fetch(context.Background(), t.TempDir(), file, nil)
	if !errors.Is(err, ErrURLNotAllowed) {
		t.Fatalf("Fetch() error = %v, want ErrURLNotAllowed", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("unapproved source was contacted %d times", requests.Load())
	}
}

func TestFetchRejectsRedirectOutsideApprovedPrefix(t *testing.T) {
	content := []byte("approved-payload")
	var outsideRequests atomic.Int32
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outsideRequests.Add(1)
		http.ServeContent(w, r, "model.bin", time.Time{}, bytes.NewReader(content))
	}))
	defer outside.Close()
	approved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, outside.URL+"/model.bin", http.StatusFound)
	}))
	defer approved.Close()

	root := t.TempDir()
	file := File{RelPath: "model.bin", URL: approved.URL + "/model.bin", SHA256: sha256Hex(content), Size: int64(len(content))}
	client := testClient(approved)
	err := client.Fetch(context.Background(), root, file, nil)
	if !errors.Is(err, ErrURLNotAllowed) {
		t.Fatalf("Fetch() error = %v, want ErrURLNotAllowed", err)
	}
	if outsideRequests.Load() != 0 {
		t.Fatalf("unapproved redirect target was contacted %d times", outsideRequests.Load())
	}
	if _, statErr := os.Stat(filepath.Join(root, "model.bin.part")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("residual .part after rejected redirect: %v", statErr)
	}
}

func TestValidateFileRejectsTraversalAndBadDigests(t *testing.T) {
	validURL := "https://releases.example/model.bin"
	cases := []File{
		{RelPath: "../escape.bin", URL: validURL, SHA256: sha256Hex(nil), Size: 0},
		{RelPath: "/abs.bin", URL: validURL, SHA256: sha256Hex(nil), Size: 0},
		{RelPath: `\abs.bin`, URL: validURL, SHA256: sha256Hex(nil), Size: 0},
		{RelPath: "a/../../b.bin", URL: validURL, SHA256: sha256Hex(nil), Size: 0},
		{RelPath: "model.bin", URL: validURL, SHA256: "not-hex", Size: 0},
		{RelPath: "model.bin", URL: "file:///etc/passwd", SHA256: sha256Hex(nil), Size: 0},
		{RelPath: "model.bin", URL: validURL, SHA256: sha256Hex(nil), Size: -1},
	}
	for _, file := range cases {
		if err := ValidateFile(file); !errors.Is(err, ErrInvalidFile) {
			t.Errorf("ValidateFile(%+v) = %v, want ErrInvalidFile", file, err)
		}
	}
}

func TestFetchReusesVerifiedCommittedFile(t *testing.T) {
	content := []byte("already-installed")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.ServeContent(w, r, "model.bin", time.Time{}, bytes.NewReader(content))
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "model.bin"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	// 上一次尝试遗留的半成品：落位文件可信时必须清掉，否则 Verify 会误读 `.part`。
	if err := os.WriteFile(filepath.Join(root, "model.bin.part"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := File{RelPath: "model.bin", URL: server.URL + "/model.bin", SHA256: sha256Hex(content), Size: int64(len(content))}
	client := testClient(server)
	if err := client.Fetch(context.Background(), root, file, nil); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("verified file was re-downloaded (%d requests)", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "model.bin.part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale .part survived reuse: %v", err)
	}
	if err := client.Verify(context.Background(), root, file); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if err := client.Commit(root, file); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

func TestCleanupRemovesOnlyPartialFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"a.part":          "partial",
		"nested/b.part":   "partial",
		"nested/keep.bin": "keep",
		"keep.bin":        "keep",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client := New(Options{})
	if err := client.Cleanup(root); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	for _, name := range []string{"keep.bin", "nested/keep.bin"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}
	for _, name := range []string{"a.part", "nested/b.part"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists: %v", name, err)
		}
	}
}

func TestContentRangeStart(t *testing.T) {
	cases := []struct {
		value string
		want  int64
		ok    bool
	}{
		{"bytes 4-9/10", 4, true},
		{"  bytes 0-1/2  ", 0, true},
		{"items 4-9/10", 0, false},
		{"bytes -9/10", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := contentRangeStart(tc.value)
		if got != tc.want || ok != tc.ok {
			t.Errorf("contentRangeStart(%q) = (%d,%v), want (%d,%v)", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAllowedMatchesOnlyConfiguredPrefix(t *testing.T) {
	client := New(Options{Prefixes: []string{
		"https://releases.example/base/",
		"http://127.0.0.1:8080",
		// 生产分发源必须是 https：非回环 http 前缀被丢弃。
		"http://mirror.example/releases",
	}})
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://releases.example/base/model.bin", true},
		{"https://releases.example/base/nested/model.bin", true},
		{"https://releases.example/base", true},
		{"https://releases.example/other/model.bin", false},
		{"https://releases.example.evil/base/model.bin", false},
		{"https://releases.example/base/../secret", true}, // 前缀匹配后仍可能被路径解析；HTTP 客户端会规范化
		{"http://127.0.0.1:8080/model.bin", true},
		{"http://127.0.0.1:9090/model.bin", false},
		{"http://mirror.example/releases/model.bin", false},
		{"ftp://releases.example/base/model.bin", false},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.raw, err)
		}
		if got := client.allowed(u); got != tc.want {
			t.Errorf("allowed(%s) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
