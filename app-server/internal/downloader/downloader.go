// Package downloader 实现模型发布包的本地下载与校验。
//
// 语义（docs/flashcli-integration.md §2/§5、FR-MODEL-02）：
//   - 只接受经批准分发源前缀的 URL（默认拒绝，前缀由调用方配置注入）；
//     分发源必须 https，仅回环地址允许 http；重定向目标同样校验；
//   - 清单文件相对安装目录落位，拒绝绝对路径与 `..` 逃逸；
//   - 下载先写 `<dest>.part`，支持 Range 断点续传（服务器忽略 Range 时从头重下）；
//   - 响应大小不得超过清单声明的大小；
//   - 网络/5xx/408/429 按指数退避重试，哈希/大小不符与 4xx 不重试；
//   - 取消或最终失败删除 `.part`，不留下半成品；校验通过后由调用方 Commit 原子落位。
//
// 本包不解析发布目录、不访问数据库，也不定义 FlashCLI 参数。
package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// File 是安装清单中的一个文件（最小形状 {rel_path,url,sha256,size}）。
type File struct {
	RelPath string `json:"rel_path"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

var (
	// ErrURLNotAllowed 表示下载源不在批准前缀内（默认拒绝）。
	ErrURLNotAllowed = errors.New("downloader: release url is not approved")
	// ErrInvalidFile 表示清单文件或落位路径不合法。
	ErrInvalidFile = errors.New("downloader: invalid release file")
)

// SizeError 表示响应/文件大小与清单不符（不可重试）。
type SizeError struct {
	RelPath  string
	Expected int64
	Actual   int64
}

func (e *SizeError) Error() string {
	return fmt.Sprintf("downloader: %s size mismatch: expected %d bytes, got %d", e.RelPath, e.Expected, e.Actual)
}

// HashError 表示 SHA-256 与清单不符（不可重试）。
type HashError struct {
	RelPath  string
	Expected string
	Actual   string
}

func (e *HashError) Error() string {
	return fmt.Sprintf("downloader: %s sha256 mismatch: expected %s, got %s", e.RelPath, e.Expected, e.Actual)
}

// StatusError 表示源返回了不可接受的 HTTP 状态。
type StatusError struct {
	RelPath   string
	Status    int
	Retryable bool
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("downloader: %s returned HTTP %d", e.RelPath, e.Status)
}

// TransferError 表示重试耗尽后的网络/服务器错误。
type TransferError struct {
	RelPath  string
	Attempts int
	Err      error
}

func (e *TransferError) Error() string {
	return fmt.Sprintf("downloader: %s failed after %d attempts: %v", e.RelPath, e.Attempts, e.Err)
}

// Unwrap 暴露底层错误。
func (e *TransferError) Unwrap() error { return e.Err }

// IsPermanent 报告错误是否不应重试（哈希/大小不符、非重试状态、URL 或路径非法）。
func IsPermanent(err error) bool {
	var sizeErr *SizeError
	var hashErr *HashError
	var statusErr *StatusError
	switch {
	case errors.As(err, &sizeErr), errors.As(err, &hashErr):
		return true
	case errors.As(err, &statusErr):
		return !statusErr.Retryable
	case errors.Is(err, ErrURLNotAllowed), errors.Is(err, ErrInvalidFile):
		return true
	default:
		return false
	}
}

// Options 是下载器配置。
type Options struct {
	// Prefixes 是批准的分发源 URL 前缀（scheme、host、可选路径）；空表示拒绝全部。
	// 分发源必须是 https；仅回环地址（本地假源/开发镜像）允许 http。
	Prefixes []string
	// Client 可注入测试用 HTTP 客户端；重定向仍会被前缀校验。
	Client *http.Client
	// MaxAttempts 是每个文件的总尝试次数（默认 3）。
	MaxAttempts int
	// RetryBaseDelay 是指数退避的初始延迟（默认 200ms）。
	RetryBaseDelay time.Duration
	// ProgressInterval 是进度回调节流间隔（默认 200ms）。
	ProgressInterval time.Duration
	// MaxRedirects 是允许的重定向次数（默认 5）。
	MaxRedirects int
}

type prefix struct {
	scheme string
	host   string
	path   string
}

// Client 是纯下载器；并发调用需由调用方按安装目录隔离。
type Client struct {
	prefixes         []prefix
	client           *http.Client
	maxAttempts      int
	retryBaseDelay   time.Duration
	progressInterval time.Duration
}

// New 构造下载器。
func New(opts Options) *Client {
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	retryBaseDelay := opts.RetryBaseDelay
	if retryBaseDelay <= 0 {
		retryBaseDelay = 200 * time.Millisecond
	}
	progressInterval := opts.ProgressInterval
	if progressInterval <= 0 {
		progressInterval = 200 * time.Millisecond
	}
	maxRedirects := opts.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = 5
	}

	c := &Client{
		prefixes:         parsePrefixes(opts.Prefixes),
		maxAttempts:      maxAttempts,
		retryBaseDelay:   retryBaseDelay,
		progressInterval: progressInterval,
	}
	base := opts.Client
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	previousCheck := base.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("%w: too many redirects", ErrURLNotAllowed)
		}
		if !c.allowed(req.URL) {
			return fmt.Errorf("%w: redirect target rejected", ErrURLNotAllowed)
		}
		if previousCheck != nil {
			return previousCheck(req, via)
		}
		return nil
	}
	c.client = &client
	return c
}

func parsePrefixes(values []string) []prefix {
	out := make([]prefix, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || !sourceSchemeAllowed(u) {
			continue
		}
		out = append(out, prefix{
			scheme: strings.ToLower(u.Scheme),
			host:   strings.ToLower(u.Host),
			path:   strings.TrimSuffix(u.EscapedPath(), "/"),
		})
	}
	return out
}

// sourceSchemeAllowed 限制分发源协议：生产源必须 https；
// 只有回环地址（httptest 假源、本机开发镜像）允许 http。
func sourceSchemeAllowed(u *url.URL) bool {
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		return isLoopbackHost(u.Hostname())
	default:
		return false
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// allowed 报告 URL 是否命中批准前缀。
func (c *Client) allowed(u *url.URL) bool {
	if u == nil || u.Host == "" || u.User != nil || !sourceSchemeAllowed(u) {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	path := u.EscapedPath()
	for _, p := range c.prefixes {
		if scheme != p.scheme || host != p.host {
			continue
		}
		if p.path == "" || path == p.path || strings.HasPrefix(path, p.path+"/") {
			return true
		}
	}
	return false
}

// ValidateFile 校验清单条目的最小形状。
func ValidateFile(file File) error {
	if file.RelPath == "" {
		return fmt.Errorf("%w: rel_path is required", ErrInvalidFile)
	}
	if file.Size < 0 {
		return fmt.Errorf("%w: %s size must be >= 0", ErrInvalidFile, file.RelPath)
	}
	if len(file.SHA256) != sha256.Size*2 {
		return fmt.Errorf("%w: %s sha256 must be 64 hex characters", ErrInvalidFile, file.RelPath)
	}
	if _, err := hex.DecodeString(file.SHA256); err != nil {
		return fmt.Errorf("%w: %s sha256 is not hex", ErrInvalidFile, file.RelPath)
	}
	u, err := url.Parse(file.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%w: %s url must be http(s)", ErrInvalidFile, file.RelPath)
	}
	if _, err := resolvePath(".", file.RelPath); err != nil {
		return err
	}
	return nil
}

// Fetch 下载单个文件到 `<root>/<rel_path>.part`，支持续传与重试。
//
// 成功返回时 `.part` 已包含完整文件；调用方应随后 Verify 并 Commit。
// 取消、永久错误或重试耗尽时会删除 `.part`。
func (c *Client) Fetch(ctx context.Context, root string, file File, onProgress func(written, total int64)) error {
	if err := ValidateFile(file); err != nil {
		return err
	}
	u, err := url.Parse(file.URL)
	if err != nil || !c.allowed(u) {
		return ErrURLNotAllowed
	}
	dest, err := resolvePath(root, file.RelPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("downloader: create directory for %s: %w", file.RelPath, err)
	}
	part := dest + ".part"
	gate := &progressGate{interval: c.progressInterval, fn: onProgress}

	// 完整文件已落位（例如上次安装/重试留下的同版本文件）：校验通过后直接复用。
	if reuse, reuseErr := c.reusable(ctx, dest, file); reuseErr == nil && reuse {
		// 落位文件可信时，同名 `.part` 一定是旧尝试残留；留着会让 Verify 误读半成品。
		if err := removeIfExists(part); err != nil {
			return fmt.Errorf("downloader: clean stale %s.part: %w", file.RelPath, err)
		}
		gate.report(file.Size, file.Size)
		return nil
	}

	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		lastErr = c.fetchOnce(ctx, file, part, gate)
		if lastErr == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = removeIfExists(part)
			return ctxErr
		}
		if IsPermanent(lastErr) {
			_ = removeIfExists(part)
			return lastErr
		}
		if attempt == c.maxAttempts {
			break
		}
		delay := c.retryBaseDelay << (attempt - 1)
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			_ = removeIfExists(part)
			return ctx.Err()
		}
	}
	_ = removeIfExists(part)
	return &TransferError{RelPath: file.RelPath, Attempts: c.maxAttempts, Err: lastErr}
}

func (c *Client) fetchOnce(ctx context.Context, file File, part string, gate *progressGate) error {
	offset := int64(0)
	if info, err := os.Stat(part); err == nil {
		offset = info.Size()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if offset > file.Size {
		offset = 0
		if err := removeIfExists(part); err != nil {
			return err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, file.URL, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFile, err)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	writeOffset := offset
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, ok := contentRangeStart(resp.Header.Get("Content-Range"))
		if !ok || start != offset {
			_ = removeIfExists(part)
			return &StatusError{RelPath: file.RelPath, Status: resp.StatusCode, Retryable: true}
		}
	case http.StatusOK:
		writeOffset = 0
	case http.StatusRequestedRangeNotSatisfiable:
		if offset == file.Size {
			gate.report(file.Size, file.Size)
			return nil
		}
		_ = removeIfExists(part)
		return &StatusError{RelPath: file.RelPath, Status: resp.StatusCode, Retryable: true}
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return &StatusError{RelPath: file.RelPath, Status: resp.StatusCode, Retryable: true}
	default:
		return &StatusError{RelPath: file.RelPath, Status: resp.StatusCode, Retryable: false}
	}

	remaining := file.Size - writeOffset
	if resp.ContentLength >= 0 && resp.ContentLength > remaining {
		return &SizeError{RelPath: file.RelPath, Expected: file.Size, Actual: writeOffset + resp.ContentLength}
	}

	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("downloader: open %s.part: %w", file.RelPath, err)
	}
	if writeOffset == 0 {
		err = out.Truncate(0)
		if err == nil {
			_, err = out.Seek(0, io.SeekStart)
		}
	} else {
		_, err = out.Seek(writeOffset, io.SeekStart)
	}
	if err != nil {
		_ = out.Close()
		return fmt.Errorf("downloader: prepare %s.part: %w", file.RelPath, err)
	}

	written := writeOffset
	buf := make([]byte, 256*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if written+int64(n) > file.Size {
				_ = out.Close()
				return &SizeError{RelPath: file.RelPath, Expected: file.Size, Actual: written + int64(n)}
			}
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				_ = out.Close()
				return fmt.Errorf("downloader: write %s.part: %w", file.RelPath, writeErr)
			}
			written += int64(n)
			gate.report(written, file.Size)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = out.Close()
			return readErr
		}
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("downloader: sync %s.part: %w", file.RelPath, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("downloader: close %s.part: %w", file.RelPath, err)
	}
	if written != file.Size {
		return &SizeError{RelPath: file.RelPath, Expected: file.Size, Actual: written}
	}
	gate.report(file.Size, file.Size)
	return nil
}

// Verify 校验已下载文件（优先 `.part`，否则已落位的最终文件）的大小与 SHA-256。
func (c *Client) Verify(ctx context.Context, root string, file File) error {
	if err := ValidateFile(file); err != nil {
		return err
	}
	dest, err := resolvePath(root, file.RelPath)
	if err != nil {
		return err
	}
	path := dest + ".part"
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		path = dest
		info, err = os.Stat(path)
	}
	if err != nil {
		return fmt.Errorf("downloader: verify %s: %w", file.RelPath, err)
	}
	if info.Size() != file.Size {
		return &SizeError{RelPath: file.RelPath, Expected: file.Size, Actual: info.Size()}
	}
	sum, err := hashFile(ctx, path)
	if err != nil {
		return err
	}
	actual := hex.EncodeToString(sum)
	if !strings.EqualFold(actual, file.SHA256) {
		return &HashError{RelPath: file.RelPath, Expected: strings.ToLower(file.SHA256), Actual: actual}
	}
	return nil
}

// Commit 把校验通过的 `.part` 原子改名到最终位置。
func (c *Client) Commit(root string, file File) error {
	if err := ValidateFile(file); err != nil {
		return err
	}
	dest, err := resolvePath(root, file.RelPath)
	if err != nil {
		return err
	}
	part := dest + ".part"
	if _, statErr := os.Stat(part); errors.Is(statErr, fs.ErrNotExist) {
		// 已复用落位文件：确认目标存在即可。
		if _, destErr := os.Stat(dest); destErr != nil {
			return fmt.Errorf("downloader: commit %s: %w", file.RelPath, destErr)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("downloader: create directory for %s: %w", file.RelPath, err)
	}
	if err := removeIfExists(dest); err != nil {
		return fmt.Errorf("downloader: replace %s: %w", file.RelPath, err)
	}
	if err := os.Rename(part, dest); err != nil {
		return fmt.Errorf("downloader: commit %s: %w", file.RelPath, err)
	}
	return nil
}

// Cleanup 删除 root 下所有 `.part` 临时文件；目录不存在视为已清理。
func (c *Client) Cleanup(root string) error {
	if root == "" {
		return nil
	}
	var firstErr error
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".part") {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) && firstErr == nil {
			firstErr = removeErr
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return firstErr
}

func resolvePath(root, relPath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relPath))
	// 拒绝 rooted path：POSIX 上 /x 是绝对路径，Windows 上 \x 是 rooted path
	// （filepath.IsAbs 为 false）。release catalog 的 rel_path 始终是安装目录内
	// 的相对路径，任何前导分隔符都不能被当作相对路径接受。
	if clean == "." || clean == "" || filepath.IsAbs(clean) ||
		strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, `\`) ||
		clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: rel_path %q escapes the install directory", ErrInvalidFile, relPath)
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve install root: %v", ErrInvalidFile, err)
	}
	target := filepath.Join(base, clean)
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: rel_path %q escapes the install directory", ErrInvalidFile, relPath)
	}
	return target, nil
}

func (c *Client) reusable(ctx context.Context, dest string, file File) (bool, error) {
	info, err := os.Stat(dest)
	if err != nil {
		return false, err
	}
	if info.Size() != file.Size {
		return false, nil
	}
	sum, err := hashFile(ctx, dest)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(sum), file.SHA256), nil
}

func hashFile(ctx context.Context, path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	buf := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return h.Sum(nil), nil
}

func contentRangeStart(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	rest, ok := strings.CutPrefix(value, "bytes ")
	if !ok {
		return 0, false
	}
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(rest[:dash], 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// progressGate 对高频回调做节流；到达总字节时总是报告。
type progressGate struct {
	interval time.Duration
	fn       func(written, total int64)
	last     time.Time
}

func (g *progressGate) report(written, total int64) {
	if g.fn == nil {
		return
	}
	now := time.Now()
	if now.Sub(g.last) < g.interval && written < total {
		return
	}
	g.last = now
	g.fn(written, total)
}
