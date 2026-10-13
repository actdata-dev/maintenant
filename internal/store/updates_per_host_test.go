// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kolapsis/maintenant/internal/container"
	"github.com/kolapsis/maintenant/internal/uid"
	"github.com/kolapsis/maintenant/internal/update"
)

type hostedContainer struct {
	agentID    string
	externalID string
}

func seedHostedContainer(t *testing.T, cs *ContainerStore, agentID, externalID, name, image string) string {
	t.Helper()
	now := time.Now()
	id, err := cs.InsertContainer(context.Background(), &container.Container{
		ExternalID:        externalID,
		AgentID:           agentID,
		Name:              name,
		Image:             image,
		State:             container.StateRunning,
		AlertSeverity:     container.SeverityWarning,
		RestartThreshold:  3,
		RuntimeType:       "docker",
		FirstSeenAt:       now,
		LastStateChangeAt: now,
	})
	require.NoError(t, err)
	return id
}

func seedHostedUpdate(t *testing.T, us *UpdateStore, scanID, agentID, externalID, name, image, current, latest string) {
	t.Helper()
	_, err := us.InsertImageUpdate(context.Background(), &update.ImageUpdate{
		ScanID:        scanID,
		ContainerID:   externalID,
		AgentID:       agentID,
		ContainerName: name,
		Image:         image + ":" + current,
		CurrentTag:    current,
		Registry:      "registry-1.docker.io",
		LatestTag:     latest,
		UpdateType:    update.UpdateTypeMinor,
		RiskScore:     update.BaseRiskScore(update.UpdateTypeMinor),
		Status:        update.StatusAvailable,
		DetectedAt:    time.Now(),
	})
	require.NoError(t, err)
}

func agentsByContainer(entries []*update.ImageUpdate, name string) map[string]string {
	out := map[string]string{}
	for _, u := range entries {
		if u.ContainerName == name {
			out[u.ContainerID] = u.AgentID
		}
	}
	return out
}

// FR-001, US1 scenario 1, SC-001: the same container, image and target version on
// several hosts gives one entry per host, each carrying its host.
func TestImageUpdates_OneEntryPerHost(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cs := NewContainerStore(db)
	us := NewUpdateStore(db)
	seedAgent(t, db, "agent-nas")
	seedAgent(t, db, "agent-web")

	valkey := []hostedContainer{
		{uid.LocalAgent, "ctr-valkey-local"},
		{"agent-nas", "ctr-valkey-nas"},
		{"agent-web", "ctr-valkey-web"},
	}
	redis := []hostedContainer{
		{"agent-web", "ctr-redis-web"},
		{"agent-nas", "ctr-redis-nas"},
	}
	for _, h := range valkey {
		seedHostedContainer(t, cs, h.agentID, h.externalID, "valkey", "valkey/valkey:8.0.1")
	}
	for _, h := range redis {
		seedHostedContainer(t, cs, h.agentID, h.externalID, "redis", "redis:7.2.4")
	}

	firstScan := seedScan(t, us)
	for _, h := range valkey {
		seedHostedUpdate(t, us, firstScan, h.agentID, h.externalID, "valkey", "valkey/valkey", "8.0.1", "8.1.3")
	}
	for _, h := range redis {
		seedHostedUpdate(t, us, firstScan, h.agentID, h.externalID, "redis", "redis", "7.2.4", "7.4.1")
	}

	wantValkey := map[string]string{
		"ctr-valkey-local": uid.LocalAgent,
		"ctr-valkey-nas":   "agent-nas",
		"ctr-valkey-web":   "agent-web",
	}
	wantRedis := map[string]string{
		"ctr-redis-web": "agent-web",
		"ctr-redis-nas": "agent-nas",
	}

	listed, err := us.ListImageUpdates(ctx, update.ListImageUpdatesOpts{})
	require.NoError(t, err)
	require.Len(t, listed, 5, "three valkey hosts and two redis hosts, one entry each")
	assert.Equal(t, wantValkey, agentsByContainer(listed, "valkey"))
	assert.Equal(t, wantRedis, agentsByContainer(listed, "redis"))

	for _, u := range listed {
		got, err := us.GetImageUpdate(ctx, u.ID)
		require.NoError(t, err)
		require.NotNil(t, got, u.ContainerID)
		assert.Equal(t, u.AgentID, got.AgentID, "GetImageUpdate must read the host of %s", u.ContainerID)

		byContainer, err := us.GetImageUpdateByContainer(ctx, u.ContainerID)
		require.NoError(t, err)
		require.NotNil(t, byContainer, "each host keeps its own entry, %s included", u.ContainerID)
		assert.Equal(t, u.AgentID, byContainer.AgentID)
	}

	// A later scan rewrites each host's entry in place instead of adding a second one.
	secondScan := seedScan(t, us)
	for _, h := range valkey {
		seedHostedUpdate(t, us, secondScan, h.agentID, h.externalID, "valkey", "valkey/valkey", "8.0.1", "8.1.3")
	}
	listed, err = us.ListImageUpdates(ctx, update.ListImageUpdatesOpts{})
	require.NoError(t, err)
	assert.Len(t, listed, 5, "a rescan must not duplicate a host's entry")
	assert.Equal(t, wantValkey, agentsByContainer(listed, "valkey"))
}

// FR-003, US1 scenario 3, SC-003: once a host runs the new version, the next
// scan's cleanup drops that host's entry and keeps the others.
func TestImageUpdates_UpgradeOnOneHostRemovesOnlyItsEntry(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cs := NewContainerStore(db)
	us := NewUpdateStore(db)
	seedAgent(t, db, "agent-nas")
	seedAgent(t, db, "agent-web")

	hosts := []hostedContainer{
		{uid.LocalAgent, "ctr-valkey-local"},
		{"agent-nas", "ctr-valkey-nas"},
		{"agent-web", "ctr-valkey-web"},
	}
	for _, h := range hosts {
		seedHostedContainer(t, cs, h.agentID, h.externalID, "valkey", "valkey/valkey:8.0.1")
	}

	firstScan := seedScan(t, us)
	for _, h := range hosts {
		seedHostedUpdate(t, us, firstScan, h.agentID, h.externalID, "valkey", "valkey/valkey", "8.0.1", "8.1.3")
	}
	listed, err := us.ListImageUpdates(ctx, update.ListImageUpdatesOpts{})
	require.NoError(t, err)
	require.Len(t, listed, 3, "precondition: one entry per host")

	// valkey was upgraded on the NAS: the next scan only finds the two others.
	secondScan := seedScan(t, us)
	for _, h := range hosts {
		if h.agentID == "agent-nas" {
			continue
		}
		seedHostedUpdate(t, us, secondScan, h.agentID, h.externalID, "valkey", "valkey/valkey", "8.0.1", "8.1.3")
	}

	// valkey still runs on the NAS (upgraded), so the scan covers all three hosts.
	covered := []string{uid.LocalAgent, "agent-nas", "agent-web"}
	stale, err := us.ListStaleImageUpdates(ctx, secondScan, covered, []string{"valkey"})
	require.NoError(t, err)
	require.Len(t, stale, 1, "only the upgraded host's entry is stale")
	assert.Equal(t, "ctr-valkey-nas", stale[0].ContainerID)
	assert.Equal(t, "valkey", stale[0].ContainerName)
	assert.Equal(t, "agent-nas", stale[0].AgentID, "the stale entry names its host")

	deleted, err := us.DeleteStaleImageUpdates(ctx, secondScan, covered, []string{"valkey"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	listed, err = us.ListImageUpdates(ctx, update.ListImageUpdatesOpts{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"ctr-valkey-local": uid.LocalAgent,
		"ctr-valkey-web":   "agent-web",
	}, agentsByContainer(listed, "valkey"))
}

// Contract (update.resolved keeps the entry's host even when its container is
// gone): an orphan finding reads its host from its own row.
func TestImageUpdates_OrphanEntryCarriesItsHost(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cs := NewContainerStore(db)
	us := NewUpdateStore(db)
	seedAgent(t, db, "agent-web")

	goneUID := seedHostedContainer(t, cs, "agent-web", "ctr-valkey-web", "valkey", "valkey/valkey:8.0.1")
	seedHostedContainer(t, cs, uid.LocalAgent, "ctr-valkey-local", "valkey", "valkey/valkey:8.0.1")

	scanID := seedScan(t, us)
	seedHostedUpdate(t, us, scanID, "agent-web", "ctr-valkey-web", "valkey", "valkey/valkey", "8.0.1", "8.1.3")
	seedHostedUpdate(t, us, scanID, uid.LocalAgent, "ctr-valkey-local", "valkey", "valkey/valkey", "8.0.1", "8.1.3")
	require.NoError(t, cs.ArchiveContainer(ctx, goneUID, time.Now()))

	orphans, err := us.ListOrphanImageUpdates(ctx)
	require.NoError(t, err)
	require.Len(t, orphans, 1)
	assert.Equal(t, "ctr-valkey-web", orphans[0].ContainerID)
	assert.Equal(t, "agent-web", orphans[0].AgentID)
	assert.Equal(t, goneUID, orphans[0].ContainerUID)
}

// Edge case "Compteurs": three hosts waiting for the same valkey update count for three.
func TestUpdateSummary_CountsOneUpdatePerHost(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cs := NewContainerStore(db)
	us := NewUpdateStore(db)
	seedAgent(t, db, "agent-nas")
	seedAgent(t, db, "agent-web")

	hosts := []hostedContainer{
		{uid.LocalAgent, "ctr-valkey-local"},
		{"agent-nas", "ctr-valkey-nas"},
		{"agent-web", "ctr-valkey-web"},
	}
	scanID := seedScan(t, us)
	for _, h := range hosts {
		seedHostedContainer(t, cs, h.agentID, h.externalID, "valkey", "valkey/valkey:8.0.1")
		seedHostedUpdate(t, us, scanID, h.agentID, h.externalID, "valkey", "valkey/valkey", "8.0.1", "8.1.3")
	}

	summary, err := us.GetUpdateSummary(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, summary.Critical+summary.Recommended+summary.Available, "one pending update per host")
	assert.Equal(t, 0, summary.UpToDate, "none of the three running containers is up to date")
}
