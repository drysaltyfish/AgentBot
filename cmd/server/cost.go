package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/drysaltyfish/agentbot/internal/cost"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// costStoreAdapter 把成本聚合快照持久化到 SQLite（F-66 的"配额若需严谨应持久化"）。
//
// cost.Store 的接口没有 ctx：它是 cost 包内部的"后台异步保存"接缝，
// 由 Tracker 自己的协程调用。这里用后台 ctx，单次操作由 SQLite 的
// busy_timeout 与 store 的重试上限兜底，不会无限挂起。
type costStoreAdapter struct{ st *store.Store }

// Load 读取上次保存的快照；从未保存过时返回 (nil, nil)。
func (a costStoreAdapter) Load() (*cost.Snapshot, error) {
	if a.st == nil {
		return nil, nil
	}
	data, ok, err := a.st.LoadCostSnapshot(context.Background())
	if err != nil || !ok {
		return nil, err
	}
	var snap cost.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("cost snapshot: decode: %w", err)
	}
	return &snap, nil
}

// Save 覆盖保存完整快照。
func (a costStoreAdapter) Save(snap *cost.Snapshot) error {
	if a.st == nil || snap == nil {
		return nil
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("cost snapshot: encode: %w", err)
	}
	return a.st.SaveCostSnapshot(context.Background(), data)
}
