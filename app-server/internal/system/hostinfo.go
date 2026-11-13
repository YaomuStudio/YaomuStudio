package system

// MemoryInfo 是系统或执行侧内存快照；HasTotal/HasUsed 区分“数值为 0”与
// “未探测到”，调用方据此省略 DeviceInfo 字段而不是伪造 0。
type MemoryInfo struct {
	TotalBytes int64
	UsedBytes  int64
	HasTotal   bool
	HasUsed    bool
}

// HostInfo 是与 GPU 执行后端无关的本机信息。
type HostInfo struct {
	CPUName string
	Memory  MemoryInfo
}
