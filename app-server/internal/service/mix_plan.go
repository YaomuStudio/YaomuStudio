package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

type frozenMixPlan struct {
	InputIDs           []domain.ID
	Digest             string
	Params             map[string]any
	SubtitleRevisionID domain.ID
	SubtitleDigest     string
}

// errMixPlanNotReady 表示 08 需要的前驱媒体产物尚未提交（例如 05 擦除还在
// 执行）。推进时等待下一次提交，不创建缺输入的任务，也不回退原片。
var errMixPlanNotReady = errors.New("mix plan input not ready")

// errMixPlanUnavailable 表示 08 计划本身的本地准备失败（例如字幕 staging 目录
// 不可写）。这种失败必须登记为 08 自己的可读失败，不能向上返回错误：推进事务
// 与上游 07 的成功提交共用同一事务，直接返回会把已成功的 07 结果一起回滚。
var errMixPlanUnavailable = errors.New("mix plan preparation unavailable")

// mixMaxAudioInputs 镜像 Runner video_merge 的 audio_inputs 上限
// （runner/src/yaomustudio_runner/ffmpeg.py）。超出时在 Core 内直接给出可读
// 失败，而不是把越界计划下发后收到不透明的 MEDIA_TOOL_INVALID_PARAMS。
const mixMaxAudioInputs = 32

// freezeMixPlan projects this Run's exact revision and selected candidates onto
// the same controlled FFmpeg operation used by media exports. The returned
// params intentionally keep the `core://frozen-subtitle` placeholder; the
// scheduler materializes the frozen revision into the attempt workspace from
// the subtitle revision/digest in `_core`.
func (s *Service) freezeMixPlan(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	tpl pipelineTemplate,
	revision domain.SubtitleRevision,
	language string,
) (frozenMixPlan, error) {
	sourcePath, source, err := s.mixSourceVideo(ctx, tx, snap, tpl, *snap.run)
	if err != nil {
		return frozenMixPlan{}, err
	}

	inputIDs := make([]domain.ID, 0, len(revision.Segments)+3)
	inputSeen := map[domain.ID]struct{}{}
	appendInput := func(id domain.ID) {
		if id == "" {
			return
		}
		if _, exists := inputSeen[id]; exists {
			return
		}
		inputSeen[id] = struct{}{}
		inputIDs = append(inputIDs, id)
	}
	appendInput(revision.ID)
	appendInput(source.ID)

	audio := make([]exportAudioInput, 0, len(revision.Segments)+1)
	selectionIDs := make([]domain.ID, 0, len(revision.Segments))

	text := renderSRT(revision.Segments)
	if text == "" {
		return frozenMixPlan{}, invalidRequestf("mix revision contains no subtitle cues")
	}

	var params map[string]any
	switch {
	case tpl.Has(domain.NodeKeyClone):
		work, err := s.segmentWorkForRevision(ctx, tx, snap, revision, language)
		if err != nil {
			return frozenMixPlan{}, err
		}
		for _, segment := range revision.Segments {
			selection, err := tx.GetCandidateSelection(ctx, segment.ID, language)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return frozenMixPlan{}, invalidRequestf(
						"segment %s has no selected voice candidate for language %s",
						segment.ID, language)
				}
				return frozenMixPlan{}, mapReference(err, "current sentence selection")
			}
			if selection.Revision != segment.ContentRevision {
				return frozenMixPlan{}, invalidRequestf("sentence selection does not match its content revision")
			}
			candidates, err := tx.ListAudioCandidates(ctx, segment.ID, language, &selection.Revision)
			if err != nil {
				return frozenMixPlan{}, err
			}
			var chosen *domain.AudioCandidate
			for i := range candidates {
				if candidates[i].ID == selection.CandidateID {
					chosen = &candidates[i]
					break
				}
			}
			if chosen == nil || !candidateMatchesWork(snap.tasks, *chosen, work[segment.ID].CacheKey) {
				return frozenMixPlan{}, invalidRequestf("sentence selection does not match frozen generation dependencies")
			}
			asset, err := pipelineAsset(ctx, tx, snap.run.ProjectID, chosen.AudioAssetID)
			if err != nil {
				return frozenMixPlan{}, err
			}
			if !isPipelineAudio(asset) {
				return frozenMixPlan{}, invalidRequestf("selected sentence asset is not audio")
			}
			path, err := exportAssetPath(asset)
			if err != nil {
				return frozenMixPlan{}, err
			}
			audio = append(audio, exportAudioInput{Path: path, Volume: exportVoiceVolume, StartMs: segment.StartMs})
			appendInput(asset.ID)
			selectionIDs = append(selectionIDs, chosen.ID)
		}

		background, err := mixBackgroundAsset(ctx, tx, snap, tpl)
		if err != nil {
			return frozenMixPlan{}, err
		}
		if background != nil {
			path, err := exportAssetPath(*background)
			if err != nil {
				return frozenMixPlan{}, err
			}
			audio = append(audio, exportAudioInput{Path: path, Volume: exportAmbientVolume})
			appendInput(background.ID)
		} else if source.Meta != nil && source.Meta.AudioTracks > 0 {
			// Clone-only custom chains keep the source video's ambient track at
			// 0.3; metadata absent means "unknown", so omit rather than guess.
			audio = append(audio, exportAudioInput{Path: sourcePath, Volume: exportAmbientVolume})
		}
		if len(audio) > mixMaxAudioInputs {
			return frozenMixPlan{}, invalidRequestf(
				"node 08 has %d audio inputs (%d dubbed segments); the media runner accepts at most %d",
				len(audio), len(revision.Segments), mixMaxAudioInputs)
		}
		params = map[string]any{
			"op":                 "video_merge",
			"video_src":          sourcePath,
			"audio_inputs":       audioInputParams(audio),
			"out":                "preview.mp4",
			"video_codec":        "h264",
			"crf":                20,
			"preset":             "medium",
			"audio_codec":        "aac",
			"audio_bitrate_kbps": exportAudioBitrate,
			"sample_rate":        exportSampleRate,
		}

	case tpl.Has(domain.NodeKeySeparate):
		background, err := mixBackgroundAsset(ctx, tx, snap, tpl)
		if err != nil {
			return frozenMixPlan{}, err
		}
		if background == nil {
			return frozenMixPlan{}, invalidRequestf("separation has no explicitly declared background audio")
		}
		path, err := exportAssetPath(*background)
		if err != nil {
			return frozenMixPlan{}, err
		}
		audio = append(audio, exportAudioInput{Path: path, Volume: exportAmbientVolume})
		appendInput(background.ID)
		params = map[string]any{
			"op":                 "video_merge",
			"video_src":          sourcePath,
			"audio_inputs":       audioInputParams(audio),
			"out":                "preview.mp4",
			"video_codec":        "h264",
			"crf":                20,
			"preset":             "medium",
			"audio_codec":        "aac",
			"audio_bitrate_kbps": exportAudioBitrate,
			"sample_rate":        exportSampleRate,
		}

	default:
		// 无 07 且无分离：burn_subtitles 默认保留源视频原声（无 -an / 无显式
		// map），只把当前语言译文烧进画面，符合“只合成译文与视频”的契约语义。
		params = map[string]any{
			"op":                 "burn_subtitles",
			"input":              sourcePath,
			"subtitle":           stagedSubtitlePlaceholder,
			"out":                "preview.mp4",
			"video_codec":        "h264",
			"crf":                20,
			"preset":             "medium",
			"audio_codec":        "aac",
			"audio_bitrate_kbps": exportAudioBitrate,
			"style":              runnerSubtitleStyle(effectiveStyle(&revision, snap.run)),
		}
		digest, err := mixPlanDigest(snap, revision, language, inputIDs, selectionIDs, text, params)
		if err != nil {
			return frozenMixPlan{}, err
		}
		subtitleDigest := sha256.Sum256([]byte(text))
		return frozenMixPlan{
			InputIDs: inputIDs, Digest: digest, Params: params,
			SubtitleRevisionID: revision.ID,
			SubtitleDigest:     hex.EncodeToString(subtitleDigest[:]),
		}, nil
	}

	if _, ok := params["style"]; !ok {
		params["style"] = runnerSubtitleStyle(effectiveStyle(&revision, snap.run))
	}
	digest, err := mixPlanDigest(snap, revision, language, inputIDs, selectionIDs, text, params)
	if err != nil {
		return frozenMixPlan{}, err
	}
	params["subtitle"] = stagedSubtitlePlaceholder
	subtitleDigest := sha256.Sum256([]byte(text))
	return frozenMixPlan{
		InputIDs: inputIDs, Digest: digest, Params: params,
		SubtitleRevisionID: revision.ID,
		SubtitleDigest:     hex.EncodeToString(subtitleDigest[:]),
	}, nil
}

func mixPlanDigest(
	snap *pipelineSnapshot,
	revision domain.SubtitleRevision,
	language string,
	inputIDs []domain.ID,
	selectionIDs []domain.ID,
	subtitle string,
	params map[string]any,
) (string, error) {
	// revision_digest 是 store 读取时派生的只读字段，不属于冻结计划输入。
	// 排除它，保证无 style/erase 的旧 revision 升级后摘要与升级前一致，
	// 已持久化的 08 Task.InputDigest/任务身份不会因升级而改变。
	revision.RevisionDigest = ""
	return domain.Digest(struct {
		RunID        domain.ID
		Language     string
		Revision     domain.SubtitleRevision
		Inputs       []domain.ID
		SelectionIDs []domain.ID
		Subtitle     string
		Plan         map[string]any
	}{
		RunID: snap.run.ID, Language: language, Revision: revision,
		Inputs: inputIDs, SelectionIDs: selectionIDs, Subtitle: subtitle, Plan: params,
	})
}

func candidateMatchesWork(tasks []domain.Task, candidate domain.AudioCandidate, key string) bool {
	for _, task := range tasks {
		matches := task.Status == domain.TaskStatusSucceeded &&
			task.SegmentID == candidate.SegmentID && task.Language == candidate.Language &&
			taskSegmentCacheKey(task) == key
		if !matches {
			continue
		}
		for _, id := range task.ArtifactIDs {
			if id == candidate.AudioAssetID {
				return true
			}
		}
	}
	return false
}

func mixBackgroundAsset(ctx context.Context, tx *store.Tx, snap *pipelineSnapshot, tpl pipelineTemplate) (*domain.Asset, error) {
	if tpl.Has(domain.NodeKeySeparate) && snap.run.Config.ReuseAudioTrackID == "" {
		asset, err := separationRoleAsset(ctx, tx, snap, "background")
		if err != nil {
			return nil, err
		}
		return &asset, nil
	}
	if tpl.Has(domain.NodeKeyClone) {
		// Clone-only custom chains and reused tracks have no produced
		// background node. The source video itself is mixed at ambient volume
		// when its metadata reports an audio track.
		return nil, nil
	}
	for _, id := range snap.run.Config.InputRevisionIDs {
		asset, err := pipelineAsset(ctx, tx, snap.run.ProjectID, id)
		if err != nil {
			return nil, err
		}
		if asset.Meta != nil && asset.Meta.AudioTracks > 0 && (isPipelineAudio(asset) || asset.Type == domain.AssetTypeVideo) {
			return &asset, nil
		}
	}
	return nil, nil
}

// mixSourceVideo 解析 08 的视频源。
//
// 模板含 05 时只接受最近一次成功擦除产物中的视频；尚未提交时返回
// errMixPlanNotReady 让调用方等待，绝不回退到未擦除原片。05 已进入失败/取消/
// 中断终态时返回可读错误，让 08 收敛为失败而不是永久等待。
func (s *Service) mixSourceVideo(
	ctx context.Context,
	q exportQuerier,
	snap *pipelineSnapshot,
	tpl pipelineTemplate,
	run domain.WorkflowRun,
) (string, domain.Asset, error) {
	if tpl.Has(domain.NodeKeyErase) {
		if eraseTask, ok := latestNodeTask(snap.tasks, domain.NodeKeyErase, ""); ok {
			switch eraseTask.Status {
			case domain.TaskStatusFailed, domain.TaskStatusCanceled, domain.TaskStatusInterrupted:
				return "", domain.Asset{}, invalidRequestf(
					"erase node task %s ended %s; retry erase before mix",
					eraseTask.ID, eraseTask.Status)
			}
		}
		artifacts := latestSucceededArtifacts(snap.tasks, domain.NodeKeyErase, "")
		for _, id := range artifacts {
			asset, err := q.GetAsset(ctx, id)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return "", domain.Asset{}, errMixPlanNotReady
				}
				return "", domain.Asset{}, err
			}
			if asset.Type != domain.AssetTypeVideo {
				continue
			}
			path, err := exportAssetPath(asset)
			if err != nil {
				return "", domain.Asset{}, err
			}
			return path, asset, nil
		}
		return "", domain.Asset{}, errMixPlanNotReady
	}

	for _, id := range run.Config.InputRevisionIDs {
		asset, err := q.GetAsset(ctx, id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return "", domain.Asset{}, invalidRequestf("input asset %s not found", id)
			}
			return "", domain.Asset{}, err
		}
		if asset.Type != domain.AssetTypeVideo {
			continue
		}
		path, err := exportAssetPath(asset)
		if err != nil {
			return "", domain.Asset{}, err
		}
		return path, asset, nil
	}
	return "", domain.Asset{}, invalidRequestf(
		"node 08 requires a video input asset; audio-only runs cannot produce a preview")
}

// mixDependenciesReady 判断 08 是否可以进入计划构造；前驱节点仍在运行时等待，
// 已进入终态失败时放行，让 freezeMixPlan 生成可读失败任务。
func mixDependenciesReady(snap *pipelineSnapshot, tpl pipelineTemplate) bool {
	if tpl.Has(domain.NodeKeyErase) && !nodeCompleted(snap.run, domain.NodeKeyErase) {
		return nodeTerminalFailure(snap, domain.NodeKeyErase)
	}
	if tpl.Has(domain.NodeKeySeparate) && !nodeCompleted(snap.run, domain.NodeKeySeparate) {
		return nodeTerminalFailure(snap, domain.NodeKeySeparate)
	}
	return true
}

func nodeTerminalFailure(snap *pipelineSnapshot, node domain.PipelineNodeKey) bool {
	task, ok := latestNodeTask(snap.tasks, node, "")
	if !ok {
		return false
	}
	switch task.Status {
	case domain.TaskStatusFailed, domain.TaskStatusCanceled, domain.TaskStatusInterrupted:
		return true
	default:
		return false
	}
}

// mixDependenciesFailed reports a terminal upstream media-node failure. In that
// case 08 must fail readably even when the language never reached translation.
func mixDependenciesFailed(snap *pipelineSnapshot, tpl pipelineTemplate) bool {
	if tpl.Has(domain.NodeKeyErase) && !nodeCompleted(snap.run, domain.NodeKeyErase) &&
		nodeTerminalFailure(snap, domain.NodeKeyErase) {
		return true
	}
	if tpl.Has(domain.NodeKeySeparate) && !nodeCompleted(snap.run, domain.NodeKeySeparate) &&
		nodeTerminalFailure(snap, domain.NodeKeySeparate) {
		return true
	}
	return false
}

// ensureMixPlanFailure 为无法构造计划的 08 直接登记 FAILED 任务。
//
// 计划缺输入（缺选择/过期选择/源缺失/纯音频/超出 Runner 输入上限）或本地
// staging 准备失败都是 08 自己的可读失败：不能把上游 07 的成功终态事务一起
// 回滚，也不能跳过缺句或插静音产出“成功”成片。任务不登记任何产物，节点按
// 失败聚合；修复输入后新的推进会用新的输入摘要建立新任务。
func (s *Service) ensureMixPlanFailure(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	language string,
	revision *domain.SubtitleRevision,
	cause error,
	events *[]domain.DomainEvent,
) error {
	inputIDs := []string{}
	if revision != nil {
		inputIDs = append(inputIDs, string(revision.ID))
	}
	inputHash, err := taskInputDigest(CreateTaskRequest{
		RunID:            string(snap.run.ID),
		Kind:             domain.TaskKindNode,
		NodeKey:          domain.NodeKeyMix,
		Language:         language,
		InputRevisionIDs: inputIDs,
	})
	if err != nil {
		return err
	}
	id := pipelineTaskID(string(snap.run.ID), string(domain.NodeKeyMix), language, "", inputHash)
	if _, exists := snap.taskByID[id]; exists {
		return nil
	}
	now := s.Now()
	stepID := stepIDForTask(domain.TaskKindNode, domain.NodeKeyMix)
	taskInputs := make([]domain.ID, 0, len(inputIDs))
	for _, id := range inputIDs {
		taskInputs = append(taskInputs, domain.ID(id))
	}
	errorCode := domain.ErrorCodeInvalidRequest
	messageKey := "task.mix_plan_invalid"
	if !errors.Is(cause, domain.ErrInvalidRequest) {
		errorCode = domain.ErrorCodeResourceExhausted
		messageKey = "task.mix_plan_unavailable"
	}
	task := domain.Task{
		ID:               id,
		ProjectID:        snap.run.ProjectID,
		RunID:            snap.run.ID,
		Kind:             domain.TaskKindNode,
		NodeKey:          domain.NodeKeyMix,
		Language:         language,
		Status:           domain.TaskStatusFailed,
		Progress:         0,
		ArtifactIDs:      []domain.ID{},
		InputRevisionIDs: taskInputs,
		InputDigest:      inputHash,
		Error: &domain.ErrorInfo{
			Code:       errorCode,
			MessageKey: messageKey,
			Retryable:  false,
			ActionIDs:  []domain.ID{},
		},
		CreatedAt: now,
		UpdatedAt: now,
		Steps: []domain.TaskStep{{
			StepID: stepID, Status: domain.StepStatusFailed, AttemptNo: 1, Epoch: s.opts.ServerEpoch,
		}},
	}
	if err := tx.CreateTask(ctx, task); err != nil {
		return err
	}
	if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:         "task.created",
		ProjectID:    task.ProjectID,
		TaskID:       task.ID,
		StateVersion: task.StateVersion,
		Data: map[string]any{
			"kind":       task.Kind,
			"node_key":   task.NodeKey,
			"segment_id": task.SegmentID,
			"language":   task.Language,
			"status":     task.Status,
		},
	}); err != nil {
		return err
	}
	if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:         "task.failed",
		ProjectID:    task.ProjectID,
		TaskID:       task.ID,
		StateVersion: task.StateVersion,
		Data: map[string]any{
			"code":        task.Error.Code,
			"message_key": task.Error.MessageKey,
		},
	}); err != nil {
		return err
	}
	if s.log != nil {
		s.log.Warn("service", "service.mix_plan_invalid", map[string]any{
			"run_id":   string(snap.run.ID),
			"language": language,
			"error":    cause.Error(),
		})
	}
	snap.tasks = append(snap.tasks, task)
	snap.taskByID[task.ID] = task
	return nil
}
