package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/downloader"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/localapi"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/updateguard"
)

const testToken = "{{SECRET_qn2fwsph}}"

type testEnv struct {
	t       *testing.T
	store   *store.Store
	bus     *events.Bus
	svc     *service.Service
	local   *localapi.Manager
	updates *updateguard.Guard
	server  *httptest.Server
	client  *http.Client
	token   string
	dataDir string
	logger  *logging.Logger
}

type envConfig struct {
	releases     service.ReleaseCatalog
	downloader   service.Downloader
	capabilities service.CapabilityProvider
	devices      service.DeviceProvider
	environment  service.EnvironmentProvider
	dataDir      string

	// mediaProber 注入导入/重定位的媒体探测（nil 时仍可登记，Meta 为空）。
	mediaProber service.MediaProber
	// ownership 注入更新准备的执行/安装拥有者视图（默认完全空闲）。
	ownership updateguard.Ownership
	// budget 是更新准备控制面等待预算（默认 2s）。
	budget time.Duration

	// statfs 注入假的分区容量探测；nil 时使用真实 system.Statfs。
	statfs func(path string) (system.DiskSpace, error)
}

// fakeUpdateOwnership 是 API 层可控的更新准备拥有者替身；不冒充真实执行器。
type fakeUpdateOwnership struct {
	mu             sync.Mutex
	inMemoryActive bool
	executorBlock  string
	settle         func(context.Context) error
	settleCalls    int
}

func (o *fakeUpdateOwnership) InMemoryActive() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.inMemoryActive
}

func (o *fakeUpdateOwnership) ExecutorBlock() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.executorBlock
}

func (o *fakeUpdateOwnership) Settle(ctx context.Context) error {
	o.mu.Lock()
	o.settleCalls++
	fn := o.settle
	o.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (o *fakeUpdateOwnership) SettleCalls() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.settleCalls
}

func defaultReleases() service.StaticReleases {
	slots := []domain.CapabilitySlotKey{
		domain.CapabilitySlotASR, domain.CapabilitySlotTranslate, domain.CapabilitySlotTTS,
		domain.CapabilitySlotVoiceClone, domain.CapabilitySlotSubtitleErase,
		domain.CapabilitySlotSeparation, domain.CapabilitySlotOCR, domain.CapabilitySlotFFmpeg,
	}
	releases := make(map[domain.CapabilitySlotKey]service.ReleasePlan, len(slots))
	for _, slot := range slots {
		releases[slot] = service.ReleasePlan{
			Release: domain.ReleaseRef{
				ReleaseID: "rel-" + string(slot),
				Version:   "1.0.0",
				Digest:    "sha256-" + string(slot),
			},
			Files: []downloader.File{{
				RelPath: "model.bin",
				URL:     "https://releases.invalid/" + string(slot) + "/model.bin",
				SHA256:  strings.Repeat("0", 64),
				Size:    1024,
			}},
			DownloadBytes:    1024,
			CacheReuseBytes:  0,
			InstallBytes:     2048,
			PeakStagingBytes: 3072,
			Permissions:      []string{"filesystem_write"},
			License: domain.LicenseSummary{
				LicenseID:   "Apache-2.0",
				LicenseName: "Apache License 2.0",
			},
		}
	}
	return service.StaticReleases{Releases: releases}
}

// fakeDownloader 便于按测试注入下载行为；未设置的步骤视为成功（不触碰真实文件）。
type fakeDownloader struct {
	fetch   func(ctx context.Context, root string, file downloader.File, onProgress func(written, total int64)) error
	verify  func(ctx context.Context, root string, file downloader.File) error
	commit  func(root string, file downloader.File) error
	cleanup func(root string) error
}

func (f fakeDownloader) Fetch(ctx context.Context, root string, file downloader.File, onProgress func(written, total int64)) error {
	if f.fetch != nil {
		return f.fetch(ctx, root, file, onProgress)
	}
	return nil
}

func (f fakeDownloader) Verify(ctx context.Context, root string, file downloader.File) error {
	if f.verify != nil {
		return f.verify(ctx, root, file)
	}
	return nil
}

func (f fakeDownloader) Commit(root string, file downloader.File) error {
	if f.commit != nil {
		return f.commit(root, file)
	}
	return nil
}

func (f fakeDownloader) Cleanup(root string) error {
	if f.cleanup != nil {
		return f.cleanup(root)
	}
	return nil
}

func newTestEnv(t *testing.T, mutate ...func(*envConfig)) *testEnv {
	t.Helper()
	cfg := envConfig{
		releases:     defaultReleases(),
		downloader:   fakeDownloader{},
		capabilities: service.StaticCapabilities{},
		devices:      service.SystemDevices{},
		dataDir:      t.TempDir(),
		budget:       2 * time.Second,
	}
	for _, fn := range mutate {
		fn(&cfg)
	}
	if cfg.ownership == nil {
		cfg.ownership = &fakeUpdateOwnership{}
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(cfg.dataDir, "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	bus := events.NewBus(st, logger)
	svc := service.New(service.Options{
		Store:        st,
		Bus:          bus,
		Log:          logger,
		WorkspaceID:  "ws-test",
		InstanceID:   "instance-test",
		ServerEpoch:  7,
		AppVersion:   "0.0.0-test",
		DataDir:      cfg.dataDir,
		Capabilities: cfg.capabilities,
		Devices:      cfg.devices,
		Environment:  cfg.environment,
		Releases:     cfg.releases,
		Downloader:   cfg.downloader,
		MediaProber:  cfg.mediaProber,
		HeavySlots:   1,
		Statfs:       cfg.statfs,
	})
	if err := svc.EnsureStorageDirs(); err != nil {
		t.Fatalf("ensure storage dirs: %v", err)
	}
	manager, err := localapi.NewManager(st, logger, localapi.Options{})
	if err != nil {
		t.Fatalf("local api manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	updates := updateguard.New(updateguard.Options{
		Store:       st,
		Ownership:   cfg.ownership,
		WorkspaceID: "ws-test",
		InstanceID:  "instance-test",
		ServerEpoch: 7,
		Budget:      cfg.budget,
	})
	handler := New(Config{Service: svc, LocalAPI: manager, Token: testToken, Log: logger, Updates: updates}).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &testEnv{
		t:       t,
		store:   st,
		bus:     bus,
		svc:     svc,
		local:   manager,
		updates: updates,
		server:  server,
		client:  &http.Client{Timeout: 10 * time.Second},
		token:   testToken,
		dataDir: cfg.dataDir,
		logger:  logger,
	}
}

type httpResult struct {
	Status int
	Body   []byte
	Header http.Header
}

func (r httpResult) code(t *testing.T) string {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal(r.Body, &envelope); err != nil {
		t.Fatalf("decode error body: %v (body=%s)", err, r.Body)
	}
	return string(envelope.Error.Code)
}

func (r httpResult) requestID(t *testing.T) string {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal(r.Body, &envelope); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return envelope.RequestID
}

// request 发起一次带实例令牌的请求；非 GET 默认带随机幂等键。
func (e *testEnv) request(method, path string, body any, opts ...func(*http.Request)) httpResult {
	e.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(value)
	default:
		data, err := json.Marshal(value)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	req.Header.Set(tokenHeader, e.token)
	req.Header.Set("request_id", "test-"+store.NewID(""))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Idempotency-Key", store.NewID("idem"))
	}
	for _, opt := range opts {
		opt(req)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read response: %v", err)
	}
	return httpResult{Status: resp.StatusCode, Body: payload, Header: resp.Header}
}

func withoutToken() func(*http.Request) {
	return func(r *http.Request) { r.Header.Del(tokenHeader) }
}

func withToken(value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(tokenHeader, value) }
}

func withHeader(key, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

func withIdemKey(key string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Idempotency-Key", key) }
}

func withoutIdem() func(*http.Request) {
	return func(r *http.Request) { r.Header.Del("Idempotency-Key") }
}

func (e *testEnv) expect(result httpResult, want int) httpResult {
	e.t.Helper()
	if result.Status != want {
		e.t.Fatalf("status = %d, want %d (body=%s)", result.Status, want, result.Body)
	}
	return result
}

func decodeInto[T any](t *testing.T, result httpResult) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(result.Body, &value); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, result.Body)
	}
	return value
}

// seedProject 通过 API 创建项目并返回 ID。
func (e *testEnv) seedProject(name string) domain.ID {
	e.t.Helper()
	res := e.expect(e.request(http.MethodPost, "/api/v1/projects", map[string]any{"name": name}), http.StatusCreated)
	project := decodeInto[domain.Project](e.t, res)
	return project.ID
}

// seedRun 通过 API 创建八节点 Run 并返回。
func (e *testEnv) seedRun(projectID string, languages ...string) domain.WorkflowRun {
	e.t.Helper()
	if len(languages) == 0 {
		languages = []string{"zh"}
	}
	res := e.expect(e.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":       projectID,
		"name":             "译制计划",
		"source_language":  "en",
		"target_languages": languages,
		"extract":          map[string]any{"mode": "asr"},
	}), http.StatusCreated)
	return decodeInto[domain.WorkflowRun](e.t, res)
}

// seedTrack 直接写入一条译文轨与初始 revision（fixture）。
func (e *testEnv) seedTrack(projectID, language string, segments []domain.Segment) domain.SubtitleTrack {
	e.t.Helper()
	return e.seedTrackType(projectID, language, domain.TrackTypeTranslation, segments)
}

// seedTrackType 直接写入指定类型/语言的字幕轨与初始 revision（fixture）。
func (e *testEnv) seedTrackType(projectID, language string, typ domain.TrackType, segments []domain.Segment) domain.SubtitleTrack {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	revisionID := store.NewID("srev")
	track := domain.SubtitleTrack{
		ID:                store.NewID("track"),
		ProjectID:         projectID,
		Language:          language,
		Type:              typ,
		CurrentRevisionID: revisionID,
		CreatedAt:         now,
		UpdatedAt:         now,
		Revisions: []domain.SubtitleRevision{{
			ID:         revisionID,
			TrackID:    "",
			AuthorType: domain.AuthorTypeSystem,
			Segments:   segments,
			CreatedAt:  now,
		}},
	}
	if err := e.store.CreateSubtitleTrack(ctx, track); err != nil {
		e.t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	built, err := e.store.GetSubtitleTrack(ctx, track.ID)
	if err != nil {
		e.t.Fatalf("GetSubtitleTrack: %v", err)
	}
	return built
}

// seedApprovedDelivery 登记并放行绑定 track 当前 revision 的 delivery 门槛。
//
// 与 Core 的交付导出门禁同口径：摘要由 domain.RevisionBindingDigest 计算，
// 放行后门槛绑定该 revision 的当前内容。
func (e *testEnv) seedApprovedDelivery(runID domain.ID, track domain.SubtitleTrack) domain.ReviewGate {
	e.t.Helper()
	ctx := context.Background()
	var revision *domain.SubtitleRevision
	for i := range track.Revisions {
		if track.Revisions[i].ID == track.CurrentRevisionID {
			revision = &track.Revisions[i]
			break
		}
	}
	if revision == nil {
		e.t.Fatalf("track %s has no current revision", track.ID)
	}
	digest, err := domain.RevisionBindingDigest(*revision)
	if err != nil {
		e.t.Fatalf("RevisionBindingDigest: %v", err)
	}
	bind := domain.DeliveryBind(digest)
	gate := domain.ReviewGate{
		ID:             store.NewID("review"),
		RunID:          runID,
		Type:           domain.ReviewTypeDelivery,
		RevisionDigest: bind,
		Status:         domain.ReviewStatusPending,
	}
	if err := e.store.CreateReviewGate(ctx, gate); err != nil {
		e.t.Fatalf("CreateReviewGate: %v", err)
	}
	approved, err := e.store.ApproveReview(ctx, gate.ID, bind, domain.ReviewTypeDelivery, time.Now().UTC())
	if err != nil {
		e.t.Fatalf("ApproveReview: %v", err)
	}
	return approved
}

// seedApprovedMix 创建冻结在 track 当前 revision 的成功 08 任务，并放行其交付门槛。
// 视频/音频导出必须绑定 08 产物；字幕 revision 级门槛不能替代。
func (e *testEnv) seedApprovedMix(runID domain.ID, track domain.SubtitleTrack, language string) domain.Task {
	e.t.Helper()
	ctx := context.Background()
	var revision *domain.SubtitleRevision
	for i := range track.Revisions {
		if track.Revisions[i].ID == track.CurrentRevisionID {
			revision = &track.Revisions[i]
			break
		}
	}
	if revision == nil {
		e.t.Fatalf("track %s has no current revision", track.ID)
	}
	now := time.Now().UTC()
	task := domain.Task{
		ID: store.NewID("task"), ProjectID: track.ProjectID, RunID: runID,
		Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix, Language: language,
		Status: domain.TaskStatusSucceeded, ArtifactIDs: []domain.ID{"asset-mix"},
		InputDigest: "mix-input-" + string(revision.ID), CreatedAt: now, UpdatedAt: now,
		Params: map[string]any{
			"language": language,
			"_core": map[string]any{
				"subtitle_revision_id": string(revision.ID),
			},
		},
	}
	if err := e.store.CreateTask(ctx, task); err != nil {
		e.t.Fatalf("CreateTask: %v", err)
	}
	digest, err := domain.NodeReviewDigest(task.RunID, task.NodeKey, task.Language,
		task.ID, task.ArtifactIDs, task.InputDigest)
	if err != nil {
		e.t.Fatalf("NodeReviewDigest: %v", err)
	}
	gate := domain.ReviewGate{
		ID: store.NewID("review"), RunID: runID, Type: domain.ReviewTypeDelivery,
		Language: language, RevisionDigest: digest, Status: domain.ReviewStatusPending,
	}
	if err := e.store.CreateReviewGate(ctx, gate); err != nil {
		e.t.Fatalf("CreateReviewGate: %v", err)
	}
	if _, err := e.store.ApproveReview(ctx, gate.ID, digest, domain.ReviewTypeDelivery, now); err != nil {
		e.t.Fatalf("ApproveReview: %v", err)
	}
	return task
}

// seedSubtitleAsset 写一个临时 SRT 文件并登记资产/位置，供导入端点测试。
func (e *testEnv) seedSubtitleAsset(projectID, content string) domain.Asset {
	e.t.Helper()
	path := filepath.Join(e.dataDir, store.NewID("srt")+".srt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatalf("write subtitle: %v", err)
	}
	return e.registerSubtitleAsset(projectID, path, int64(len(content)))
}

// seedSubtitleAssetSize 登记指定大小的稀疏 SRT 文件；用于文件大小上限测试，
// 内容不参与解析（端点先按大小拒绝）。
func (e *testEnv) seedSubtitleAssetSize(projectID string, size int64) domain.Asset {
	e.t.Helper()
	path := filepath.Join(e.dataDir, store.NewID("srt")+".srt")
	file, err := os.Create(path)
	if err != nil {
		e.t.Fatalf("create subtitle: %v", err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		e.t.Fatalf("truncate subtitle: %v", err)
	}
	if err := file.Close(); err != nil {
		e.t.Fatalf("close subtitle: %v", err)
	}
	return e.registerSubtitleAsset(projectID, path, size)
}

// registerSubtitleAsset 登记 subtitle 资产与其受控位置。
func (e *testEnv) registerSubtitleAsset(projectID, path string, sizeBytes int64) domain.Asset {
	e.t.Helper()
	now := time.Now().UTC()
	asset := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  projectID,
		Type:       domain.AssetTypeSubtitle,
		Name:       "source.srt",
		SizeBytes:  sizeBytes,
		Status:     domain.AssetStatusReady,
		ImportedAt: now,
	}
	if err := e.store.CreateAsset(context.Background(), asset); err != nil {
		e.t.Fatalf("CreateAsset: %v", err)
	}
	if err := e.store.AddAssetLocation(context.Background(), domain.AssetLocation{
		ID:        store.NewID("loc"),
		AssetID:   asset.ID,
		Path:      path,
		Source:    domain.LocationSourceExternalReference,
		Owner:     domain.LocationOwnerUser,
		Available: true,
		CreatedAt: now,
	}); err != nil {
		e.t.Fatalf("AddAssetLocation: %v", err)
	}
	return asset
}

// seedAudioAsset 登记一个可被候选引用的音频资产。
func (e *testEnv) seedAudioAsset(projectID string) domain.Asset {
	e.t.Helper()
	asset := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  projectID,
		Type:       domain.AssetTypeAudio,
		Name:       "candidate.wav",
		SizeBytes:  16,
		Status:     domain.AssetStatusReady,
		ImportedAt: time.Now().UTC(),
	}
	if err := e.store.CreateAsset(context.Background(), asset); err != nil {
		e.t.Fatalf("CreateAsset: %v", err)
	}
	return asset
}

// seedAudioTrackAsset 登记一个已分离的人声/环境音轨资产（工具输入校验用）。
func (e *testEnv) seedAudioTrackAsset(projectID string) domain.Asset {
	e.t.Helper()
	asset := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  projectID,
		Type:       domain.AssetTypeAudioTrack,
		Name:       "voice.wav",
		SizeBytes:  16,
		Status:     domain.AssetStatusReady,
		ImportedAt: time.Now().UTC(),
	}
	if err := e.store.CreateAsset(context.Background(), asset); err != nil {
		e.t.Fatalf("CreateAsset: %v", err)
	}
	return asset
}

// seedVideoAsset 登记一个指定大小的视频资产（空间准入测试用）。
func (e *testEnv) seedVideoAsset(projectID string, sizeBytes int64) domain.Asset {
	e.t.Helper()
	asset := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  projectID,
		Type:       domain.AssetTypeVideo,
		Name:       "source.mp4",
		SizeBytes:  sizeBytes,
		Status:     domain.AssetStatusReady,
		ImportedAt: time.Now().UTC(),
	}
	if err := e.store.CreateAsset(context.Background(), asset); err != nil {
		e.t.Fatalf("CreateAsset: %v", err)
	}
	return asset
}

// freezeCandidateFixture explicitly supplies the release locks and reference
// media required by model work. Legacy seedRun alone remains an unlocked Run.
func (e *testEnv) freezeCandidateFixture(run domain.WorkflowRun) {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	path := filepath.Join(e.t.TempDir(), "reference.wav")
	if err := os.WriteFile(path, []byte("fixture-reference-audio"), 0o600); err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("fixture-reference-audio"))
	asset := domain.Asset{
		ID: store.NewID("asset"), ProjectID: run.ProjectID, Type: domain.AssetTypeAudioTrack,
		Name: "reference.wav", Status: domain.AssetStatusReady, SizeBytes: 23,
		SHA256: hex.EncodeToString(sum[:]),
		Meta:   &domain.MediaMeta{Role: "vocals", AudioTracks: 1}, ImportedAt: now,
	}
	if err := e.store.CreateAsset(ctx, asset); err != nil {
		e.t.Fatal(err)
	}
	if err := e.store.AddAssetLocation(ctx, domain.AssetLocation{
		ID: store.NewID("loc"), AssetID: asset.ID, Path: path, Available: true,
		Source: domain.LocationSourceExternalReference, Owner: domain.LocationOwnerUser, CreatedAt: now,
	}); err != nil {
		e.t.Fatal(err)
	}
	configs := map[domain.CapabilitySlotKey]domain.ExecutionConfig{}
	for slot, release := range defaultReleases().Releases {
		configs[slot] = domain.ExecutionConfig{ModelLock: release.Release, DeviceID: "cpu", Params: map[string]any{}}
	}
	encoded, err := json.Marshal(configs)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.store.DB().ExecContext(ctx,
		`UPDATE workflow_runs SET execution_config_json = ?, reuse_audio_track_id = ? WHERE id = ?`,
		string(encoded), asset.ID, run.ID,
	); err != nil {
		e.t.Fatal(err)
	}
}
