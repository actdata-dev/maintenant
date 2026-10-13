// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/kolapsis/maintenant/internal/agent"
	"github.com/kolapsis/maintenant/internal/container"
	"github.com/kolapsis/maintenant/internal/extension"
	"github.com/kolapsis/maintenant/internal/security"
	"github.com/kolapsis/maintenant/internal/store"
	"github.com/kolapsis/maintenant/internal/store/storetest"
	"github.com/kolapsis/maintenant/internal/uid"
	"github.com/kolapsis/maintenant/internal/update"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSecurityService() *security.Service {
	return security.NewService(security.Deps{Logger: slog.Default()})
}

func TestGetSecurityInsights_All(t *testing.T) {
	svc := &Services{SecuritySvc: newTestSecurityService(), Logger: slog.Default(), Version: "test"}
	result, _, err := getSecurityInsightsHandler(svc)(context.Background(), nil, getSecurityInsightsInput{})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, textFromContent(t, result.Content), "summary")
}

func TestGetSecurityInsights_ContainerEmpty(t *testing.T) {
	svc := &Services{SecuritySvc: newTestSecurityService(), Logger: slog.Default(), Version: "test"}
	result, _, err := getSecurityInsightsHandler(svc)(context.Background(), nil, getSecurityInsightsInput{ContainerID: "c-1"})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := textFromContent(t, result.Content)
	assert.Contains(t, text, "c-1")
	assert.Contains(t, text, `"count":0`)
}

func TestListCVE_CE_EditionRequired(t *testing.T) {
	withEdition(t, extension.Community)
	svc := &Services{Logger: slog.Default(), Version: "test"}
	result, _, err := listCVEHandler(svc)(context.Background(), nil, listCVEInput{})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, textFromContent(t, result.Content), "edition_required")
}

func TestListRiskScores_CE_EditionRequired(t *testing.T) {
	withEdition(t, extension.Community)
	svc := &Services{Logger: slog.Default(), Version: "test"}
	result, _, err := listRiskScoresHandler(svc)(context.Background(), nil, listRiskScoresInput{})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, textFromContent(t, result.Content), "edition_required")
}

func TestGetSecurityPosture_CE_EditionRequired(t *testing.T) {
	withEdition(t, extension.Community)
	svc := &Services{Logger: slog.Default(), Version: "test"}
	result, _, err := getSecurityPostureHandler(svc)(context.Background(), nil, getSecurityPostureInput{})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, textFromContent(t, result.Content), "edition_required")
}

func newTestUpdateStore(t *testing.T) update.UpdateStore {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"), slog.Default())
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background(), db, slog.Default()))

	ctx, cancel := context.WithCancel(context.Background())
	db.StartWriter(ctx)
	t.Cleanup(func() {
		cancel()
		_ = db.Close()
	})
	return store.NewUpdateStore(db)
}

func TestListCVE_Container_NotEvaluated(t *testing.T) {
	withEdition(t, extension.Pro)
	svc := &Services{UpdateStore: newTestUpdateStore(t), Logger: slog.Default(), Version: "test"}

	result, _, err := listCVEHandler(svc)(context.Background(), nil, listCVEInput{ContainerID: "c-1"})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, textFromContent(t, result.Content), `"evaluation":"not_evaluated"`)
}

func TestListCVE_Container_Evaluated(t *testing.T) {
	withEdition(t, extension.Pro)
	us := newTestUpdateStore(t)
	require.NoError(t, us.UpsertCVEEvaluation(context.Background(), &update.CVEEvaluation{
		ContainerID: "c-1",
		Status:      update.CVEEvaluated,
		EvaluatedAt: time.Now(),
	}))
	svc := &Services{UpdateStore: us, Logger: slog.Default(), Version: "test"}

	result, _, err := listCVEHandler(svc)(context.Background(), nil, listCVEInput{ContainerID: "c-1"})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, textFromContent(t, result.Content), `"evaluation":"evaluated"`)
}

func TestListRiskScores_CarriesTheHostOfEachRow(t *testing.T) {
	withEdition(t, extension.Pro)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := storetest.Open(t, logger)

	agentStore := store.NewAgentStore(db)
	for _, ag := range []agent.Agent{
		{AgentID: "agent-nas", Hostname: "nas-02", Label: "NAS cave", Status: "active", DetectedRuntime: "docker", CreatedAt: time.Now()},
		{AgentID: "agent-web", Hostname: "web-07", Status: "active", DetectedRuntime: "docker", CreatedAt: time.Now()},
	} {
		require.NoError(t, agentStore.Insert(ctx, &ag))
	}

	containerStore := store.NewContainerStore(db)
	updateStore := store.NewUpdateStore(db)
	scanID, err := updateStore.InsertScanRecord(ctx, &update.ScanRecord{StartedAt: time.Now(), Status: update.ScanStatusCompleted})
	require.NoError(t, err)
	now := time.Now()
	for externalID, agentID := range map[string]string{
		"ctr-valkey-local": uid.LocalAgent,
		"ctr-valkey-nas":   "agent-nas",
		"ctr-valkey-web":   "agent-web",
	} {
		_, err := containerStore.InsertContainer(ctx, &container.Container{
			ExternalID: externalID, AgentID: agentID, Name: "valkey", Image: "valkey/valkey:8.0.1",
			State: container.StateRunning, AlertSeverity: container.SeverityWarning, RestartThreshold: 3,
			RuntimeType: "docker", FirstSeenAt: now, LastStateChangeAt: now,
		})
		require.NoError(t, err)
		_, err = updateStore.InsertImageUpdate(ctx, &update.ImageUpdate{
			ScanID: scanID, ContainerID: externalID, AgentID: agentID, ContainerName: "valkey",
			Image: "valkey/valkey:8.0.1", CurrentTag: "8.0.1", Registry: "registry-1.docker.io", LatestTag: "8.1.3",
			UpdateType: update.UpdateTypeMinor, RiskScore: update.BaseRiskScore(update.UpdateTypeMinor),
			Status: update.StatusAvailable, DetectedAt: now,
		})
		require.NoError(t, err)
	}

	svc := &Services{
		Updates: update.NewService(update.Deps{
			Store:      updateStore,
			Scanner:    update.NewScanner(update.NewRegistryClient(), updateStore, logger),
			Containers: noContainers{},
			Logger:     logger,
		}),
		Agents: agentStore,
		Logger: logger,
	}

	result, _, err := listRiskScoresHandler(svc)(ctx, nil, listRiskScoresInput{})
	require.NoError(t, err)
	require.False(t, result.IsError)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(textFromContent(t, result.Content)), &rows))
	got := map[string][2]any{}
	for _, row := range rows {
		got[row["container_id"].(string)] = [2]any{row["agent_id"], row["agent_label"]}
	}
	assert.Equal(t, map[string][2]any{
		"ctr-valkey-local": {uid.LocalAgent, nil},
		"ctr-valkey-nas":   {"agent-nas", "NAS cave"},
		"ctr-valkey-web":   {"agent-web", "web-07"},
	}, got)
}
