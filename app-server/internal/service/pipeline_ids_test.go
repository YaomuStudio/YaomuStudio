package service

import "testing"

func TestPipelineTaskIDStableAndScoped(t *testing.T) {
	first := pipelineTaskID("run-1", "06_translate", "en", "", "rev-a")
	same := pipelineTaskID("run-1", "06_translate", "en", "", "rev-a")
	otherLanguage := pipelineTaskID("run-1", "06_translate", "ja", "", "rev-a")
	otherInput := pipelineTaskID("run-1", "06_translate", "en", "", "rev-b")
	otherSegment := pipelineTaskID("run-1", "07_clone", "en", "seg-1", "rev-a")

	if first != same {
		t.Fatalf("same inputs produce different ids: %s != %s", first, same)
	}
	for name, id := range map[string]string{
		"language": otherLanguage,
		"input":    otherInput,
		"segment":  otherSegment,
	} {
		if id == first {
			t.Errorf("%s change did not change task id", name)
		}
	}
	if len(first) <= len("task-") {
		t.Fatalf("task id = %q", first)
	}
}
