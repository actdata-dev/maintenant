// Copyright 2026 Benjamin Touchard (kOlapsis)
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kolapsis/maintenant/internal/agent"
	"github.com/kolapsis/maintenant/internal/container"
	"github.com/kolapsis/maintenant/internal/store"
	"github.com/kolapsis/maintenant/internal/store/storetest"
	"github.com/kolapsis/maintenant/internal/uid"
	"github.com/kolapsis/maintenant/internal/update"
)

// FR-006, contract "Outil MCP get_updates": each entry carries its agent_id (the
// local sentinel for the server's runtime) and, for a known agent, agent_label
// (label, else hostname).
func TestGetUpdates_CarriesTheHostOfEachEntry(t *testing.T) {
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
	for _, c := range []struct{ agentID, externalID string }{
		{uid.LocalAgent, "ctr-valkey-local"},
		{"agent-nas", "ctr-valkey-nas"},
		{"agent-web", "ctr-valkey-web"},
	} {
		_, err := containerStore.InsertContainer(ctx, &container.Container{
			ExternalID:        c.externalID,
			AgentID:           c.agentID,
			Name:              "valkey",
			Image:             "valkey/valkey:8.0.1",
			State:             container.StateRunning,
			AlertSeverity:     container.SeverityWarning,
			RestartThreshold:  3,
			RuntimeType:       "docker",
			FirstSeenAt:       now,
			LastStateChangeAt: now,
		})
		require.NoError(t, err)
		_, err = updateStore.InsertImageUpdate(ctx, &update.ImageUpdate{
			ScanID:        scanID,
			ContainerID:   c.externalID,
			AgentID:       c.agentID,
			ContainerName: "valkey",
			Image:         "valkey/valkey:8.0.1",
			CurrentTag:    "8.0.1",
			Registry:      "registry-1.docker.io",
			LatestTag:     "8.1.3",
			UpdateType:    update.UpdateTypeMinor,
			RiskScore:     update.BaseRiskScore(update.UpdateTypeMinor),
			Status:        update.StatusAvailable,
			DetectedAt:    now,
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

	result, _, err := getUpdatesHandler(svc)(ctx, nil, getUpdatesInput{})
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*gomcp.TextContent)
	require.True(t, ok)

	var payload struct {
		Updates []map[string]any `json:"updates"`
	}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))
	require.Len(t, payload.Updates, 3, "one entry per host")

	byContainer := map[string]map[string]any{}
	for _, u := range payload.Updates {
		id, _ := u["container_id"].(string)
		byContainer[id] = u
	}

	require.Contains(t, byContainer, "ctr-valkey-nas")
	assert.Equal(t, "agent-nas", byContainer["ctr-valkey-nas"]["agent_id"])
	assert.Equal(t, "NAS cave", byContainer["ctr-valkey-nas"]["agent_label"])

	require.Contains(t, byContainer, "ctr-valkey-web")
	assert.Equal(t, "agent-web", byContainer["ctr-valkey-web"]["agent_id"])
	assert.Equal(t, "web-07", byContainer["ctr-valkey-web"]["agent_label"], "no label: the hostname stands in")

	require.Contains(t, byContainer, "ctr-valkey-local")
	assert.Equal(t, uid.LocalAgent, byContainer["ctr-valkey-local"]["agent_id"])
}
