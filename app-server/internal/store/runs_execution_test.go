package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestRunExecutionConfigurationRoundTripAndLegacyNull(t *testing.T) {
	s := newTestStore(t)
	project := seedProject(t, s, "execution-project")
	base := seedRun(t, s, project.ID, "legacy-run")
	cases := []struct {
		name    string
		configs map[domain.CapabilitySlotKey]domain.ExecutionConfig
	}{
		{name: "legacy null"},
		{name: "new empty snapshot", configs: map[domain.CapabilitySlotKey]domain.ExecutionConfig{}},
		{name: "frozen release and device", configs: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
			domain.CapabilitySlotTTS: {
				ModelLock: domain.ReleaseRef{ReleaseID: "frozen-tts", Version: "1", Digest: "sha256-release"},
				DeviceID:  "gpu-frozen", Params: map[string]any{"declared": true}, ContextRadius: 1,
			},
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := base
			run.ID = []string{"null-run", "empty-run", "locked-run"}[i]
			run.Config.Execution = tc.configs
			if err := s.CreateWorkflowRun(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			stored, err := s.GetWorkflowRun(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tc.configs, stored.Config.Execution) {
				t.Fatalf("config = %#v, want %#v", stored.Config.Execution, tc.configs)
			}
		})
	}
}
