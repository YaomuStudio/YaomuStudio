package uninstall

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

func WriteINI(path string, result Result) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("result path required")
	}
	values := map[string]string{
		"schema_version":        fmt.Sprintf("%d", SchemaVersion),
		"status":                result.Status,
		"personal_bytes":        fmt.Sprintf("%d", result.PersonalBytes),
		"models_bytes":          fmt.Sprintf("%d", result.ModelsBytes),
		"models_download_bytes": fmt.Sprintf("%d", result.ModelsDownloadBytes),
		"models_known":          boolText(result.ModelsKnown),
		"models_size_display":   result.ModelsSizeDisplay,
		"snapshot":              result.Snapshot,
		"backup_status":         result.BackupStatus,
		"backup_path":           result.BackupPath,
		"personal_deleted":      boolText(result.PersonalDeleted),
		"models_deleted":        boolText(result.ModelsDeleted),
		"personal_requested":    boolText(result.PersonalRequested),
		"models_requested":      boolText(result.ModelsRequested),
		"message_key":           result.MessageKey,
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("[uninstall]\n")
	for _, key := range keys {
		value := strings.ReplaceAll(values[key], "\r", " ")
		value = strings.ReplaceAll(value, "\n", " ")
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(value)
		builder.WriteByte('\n')
	}
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func boolText(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func filepathDir(path string) string {
	at := strings.LastIndexAny(path, `/\`)
	if at < 0 {
		return "."
	}
	return path[:at]
}
