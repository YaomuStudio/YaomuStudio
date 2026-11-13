package domain

// 共享发布安装的稳定顺序、覆盖校验与物理身份派生（纯函数）。

import (
	"fmt"
	"sort"
)

// capabilitySlotCanonicalOrder 固定能力槽位的规范顺序（六模型 + OCR + FFmpeg）。
// 覆盖/绑定投影都按它排序，保证同一集合在不同读取路径得到同一数组。
var capabilitySlotCanonicalOrder = map[CapabilitySlotKey]int{
	CapabilitySlotASR:           0,
	CapabilitySlotTranslate:     1,
	CapabilitySlotTTS:           2,
	CapabilitySlotVoiceClone:    3,
	CapabilitySlotSubtitleErase: 4,
	CapabilitySlotSeparation:    5,
	CapabilitySlotOCR:           6,
	CapabilitySlotFFmpeg:        7,
}

// NormalizeCapabilitySlots 校验已知槽位、去重并按规范顺序排序。
// 未知槽位或重复声明返回 INVALID_REQUEST；空输入返回空数组。
func NormalizeCapabilitySlots(slots []CapabilitySlotKey) ([]CapabilitySlotKey, error) {
	if len(slots) == 0 {
		return []CapabilitySlotKey{}, nil
	}
	seen := make(map[CapabilitySlotKey]struct{}, len(slots))
	for _, slot := range slots {
		if !slot.Valid() {
			return nil, fmt.Errorf("%w: unknown capability slot %q", ErrInvalidRequest, slot)
		}
		if _, ok := seen[slot]; ok {
			return nil, fmt.Errorf("%w: duplicate capability slot %q", ErrInvalidRequest, slot)
		}
		seen[slot] = struct{}{}
	}
	return SortCapabilitySlots(slots), nil
}

// SortCapabilitySlots 按规范顺序排序并去重；未知槽位排在已知槽位之后。
func SortCapabilitySlots(slots []CapabilitySlotKey) []CapabilitySlotKey {
	seen := make(map[CapabilitySlotKey]struct{}, len(slots))
	out := make([]CapabilitySlotKey, 0, len(slots))
	for _, slot := range slots {
		if _, ok := seen[slot]; ok {
			continue
		}
		seen[slot] = struct{}{}
		out = append(out, slot)
	}
	sort.SliceStable(out, func(i, j int) bool { return slotLess(out[i], out[j]) })
	return out
}

func slotLess(a, b CapabilitySlotKey) bool {
	oa, oka := capabilitySlotCanonicalOrder[a]
	ob, okb := capabilitySlotCanonicalOrder[b]
	switch {
	case oka && okb:
		return oa < ob
	case oka:
		return true
	case okb:
		return false
	default:
		return a < b
	}
}

// ContainsCapabilitySlot 报告排序后的槽位集合是否包含 slot。
func ContainsCapabilitySlot(slots []CapabilitySlotKey, slot CapabilitySlotKey) bool {
	for _, candidate := range slots {
		if candidate == slot {
			return true
		}
	}
	return false
}

// EffectiveCoveredSlots 返回记录冻结的覆盖范围；旧记录没有覆盖时只回退到
// 原始单 slot（历史事实），不做任何推测扩展。
func EffectiveCoveredSlots(inst Installation) []CapabilitySlotKey {
	if len(inst.CoveredSlots) > 0 {
		return SortCapabilitySlots(inst.CoveredSlots)
	}
	if inst.Slot.Valid() {
		return []CapabilitySlotKey{inst.Slot}
	}
	return []CapabilitySlotKey{}
}

// InstallationBoundSlot 报告安装记录仍被 slot 引用。旧静态测试夹具或手工
// 构造的未投影记录（两个投影均为 nil）保留单 slot 语义；真实 Store 读取
// 总是投影显式数组，空数组不视为未投影。
func InstallationBoundSlot(inst Installation, slot CapabilitySlotKey) bool {
	if inst.BoundSlots == nil && inst.CurrentSlots == nil {
		return inst.Slot == slot
	}
	return ContainsCapabilitySlot(inst.BoundSlots, slot)
}

// InstallationBoundSlots 返回仍引用该记录的槽位投影；未投影夹具回退原单 slot。
func InstallationBoundSlots(inst Installation) []CapabilitySlotKey {
	if inst.BoundSlots == nil && inst.CurrentSlots == nil {
		if inst.Slot.Valid() {
			return []CapabilitySlotKey{inst.Slot}
		}
		return []CapabilitySlotKey{}
	}
	return SortCapabilitySlots(inst.BoundSlots)
}

// InstallationCurrentSlot 报告该 Done 实体当前是否被 slot 选中。
func InstallationCurrentSlot(inst Installation, slot CapabilitySlotKey) bool {
	if inst.BoundSlots == nil && inst.CurrentSlots == nil {
		return inst.Phase == InstallationPhaseDone && inst.Slot == slot
	}
	return ContainsCapabilitySlot(inst.CurrentSlots, slot)
}

// InstallationCurrentSlots 返回当前选中该实体的槽位投影；未投影夹具按旧
// 单 slot Done 语义回退。
func InstallationCurrentSlots(inst Installation) []CapabilitySlotKey {
	if inst.BoundSlots == nil && inst.CurrentSlots == nil {
		if inst.Phase == InstallationPhaseDone && inst.Slot.Valid() {
			return []CapabilitySlotKey{inst.Slot}
		}
		return []CapabilitySlotKey{}
	}
	return SortCapabilitySlots(inst.CurrentSlots)
}

// ReleaseStorageKey 返回完整 ReleaseRef 的规范 SHA-256（含 channel）。
// 与精确锁相等同一口径：id/version/digest 必填，channel 可为空。
func ReleaseStorageKey(ref ReleaseRef) (string, error) {
	if ref.ReleaseID == "" || ref.Version == "" || ref.Digest == "" {
		return "", fmt.Errorf("%w: incomplete release reference", ErrInvalidRequest)
	}
	return Digest(struct {
		ReleaseID string `json:"release_id"`
		Version   string `json:"version"`
		Digest    string `json:"digest"`
		Channel   string `json:"channel,omitempty"`
	}{ReleaseID: ref.ReleaseID, Version: ref.Version, Digest: ref.Digest, Channel: ref.Channel})
}

// SameInstallationFiles 报告两个已验证文件清单的内容是否一致。
func SameInstallationFiles(a, b []InstallationFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
