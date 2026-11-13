package service

import (
	"context"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func TestListCapabilitiesAnnotatesRuntimeState(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("capability-runtime")

	seedRunningTask(t, env, project.ID, domain.TaskStatusRunning, "asr")
	seedRunningTask(t, env, project.ID, domain.TaskStatusCancelRequested, "asr")
	seedRunningTask(t, env, project.ID, domain.TaskStatusRunning, "voice-clone")
	seedRunningTask(t, env, project.ID, domain.TaskStatusQueued, "tts")

	caps, err := env.svc.ListCapabilities(context.Background())
	if err != nil {
		t.Fatalf("ListCapabilities: %v", err)
	}
	bySlot := make(map[domain.CapabilitySlotKey]domain.Capability, len(caps))
	for _, capability := range caps {
		bySlot[capability.Slot] = capability
	}

	if got := bySlot[domain.CapabilitySlotASR]; got.RuntimeState != domain.CapabilityRuntimeRunning || got.RunningTasks != 2 {
		t.Fatalf("asr runtime = %s/%d, want running/2", got.RuntimeState, got.RunningTasks)
	}
	if got := bySlot[domain.CapabilitySlotVoiceClone]; got.RuntimeState != domain.CapabilityRuntimeRunning || got.RunningTasks != 1 {
		t.Fatalf("voice-clone runtime = %s/%d, want running/1", got.RuntimeState, got.RunningTasks)
	}
	if got := bySlot[domain.CapabilitySlotTTS]; got.RuntimeState != domain.CapabilityRuntimeIdle || got.RunningTasks != 0 {
		t.Fatalf("tts runtime = %s/%d, want idle/0 (QUEUED is not running)", got.RuntimeState, got.RunningTasks)
	}
}

func TestListCapabilitiesIgnoresUnmappedSlot(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("capability-runtime-unmapped")
	seedRunningTask(t, env, project.ID, domain.TaskStatusRunning, "")

	caps, err := env.svc.ListCapabilities(context.Background())
	if err != nil {
		t.Fatalf("ListCapabilities: %v", err)
	}
	for _, capability := range caps {
		if capability.RuntimeState != domain.CapabilityRuntimeIdle || capability.RunningTasks != 0 {
			t.Fatalf("capability %s = %s/%d, want idle/0", capability.Slot, capability.RuntimeState, capability.RunningTasks)
		}
	}
}

func seedRunningTask(
	t *testing.T,
	env *exportTestEnv,
	projectID domain.ID,
	status domain.TaskStatus,
	slot string,
) domain.Task {
	t.Helper()
	now := time.Now().UTC()
	params := map[string]any{}
	if slot != "" {
		params["_core"] = map[string]any{"execution_slot": slot}
	}
	task := domain.Task{
		ID:        store.NewID("task"),
		ProjectID: projectID,
		Kind:      domain.TaskKindNode,
		Status:    status,
		Params:    params,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := env.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask(%s): %v", status, err)
	}
	return task
}
