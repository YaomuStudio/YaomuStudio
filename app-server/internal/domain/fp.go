package domain

// 句级缓存指纹（纯函数）。
//
// 依据 docs/data-and-storage.md §9 与 docs/workflow-and-review.md §4：
//   - 整轨 revision 只用于溯源/审核，不作为每一句共同的缓存失效键；
//   - 句级等价键至少包含稳定句 ID、句级 content_revision/内容摘要、
//     时间范围、参考声源摘要、必要邻接上下文摘要、模型/运行时版本与相关参数；
//   - 拆分/合并或上下文变化只使受影响句段及相邻窗口失效。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// cacheKeyVersion 参与哈希，语义变化时递增以整体失效旧缓存。
const cacheKeyVersion = 1

// CacheScope 是同一轨/同一生产中所有句段共享的缓存依赖。
type CacheScope struct {
	// SourceAudioDigest 是参考声源（人声轨、克隆声源或用户提供音轨）的内容摘要。
	SourceAudioDigest string
	// ModelLock 是冻结的模型/运行时发布引用。
	ModelLock ReleaseRef
	// Params 是受控参数快照；必须可 JSON 序列化。
	Params map[string]any
	// ContextRadius 是参与键的相邻句数；0 表示不依赖上下文，1 表示前后各一句。
	ContextRadius int
}

// cacheKeyDoc 是参与哈希的规范化结构；字段顺序由结构固定，map 键由 encoding/json 排序。
type cacheKeyDoc struct {
	Version           int            `json:"v"`
	SegmentID         ID             `json:"segment_id"`
	ContentRevision   int            `json:"content_revision"`
	ContentDigest     string         `json:"content_digest"`
	StartMs           Millis         `json:"start_ms"`
	EndMs             Millis         `json:"end_ms"`
	Speaker           string         `json:"speaker,omitempty"`
	SourceAudioDigest string         `json:"source_audio_digest"`
	PrevContext       []string       `json:"prev_context"`
	NextContext       []string       `json:"next_context"`
	ModelLock         ReleaseRef     `json:"model_lock"`
	Params            map[string]any `json:"params"`
}

// contextDigest 计算一个相邻句段的上下文摘要（内容 + 时间窗 + 稳定 ID）。
func contextDigest(seg Segment) string {
	doc := struct {
		ID            ID     `json:"id"`
		ContentDigest string `json:"content_digest"`
		StartMs       Millis `json:"start_ms"`
		EndMs         Millis `json:"end_ms"`
	}{
		ID:            seg.ID,
		ContentDigest: seg.ContentDigest,
		StartMs:       seg.StartMs,
		EndMs:         seg.EndMs,
	}
	return digestJSON(doc)
}

// contextDigests 计算相邻窗口内每个句段的上下文摘要。
func contextDigests(segments []Segment) []string {
	digests := make([]string, 0, len(segments))
	for _, seg := range segments {
		digests = append(digests, contextDigest(seg))
	}
	return digests
}

// SegmentCacheKey 计算单个句段的缓存等价键。
//
// prev/next 是参与上下文的相邻句段（按距离从近到远），由调用方按
// CacheScope.ContextRadius 选择窗口；相邻句的 ID、内容摘要与时间窗都进入键。
func SegmentCacheKey(seg Segment, prev, next []Segment, scope CacheScope) (string, error) {
	if seg.ID == "" {
		return "", fmt.Errorf("%w: segment id required", ErrInvalidRequest)
	}
	if scope.ContextRadius < 0 {
		return "", fmt.Errorf("%w: context radius < 0", ErrInvalidRequest)
	}
	params := scope.Params
	if params == nil {
		params = map[string]any{}
	}
	doc := cacheKeyDoc{
		Version:           cacheKeyVersion,
		SegmentID:         seg.ID,
		ContentRevision:   seg.ContentRevision,
		ContentDigest:     seg.ContentDigest,
		StartMs:           seg.StartMs,
		EndMs:             seg.EndMs,
		Speaker:           seg.Speaker,
		SourceAudioDigest: scope.SourceAudioDigest,
		PrevContext:       contextDigests(prev),
		NextContext:       contextDigests(next),
		ModelLock:         scope.ModelLock,
		Params:            params,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("%w: encode cache key: %v", ErrInvalidRequest, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// SegmentCacheKeys 为整轨计算句级缓存键，按 CacheScope.ContextRadius 取相邻窗口。
//
// 相邻窗口边界处缺少的邻居不会补空占位，因此轨道首尾句段与中间句段的键不同，
// 但同一输入重复计算结果稳定。返回 map[segment_id]key；重复 ID 返回错误。
func SegmentCacheKeys(segments []Segment, scope CacheScope) (map[string]string, error) {
	if scope.ContextRadius < 0 {
		return nil, fmt.Errorf("%w: context radius < 0", ErrInvalidRequest)
	}
	keys := make(map[string]string, len(segments))
	for i, seg := range segments {
		if _, ok := keys[seg.ID]; ok {
			return nil, fmt.Errorf("%w: duplicate segment id %q", ErrInvalidRequest, seg.ID)
		}
		lo := i - scope.ContextRadius
		if lo < 0 {
			lo = 0
		}
		hi := i + scope.ContextRadius
		if hi > len(segments)-1 {
			hi = len(segments) - 1
		}
		key, err := SegmentCacheKey(seg, segments[lo:i], segments[i+1:hi+1], scope)
		if err != nil {
			return nil, err
		}
		keys[seg.ID] = key
	}
	return keys, nil
}

// SegmentContentDigest 计算句段内容摘要（文本 + 说话人），由 Core 在保存 revision 时写入。
func SegmentContentDigest(text, speaker string) string {
	doc := struct {
		Text    string `json:"text"`
		Speaker string `json:"speaker,omitempty"`
	}{Text: text, Speaker: speaker}
	return digestJSON(doc)
}

// Digest 计算任意可 JSON 序列化值的稳定摘要（sha256 hex）。
// 结构体字段顺序固定；map 键由 encoding/json 排序，因此等价输入得到同一摘要。
func Digest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("%w: digest value: %v", ErrInvalidRequest, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func digestJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// 这里的输入只含字符串/整数结构体，编码不会失败；保持纯函数签名简单。
		panic(fmt.Sprintf("domain: marshal digest input: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SortedMapKeys 返回 map 的有序键，供稳定比较/调试使用。
func SortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
