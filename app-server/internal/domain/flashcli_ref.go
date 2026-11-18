package domain

// 已准入的 FlashCLI 模型引用。只允许这些 ref 进入 `flashcli pull <ref>`，
// 不接受目录或请求里的其他仓库。
var admittedFlashCLIRefs = map[string]struct{}{
	"tts/omnivoice_flashrt:1.0.3": {},
	"xym/melband-roformer:1.0.10": {},
	"asr/qwen3_asr:1.0.1":         {},
}

// AdmittedFlashCLIRef 报告 ref 是否是当前产品允许预拉的模型。
func AdmittedFlashCLIRef(ref string) bool {
	_, ok := admittedFlashCLIRefs[ref]
	return ok
}
