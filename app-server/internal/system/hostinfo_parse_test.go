package system

import "testing"

func TestParseLinuxMeminfo(t *testing.T) {
	t.Run("MemAvailable", func(t *testing.T) {
		memory := parseLinuxMeminfo([]byte(
			"MemTotal:       8000000 kB\n" +
				"MemFree:        1000000 kB\n" +
				"MemAvailable:   3000000 kB\n" +
				"Buffers:          50000 kB\n" +
				"Cached:         1000000 kB\n",
		))
		if !memory.HasTotal || !memory.HasUsed {
			t.Fatalf("memory = %+v, want total and used", memory)
		}
		if memory.TotalBytes != 8000000*1024 || memory.UsedBytes != 5000000*1024 {
			t.Fatalf("memory = %+v", memory)
		}
	})

	t.Run("FallbackWithoutMemAvailable", func(t *testing.T) {
		memory := parseLinuxMeminfo([]byte(
			"MemTotal:       8000000 kB\n" +
				"MemFree:        1000000 kB\n" +
				"Buffers:         500000 kB\n" +
				"Cached:         1500000 kB\n",
		))
		if !memory.HasTotal || !memory.HasUsed {
			t.Fatalf("memory = %+v, want total and used", memory)
		}
		if memory.UsedBytes != 5000000*1024 {
			t.Fatalf("used = %d", memory.UsedBytes)
		}
	})

	t.Run("TotalOnlyWithoutFreeBreakdown", func(t *testing.T) {
		memory := parseLinuxMeminfo([]byte("MemTotal: 8000000 kB\n"))
		if !memory.HasTotal || memory.HasUsed {
			t.Fatalf("memory = %+v, want total only", memory)
		}
	})

	t.Run("MissingTotal", func(t *testing.T) {
		memory := parseLinuxMeminfo([]byte("MemFree: 1000 kB\n"))
		if memory.HasTotal || memory.HasUsed {
			t.Fatalf("memory = %+v, want empty", memory)
		}
	})

	t.Run("AvailableAboveTotalClamps", func(t *testing.T) {
		memory := parseLinuxMeminfo([]byte(
			"MemTotal:       1000 kB\n" +
				"MemAvailable:   5000 kB\n",
		))
		if !memory.HasUsed || memory.UsedBytes != 0 {
			t.Fatalf("memory = %+v, want used 0", memory)
		}
	})
}

func TestParseVMStat(t *testing.T) {
	t.Run("AppleSiliconPageSize", func(t *testing.T) {
		used, ok := parseVMStat([]byte(
			"Mach Virtual Memory Statistics: (page size of 16384 bytes)\n" +
				"Pages free: 1000.\n" +
				"Pages active: 100.\n" +
				"Pages inactive: 50.\n" +
				"Pages wired down: 20.\n" +
				"Pages occupied by compressor: 5.\n",
		))
		if !ok {
			t.Fatal("parseVMStat returned no result")
		}
		if want := int64(125 * 16384); used != want {
			t.Fatalf("used = %d, want %d", used, want)
		}
	})

	t.Run("DefaultPageSize", func(t *testing.T) {
		used, ok := parseVMStat([]byte(
			"Mach Virtual Memory Statistics:\n" +
				"Pages active: 100.\n" +
				"Pages wired down: 20.\n",
		))
		if !ok {
			t.Fatal("parseVMStat returned no result")
		}
		if want := int64(120 * 4096); used != want {
			t.Fatalf("used = %d, want %d", used, want)
		}
	})

	t.Run("InvalidPageSizeFallsBack", func(t *testing.T) {
		used, ok := parseVMStat([]byte(
			"Mach Virtual Memory Statistics: (page size of 0 bytes)\n" +
				"Pages active: 10.\n",
		))
		if !ok {
			t.Fatal("parseVMStat returned no result")
		}
		if want := int64(10 * 4096); used != want {
			t.Fatalf("used = %d, want %d", used, want)
		}
	})

	t.Run("NoMatchedPages", func(t *testing.T) {
		if _, ok := parseVMStat([]byte("Pages free: 100.\n")); ok {
			t.Fatal("parseVMStat matched free pages only")
		}
	})
}

func TestParseProcCPUInfo(t *testing.T) {
	t.Run("ModelName", func(t *testing.T) {
		name := parseProcCPUInfo([]byte("processor : 0\nmodel name : Intel(R) Core(TM) i7\n"))
		if name != "Intel(R) Core(TM) i7" {
			t.Fatalf("name = %q", name)
		}
	})

	t.Run("HardwareFallback", func(t *testing.T) {
		name := parseProcCPUInfo([]byte("processor : 0\nHardware : ARMv8 Processor rev 1\n"))
		if name != "ARMv8 Processor rev 1" {
			t.Fatalf("name = %q", name)
		}
	})

	t.Run("CPUKeyFallback", func(t *testing.T) {
		name := parseProcCPUInfo([]byte("processor : 0\nCPU : Freescale i.MX8\n"))
		if name != "Freescale i.MX8" {
			t.Fatalf("name = %q", name)
		}
	})

	t.Run("Missing", func(t *testing.T) {
		if name := parseProcCPUInfo([]byte("processor : 0\n")); name != "" {
			t.Fatalf("name = %q, want empty", name)
		}
	})
}
