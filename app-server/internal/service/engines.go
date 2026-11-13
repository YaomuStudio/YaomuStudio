package service

import (
	"context"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// EngineConfigUpdate 是槽位配置更新输入；values 只接受受控 Schema 内的键。
type EngineConfigUpdate struct {
	DeviceID *string        `json:"device_id"`
	Values   map[string]any `json:"values"`
}

// GetEngineConfig 读取槽位配置；未配置时返回默认 auto。
func (s *Service) GetEngineConfig(ctx context.Context, slotValue string) (domain.EngineConfig, error) {
	slot, ok := modelSlotOf(slotValue)
	if !ok {
		return domain.EngineConfig{}, notFoundf("unknown model slot %q", slotValue)
	}
	cfg, _, err := s.store.GetEngineConfig(ctx, slot)
	if err != nil {
		return domain.EngineConfig{}, err
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID = "auto"
	}
	if cfg.Values == nil {
		cfg.Values = map[string]any{}
	}
	return cfg, nil
}

// UpdateEngineConfig 更新槽位配置（200）；设备/版本变化只对后续任务生效。
func (s *Service) UpdateEngineConfig(ctx context.Context, idem Idem, slotValue string, update EngineConfigUpdate) (Response, error) {
	slot, ok := modelSlotOf(slotValue)
	if !ok {
		return Response{}, notFoundf("unknown model slot %q", slotValue)
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "engines.config", idem, 200, func(ctx context.Context, tx *store.Tx) (any, error) {
		existing, _, err := tx.GetEngineConfig(ctx, slot)
		if err != nil {
			return nil, err
		}
		deviceID := existing.DeviceID
		if deviceID == "" {
			deviceID = "auto"
		}
		if update.DeviceID != nil {
			valid, err := s.lookupDevice(ctx, *update.DeviceID)
			if err != nil {
				return nil, err
			}
			if !valid {
				return nil, notFoundf("device %q is not available", *update.DeviceID)
			}
			deviceID = *update.DeviceID
		}
		values := map[string]any{}
		for key, value := range existing.Values {
			values[key] = value
		}
		for key, value := range update.Values {
			values[key] = value
		}
		if err := validateEngineValues(slot, values); err != nil {
			return nil, err
		}
		now := s.Now()
		cfg := domain.EngineConfig{Slot: slot, DeviceID: deviceID, Values: values, UpdatedAt: &now}
		if err := tx.SaveEngineConfig(ctx, cfg, now); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type: "engine.config_updated",
			Data: map[string]any{
				"slot":      slot,
				"device_id": deviceID,
			},
		}); err != nil {
			return nil, err
		}
		return cfg, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	return resp, nil
}
