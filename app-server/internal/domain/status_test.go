package domain

import (
	"errors"
	"testing"
	"time"
)

func allRunStatuses() []RunStatus {
	return []RunStatus{
		RunStatusDraft, RunStatusQueued, RunStatusRunning, RunStatusPauseRequested,
		RunStatusPaused, RunStatusWaitingReview, RunStatusPartial, RunStatusSucceeded,
		RunStatusFailed, RunStatusCanceled,
	}
}

func allTaskStatuses() []TaskStatus {
	return []TaskStatus{
		TaskStatusQueued, TaskStatusWaitingResource, TaskStatusRunning,
		TaskStatusPauseRequested, TaskStatusPaused, TaskStatusSucceeded, TaskStatusFailed,
		TaskStatusCancelRequested, TaskStatusCanceled, TaskStatusInterrupted,
	}
}

func allNodeStatuses() []NodeStatus {
	return []NodeStatus{
		NodeStatusPending, NodeStatusRunning, NodeStatusWaitingReview,
		NodeStatusDone, NodeStatusSkipped, NodeStatusReused, NodeStatusFailed,
	}
}

func allStepStatuses() []StepStatus {
	return []StepStatus{
		StepStatusPending, StepStatusRunning, StepStatusCommitting,
		StepStatusSucceeded, StepStatusFailed, StepStatusCanceled, StepStatusInterrupted,
	}
}

func allInstallationPhases() []InstallationPhase {
	return []InstallationPhase{
		InstallationPhasePlanning, InstallationPhaseDownloading, InstallationPhaseVerifying,
		InstallationPhaseInstalling, InstallationPhaseDone, InstallationPhaseFailed,
		InstallationPhaseCanceled,
	}
}

func statusSet[T ~string](statuses ...T) map[string]bool {
	set := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		set[string(s)] = true
	}
	return set
}

func checkEnum[T ~string](t *testing.T, name string, values []T, valid func(T) bool) {
	t.Helper()
	for _, v := range values {
		if !valid(v) {
			t.Errorf("%s: declared value %q is not valid", name, v)
		}
	}
	if valid(T("__invalid__")) {
		t.Errorf("%s: unknown value accepted", name)
	}
}

func TestEnumValidation(t *testing.T) {
	checkEnum(t, "RunStatus", allRunStatuses(), func(v RunStatus) bool { return v.Valid() })
	checkEnum(t, "TaskStatus", allTaskStatuses(), func(v TaskStatus) bool { return v.Valid() })
	checkEnum(t, "NodeStatus", allNodeStatuses(), func(v NodeStatus) bool { return v.Valid() })
	checkEnum(t, "StepStatus", allStepStatuses(), func(v StepStatus) bool { return v.Valid() })
	checkEnum(t, "InstallationPhase", allInstallationPhases(), func(v InstallationPhase) bool { return v.Valid() })
	checkEnum(t, "PipelineNodeKey", []PipelineNodeKey{
		NodeKeyImport, NodeKeyAnalyze, NodeKeySeparate, NodeKeyExtract,
		NodeKeyErase, NodeKeyTranslate, NodeKeyClone, NodeKeyMix,
	}, func(v PipelineNodeKey) bool { return v.Valid() })
	checkEnum(t, "ReviewType", []ReviewType{ReviewTypeTranslation, ReviewTypeDelivery},
		func(v ReviewType) bool { return v.Valid() })
	checkEnum(t, "ReviewStatus", []ReviewStatus{ReviewStatusPending, ReviewStatusApproved},
		func(v ReviewStatus) bool { return v.Valid() })
	checkEnum(t, "TaskKind", []TaskKind{TaskKindNode, TaskKindSegment, TaskKindExport},
		func(v TaskKind) bool { return v.Valid() })
	checkEnum(t, "AssetType", []AssetType{
		AssetTypeVideo, AssetTypeAudio, AssetTypeSubtitle, AssetTypeAudioTrack,
		AssetTypeImage, AssetTypeArchive, AssetTypeOther,
	}, func(v AssetType) bool { return v.Valid() })
	checkEnum(t, "AssetStatus", []AssetStatus{
		AssetStatusImporting, AssetStatusReady, AssetStatusMissing, AssetStatusCorrupt,
	}, func(v AssetStatus) bool { return v.Valid() })
	checkEnum(t, "TrackType", []TrackType{TrackTypeTranscription, TrackTypeTranslation, TrackTypeCorrection},
		func(v TrackType) bool { return v.Valid() })
	checkEnum(t, "AuthorType", []AuthorType{AuthorTypeSystem, AuthorTypeUser},
		func(v AuthorType) bool { return v.Valid() })
	checkEnum(t, "CapabilitySlotKey", []CapabilitySlotKey{
		CapabilitySlotASR, CapabilitySlotTranslate, CapabilitySlotTTS, CapabilitySlotVoiceClone,
		CapabilitySlotSubtitleErase, CapabilitySlotSeparation, CapabilitySlotOCR, CapabilitySlotFFmpeg,
	}, func(v CapabilitySlotKey) bool { return v.Valid() })
	checkEnum(t, "ModelSlotKey", []ModelSlotKey{
		ModelSlotASR, ModelSlotTranslate, ModelSlotTTS, ModelSlotVoiceClone,
		ModelSlotSubtitleErase, ModelSlotSeparation,
	}, func(v ModelSlotKey) bool { return v.Valid() })
	checkEnum(t, "SlotState", []SlotState{
		SlotStateUnknown, SlotStateUnsupported, SlotStateUnverified, SlotStateNeedsSetup,
		SlotStateInstalling, SlotStateVerifying, SlotStateReady, SlotStateNeedsAction,
		SlotStateRestartRequired, SlotStateFailed, SlotStateDegraded,
	}, func(v SlotState) bool { return v.Valid() })
	checkEnum(t, "PreflightSeverity", []PreflightSeverity{PreflightPass, PreflightWarning, PreflightBlocker},
		func(v PreflightSeverity) bool { return v.Valid() })
	checkEnum(t, "ParamFieldType", []ParamFieldType{
		ParamFieldTypeString, ParamFieldTypeNumber, ParamFieldTypeBoolean, ParamFieldTypeEnum,
	}, func(v ParamFieldType) bool { return v.Valid() })
	checkEnum(t, "ExtractMode", []ExtractMode{ExtractModeASR, ExtractModeOCR, ExtractModeASROCR},
		func(v ExtractMode) bool { return v.Valid() })
	checkEnum(t, "EraseMode", []EraseMode{EraseModeDialogOnly, EraseModeDialogAndArt},
		func(v EraseMode) bool { return v.Valid() })
	checkEnum(t, "OCRLanguage", []OCRLanguage{
		OCRLanguageChinese, OCRLanguageChineseTraditional, OCRLanguageEnglish, OCRLanguageJapanese,
	}, func(v OCRLanguage) bool { return v.Valid() })
	checkEnum(t, "SubtitleAlign", []SubtitleAlign{
		SubtitleAlignLeft, SubtitleAlignCenter, SubtitleAlignRight,
	}, func(v SubtitleAlign) bool { return v.Valid() })
	checkEnum(t, "CommitStatus", []CommitStatus{CommitStatusPending, CommitStatusCommitted, CommitStatusFailed},
		func(v CommitStatus) bool { return v.Valid() })
	checkEnum(t, "ExportKind", []ExportKind{ExportKindVideo, ExportKindAudio, ExportKindSubtitle},
		func(v ExportKind) bool { return v.Valid() })
	checkEnum(t, "ExportFormat", []ExportFormat{
		ExportFormatMP4H264, ExportFormatSRT, ExportFormatASS, ExportFormatWAV, ExportFormatMP3,
	}, func(v ExportFormat) bool { return v.Valid() })
	checkEnum(t, "ErrorCode", []ErrorCode{
		ErrorCodeInvalidRequest, ErrorCodeLocalAuthRequired, ErrorCodeLocalAccessDenied,
		ErrorCodeRevisionConflict, ErrorCodePreflightStale, ErrorCodeIdempotencyConflict,
		ErrorCodeModelNotInstalled, ErrorCodeRuntimeIncompatible, ErrorCodeCapabilityUnverified,
		ErrorCodeInsufficientDisk, ErrorCodeCoreNotReady, ErrorCodePersistenceUnavailable,
		ErrorCodeWorkerExited, ErrorCodeResourceExhausted, ErrorCodeEventCursorExpired, ErrorCodeNotFound,
		ErrorCodeReviewRequired,
	}, func(v ErrorCode) bool { return v.Valid() })
}

func TestRunTransitionTable(t *testing.T) {
	allowed := map[RunStatus]map[string]bool{
		RunStatusDraft:          statusSet(RunStatusQueued, RunStatusCanceled),
		RunStatusQueued:         statusSet(RunStatusRunning, RunStatusPauseRequested, RunStatusFailed, RunStatusCanceled),
		RunStatusRunning:        statusSet(RunStatusPauseRequested, RunStatusWaitingReview, RunStatusSucceeded, RunStatusPartial, RunStatusFailed, RunStatusCanceled),
		RunStatusPauseRequested: statusSet(RunStatusPaused, RunStatusSucceeded, RunStatusPartial, RunStatusFailed, RunStatusCanceled),
		RunStatusPaused:         statusSet(RunStatusQueued, RunStatusFailed, RunStatusCanceled),
		RunStatusWaitingReview:  statusSet(RunStatusRunning, RunStatusPauseRequested, RunStatusSucceeded, RunStatusPartial, RunStatusFailed, RunStatusCanceled),
		RunStatusPartial:        {},
		RunStatusSucceeded:      {},
		RunStatusFailed:         {},
		RunStatusCanceled:       {},
	}

	for _, from := range allRunStatuses() {
		for _, to := range allRunStatuses() {
			want := allowed[from][string(to)]
			if got := CanTransitionRun(from, to); got != want {
				t.Errorf("CanTransitionRun(%s, %s) = %v, want %v", from, to, got, want)
			}
			_, err := TransitionRun(from, to)
			if want && from != to && err != nil {
				t.Errorf("TransitionRun(%s, %s) error = %v, want nil", from, to, err)
			}
			if (!want || from == to) && !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("TransitionRun(%s, %s) error = %v, want ErrInvalidTransition", from, to, err)
			}
		}
	}
}

func TestRunTerminalAndGates(t *testing.T) {
	terminal := statusSet(RunStatusPartial, RunStatusSucceeded, RunStatusFailed, RunStatusCanceled)
	dispatch := statusSet(RunStatusQueued, RunStatusRunning, RunStatusWaitingReview)

	for _, status := range allRunStatuses() {
		if got := status.Terminal(); got != terminal[string(status)] {
			t.Errorf("%s.Terminal() = %v, want %v", status, got, terminal[string(status)])
		}
		if got := RunAllowsDispatch(status); got != dispatch[string(status)] {
			t.Errorf("RunAllowsDispatch(%s) = %v, want %v", status, got, dispatch[string(status)])
		}
		wantReview := !terminal[string(status)]
		if got := RunAllowsReviewApproval(status); got != wantReview {
			t.Errorf("RunAllowsReviewApproval(%s) = %v, want %v", status, got, wantReview)
		}
		if got := RunIsPaused(status); got != (status == RunStatusPauseRequested || status == RunStatusPaused) {
			t.Errorf("RunIsPaused(%s) = %v", status, got)
		}
	}
}

func TestTaskTransitionTable(t *testing.T) {
	allowed := map[TaskStatus]map[string]bool{
		TaskStatusQueued:          statusSet(TaskStatusWaitingResource, TaskStatusRunning, TaskStatusPauseRequested, TaskStatusCanceled, TaskStatusFailed),
		TaskStatusWaitingResource: statusSet(TaskStatusRunning, TaskStatusPauseRequested, TaskStatusCanceled, TaskStatusFailed),
		TaskStatusRunning:         statusSet(TaskStatusSucceeded, TaskStatusFailed, TaskStatusCancelRequested, TaskStatusInterrupted, TaskStatusPauseRequested),
		TaskStatusCancelRequested: statusSet(TaskStatusCanceled, TaskStatusSucceeded, TaskStatusInterrupted),
		TaskStatusPauseRequested:  statusSet(TaskStatusPaused, TaskStatusSucceeded, TaskStatusFailed, TaskStatusCanceled, TaskStatusInterrupted),
		TaskStatusPaused:          statusSet(TaskStatusQueued, TaskStatusRunning, TaskStatusCanceled, TaskStatusFailed),
		TaskStatusFailed:          statusSet(TaskStatusQueued),
		TaskStatusInterrupted:     statusSet(TaskStatusQueued),
		TaskStatusSucceeded:       {},
		TaskStatusCanceled:        {},
	}

	for _, from := range allTaskStatuses() {
		for _, to := range allTaskStatuses() {
			want := allowed[from][string(to)]
			if got := CanTransitionTask(from, to); got != want {
				t.Errorf("CanTransitionTask(%s, %s) = %v, want %v", from, to, got, want)
			}
			_, err := TransitionTask(from, to)
			if (!want || from == to) && !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("TransitionTask(%s, %s) error = %v, want ErrInvalidTransition", from, to, err)
			}
		}
	}
}

func TestTaskRetryRules(t *testing.T) {
	for _, status := range allTaskStatuses() {
		want := status == TaskStatusFailed || status == TaskStatusInterrupted
		if got := TaskRetryable(status); got != want {
			t.Errorf("TaskRetryable(%s) = %v, want %v", status, got, want)
		}
	}
	// SUCCEEDED / CANCELED 是终止状态，不原地重试。
	for _, status := range []TaskStatus{TaskStatusSucceeded, TaskStatusCanceled} {
		if !status.Terminal() {
			t.Errorf("%s.Terminal() = false, want true", status)
		}
		if _, err := PrepareTaskRetry(Task{Status: status, InputDigest: "a"}, "a"); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("PrepareTaskRetry(%s) error = %v, want ErrInvalidTransition", status, err)
		}
	}
	// FAILED 允许重试回到 QUEUED。
	retried, err := PrepareTaskRetry(Task{Status: TaskStatusFailed, Progress: 80, InputDigest: "abc"}, "abc")
	if err != nil {
		t.Fatalf("PrepareTaskRetry(FAILED) error = %v", err)
	}
	if retried.Status != TaskStatusQueued || retried.Progress != 0 {
		t.Errorf("retried task = %s/%d, want QUEUED/0", retried.Status, retried.Progress)
	}
	// 输入变化返回冲突。
	if _, err := PrepareTaskRetry(Task{Status: TaskStatusFailed, InputDigest: "abc"}, "def"); !errors.Is(err, ErrRevisionConflict) {
		t.Errorf("PrepareTaskRetry changed input error = %v, want ErrRevisionConflict", err)
	}
}

func TestNextAttemptNo(t *testing.T) {
	cases := []struct {
		name     string
		attempts []Attempt
		want     int
	}{
		{"none", nil, 1},
		{"single", []Attempt{{AttemptNo: 1}}, 2},
		{"gaps never reuse", []Attempt{{AttemptNo: 1}, {AttemptNo: 5}, {AttemptNo: 2}}, 6},
		{"out of order", []Attempt{{AttemptNo: 3}, {AttemptNo: 1}}, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextAttemptNo(tc.attempts); got != tc.want {
				t.Errorf("NextAttemptNo() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNodeTransitionTable(t *testing.T) {
	allowed := map[NodeStatus]map[string]bool{
		NodeStatusPending:       statusSet(NodeStatusRunning, NodeStatusSkipped, NodeStatusReused, NodeStatusFailed),
		NodeStatusRunning:       statusSet(NodeStatusDone, NodeStatusFailed),
		NodeStatusWaitingReview: statusSet(NodeStatusDone, NodeStatusFailed),
		NodeStatusDone:          {},
		NodeStatusSkipped:       {},
		NodeStatusReused:        {},
		NodeStatusFailed:        statusSet(NodeStatusRunning),
	}

	for _, from := range allNodeStatuses() {
		for _, to := range allNodeStatuses() {
			want := allowed[from][string(to)]
			// 用非审核节点（01）验证基础迁移表。
			if got := CanTransitionNode(from, to, NodeKeyImport); got != want {
				t.Errorf("CanTransitionNode(%s, %s, 01) = %v, want %v", from, to, got, want)
			}
		}
	}

	// 06/08 必须经 waiting_review 放行；其他节点不能停在人工审核。
	for _, key := range []PipelineNodeKey{NodeKeyTranslate, NodeKeyMix} {
		if !CanTransitionNode(NodeStatusRunning, NodeStatusWaitingReview, key) {
			t.Errorf("CanTransitionNode(running, waiting_review, %s) = false, want true", key)
		}
		if CanTransitionNode(NodeStatusRunning, NodeStatusDone, key) {
			t.Errorf("CanTransitionNode(running, done, %s) = true, want false before review", key)
		}
		if !CanTransitionNode(NodeStatusWaitingReview, NodeStatusDone, key) {
			t.Errorf("CanTransitionNode(waiting_review, done, %s) = false, want true", key)
		}
	}
	for _, key := range []PipelineNodeKey{NodeKeyImport, NodeKeyAnalyze, NodeKeySeparate, NodeKeyExtract, NodeKeyErase, NodeKeyClone} {
		if CanTransitionNode(NodeStatusRunning, NodeStatusWaitingReview, key) {
			t.Errorf("CanTransitionNode(running, waiting_review, %s) = true, want false", key)
		}
	}
}

func TestStepAndInstallationTransitions(t *testing.T) {
	stepAllowed := map[StepStatus]map[string]bool{
		StepStatusPending:     statusSet(StepStatusRunning, StepStatusFailed, StepStatusCanceled),
		StepStatusRunning:     statusSet(StepStatusCommitting, StepStatusSucceeded, StepStatusFailed, StepStatusCanceled, StepStatusInterrupted),
		StepStatusCommitting:  statusSet(StepStatusSucceeded, StepStatusFailed, StepStatusCanceled, StepStatusInterrupted),
		StepStatusSucceeded:   {},
		StepStatusFailed:      {},
		StepStatusCanceled:    {},
		StepStatusInterrupted: {},
	}
	for _, from := range allStepStatuses() {
		for _, to := range allStepStatuses() {
			want := stepAllowed[from][string(to)]
			if got := CanTransitionStep(from, to); got != want {
				t.Errorf("CanTransitionStep(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}

	installAllowed := map[InstallationPhase]map[string]bool{
		InstallationPhasePlanning:    statusSet(InstallationPhaseDownloading, InstallationPhaseFailed, InstallationPhaseCanceled),
		InstallationPhaseDownloading: statusSet(InstallationPhaseVerifying, InstallationPhaseFailed, InstallationPhaseCanceled),
		InstallationPhaseVerifying:   statusSet(InstallationPhaseInstalling, InstallationPhaseFailed, InstallationPhaseCanceled),
		InstallationPhaseInstalling:  statusSet(InstallationPhaseDone, InstallationPhaseFailed, InstallationPhaseCanceled),
		InstallationPhaseDone:        {},
		InstallationPhaseFailed:      {},
		InstallationPhaseCanceled:    {},
	}
	for _, from := range allInstallationPhases() {
		for _, to := range allInstallationPhases() {
			want := installAllowed[from][string(to)]
			if got := CanTransitionInstallationPhase(from, to); got != want {
				t.Errorf("CanTransitionInstallationPhase(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestApproveReviewGate(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 30, 0, 0, time.FixedZone("CST", 8*3600))
	base := ReviewGate{
		ID:             "review-1",
		RunID:          "run-1",
		Type:           ReviewTypeTranslation,
		Language:       "ja",
		RevisionDigest: "digest-a",
		Status:         ReviewStatusPending,
	}

	approved, err := ApproveReviewGate(base, "digest-a", ReviewTypeTranslation, now)
	if err != nil {
		t.Fatalf("ApproveReviewGate() error = %v", err)
	}
	if approved.Status != ReviewStatusApproved {
		t.Errorf("status = %s, want approved", approved.Status)
	}
	if approved.ConfirmedAt == nil || !approved.ConfirmedAt.Equal(now) || approved.ConfirmedAt.Location() != time.UTC {
		t.Errorf("confirmed_at = %v, want UTC %v", approved.ConfirmedAt, now)
	}

	if _, err := ApproveReviewGate(base, "digest-b", ReviewTypeTranslation, now); !errors.Is(err, ErrRevisionConflict) {
		t.Errorf("digest mismatch error = %v, want ErrRevisionConflict", err)
	}
	if _, err := ApproveReviewGate(base, "", ReviewTypeTranslation, now); !errors.Is(err, ErrRevisionConflict) {
		t.Errorf("empty digest error = %v, want ErrRevisionConflict", err)
	}
	if _, err := ApproveReviewGate(base, "digest-a", ReviewTypeDelivery, now); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("type mismatch error = %v, want ErrInvalidRequest", err)
	}
	if _, err := ApproveReviewGate(approved, "digest-a", ReviewTypeTranslation, now); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("already approved error = %v, want ErrInvalidTransition", err)
	}
}

func TestValidateSegmentInputs(t *testing.T) {
	valid := []SegmentInput{
		{ID: "a", StartMs: 0, EndMs: 1000, Text: "hello"},
		{ID: "b", StartMs: 1000, EndMs: 2500, Text: "world"},
		{StartMs: 2500, EndMs: 3000, Text: "new"},
	}
	if err := ValidateSegmentInputs(valid); err != nil {
		t.Fatalf("valid segments error = %v", err)
	}

	cases := []struct {
		name     string
		segments []SegmentInput
	}{
		{"negative start", []SegmentInput{{StartMs: -1, EndMs: 100, Text: "x"}}},
		{"empty window", []SegmentInput{{StartMs: 100, EndMs: 100, Text: "x"}}},
		{"reversed window", []SegmentInput{{StartMs: 200, EndMs: 100, Text: "x"}}},
		{"overlap", []SegmentInput{{StartMs: 0, EndMs: 1000}, {StartMs: 999, EndMs: 2000}}},
		{"out of order", []SegmentInput{{StartMs: 1000, EndMs: 2000}, {StartMs: 0, EndMs: 500}}},
		{"duplicate id", []SegmentInput{{ID: "a", StartMs: 0, EndMs: 100}, {ID: "a", StartMs: 100, EndMs: 200}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSegmentInputs(tc.segments); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("ValidateSegmentInputs() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestValidateRunConfig(t *testing.T) {
	valid := RunConfig{
		Extract:       &ExtractOptions{Mode: ExtractModeASROCR, OCRLanguage: OCRLanguageJapanese},
		EraseMode:     EraseModeDialogAndArt,
		SubtitleStyle: &SubtitleStyle{Align: SubtitleAlignCenter, MaxTranslationLines: 3},
	}
	if err := ValidateRunConfig(valid); err != nil {
		t.Fatalf("valid run config error = %v", err)
	}
	// 未指定可选字段时不应被拒绝。
	if err := ValidateRunConfig(RunConfig{}); err != nil {
		t.Errorf("empty run config error = %v", err)
	}

	cases := []struct {
		name string
		cfg  RunConfig
	}{
		{"bad extract mode", RunConfig{Extract: &ExtractOptions{Mode: "unknown"}}},
		{"bad ocr language", RunConfig{Extract: &ExtractOptions{Mode: ExtractModeOCR, OCRLanguage: "klingon"}}},
		{"bad erase mode", RunConfig{EraseMode: "everything"}},
		{"bad align", RunConfig{SubtitleStyle: &SubtitleStyle{Align: "justify"}}},
		{"max lines too small", RunConfig{SubtitleStyle: &SubtitleStyle{MaxTranslationLines: -1}}},
		{"max lines too large", RunConfig{SubtitleStyle: &SubtitleStyle{MaxTranslationLines: 5}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateRunConfig(tc.cfg); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("ValidateRunConfig() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestErrorCodeAndPreflight(t *testing.T) {
	if code, ok := CodeOf(ErrRevisionConflict); !ok || code != ErrorCodeRevisionConflict {
		t.Errorf("CodeOf(ErrRevisionConflict) = %s/%v", code, ok)
	}
	if _, ok := CodeOf(errors.New("plain")); ok {
		t.Error("CodeOf(plain error) ok = true, want false")
	}
	if info := ErrInvalidRequest.Info(); info.ActionIDs == nil {
		t.Error("Error.Info().ActionIDs = nil, want empty array")
	}

	result := PreflightResult{Issues: []PreflightIssue{
		{Severity: PreflightPass, Code: "OK"},
		{Severity: PreflightWarning, Code: "SLOW_CPU"},
	}}
	if result.HasBlocker() || !result.CanProceed() {
		t.Error("warning-only preflight should proceed without blocker")
	}
	result.Issues = append(result.Issues, PreflightIssue{Severity: PreflightBlocker, Code: "MODEL_NOT_INSTALLED"})
	if !result.HasBlocker() || result.CanProceed() {
		t.Error("blocker preflight should not proceed")
	}
}
