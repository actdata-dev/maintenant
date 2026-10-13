// Copyright 2026 Benjamin Touchard (kOlapsis)
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kolapsis/maintenant/internal/event"
	"github.com/kolapsis/maintenant/internal/uid"
)

// insertRecordingStore keeps a copy of every update a scan persists.
type insertRecordingStore struct {
	*stubStore
	inserted []ImageUpdate
}

func (s *insertRecordingStore) InsertImageUpdate(_ context.Context, u *ImageUpdate) (string, error) {
	s.inserted = append(s.inserted, *u)
	return "", nil
}

// hostPendingStore holds the updates earlier scans left pending, several hosts
// per container name, and drops them by host and name like the real store does.
type hostPendingStore struct {
	*stubStore
	pending []StaleImageUpdate
}

func (s *hostPendingStore) ListStaleImageUpdates(_ context.Context, _ string, agentIDs, names []string) ([]StaleImageUpdate, error) {
	var stale []StaleImageUpdate
	for _, su := range s.pending {
		if slices.Contains(agentIDs, su.AgentID) && slices.Contains(names, su.ContainerName) {
			stale = append(stale, su)
		}
	}
	return stale, nil
}

func (s *hostPendingStore) DeleteStaleImageUpdates(_ context.Context, _ string, agentIDs, names []string) (int64, error) {
	before := len(s.pending)
	s.pending = slices.DeleteFunc(s.pending, func(su StaleImageUpdate) bool {
		return slices.Contains(agentIDs, su.AgentID) && slices.Contains(names, su.ContainerName)
	})
	return int64(before - len(s.pending)), nil
}

func resolvedAgents(t *testing.T, events *[]map[string]interface{}) EventCallback {
	return func(eventType string, data interface{}) {
		if eventType != event.UpdateResolved {
			return
		}
		m, ok := data.(map[string]interface{})
		require.True(t, ok)
		*events = append(*events, m)
	}
}

// FR-001: the scan hands each update to the store with the host of its container.
func TestRunScan_PersistsTheHostOfEachUpdate(t *testing.T) {
	store := &insertRecordingStore{stubStore: &stubStore{}}
	reg := &stubRegistry{tags: map[string][]string{
		"valkey/valkey": {"8.0.1", "8.1.3"},
		"library/redis": {"7.2.4", "7.4.1"},
	}}
	svc := NewService(Deps{
		Store:   store,
		Scanner: newTestScanner(reg, store),
		Containers: stubLister{containers: []ContainerInfo{
			{UID: "uid-valkey-local", AgentID: uid.LocalAgent, ExternalID: "ctr-valkey-local", Name: "valkey", Image: "valkey/valkey:8.0.1"},
			{UID: "uid-valkey-nas", AgentID: "agent-nas", ExternalID: "ctr-valkey-nas", Name: "valkey", Image: "valkey/valkey:8.0.1"},
			{UID: "uid-valkey-web", AgentID: "agent-web", ExternalID: "ctr-valkey-web", Name: "valkey", Image: "valkey/valkey:8.0.1"},
			{UID: "uid-redis-web", AgentID: "agent-web", ExternalID: "ctr-redis-web", Name: "redis", Image: "redis:7.2.4"},
		}},
		Logger: testLogger(),
	})

	svc.runScan(context.Background())

	got := map[string]string{}
	for _, u := range store.inserted {
		got[u.ContainerID] = u.AgentID
	}
	assert.Equal(t, map[string]string{
		"ctr-valkey-local": uid.LocalAgent,
		"ctr-valkey-nas":   "agent-nas",
		"ctr-valkey-web":   "agent-web",
		"ctr-redis-web":    "agent-web",
	}, got)
}

// Contract update.resolved: AgentID is the entry's, including for an entry whose
// container is gone from the scan set.
func TestRunScan_ResolvedOrphanKeepsTheHostOfItsEntry(t *testing.T) {
	store := &orphanStore{
		stubStore: &stubStore{},
		orphans: []StaleImageUpdate{{
			ContainerID:   "ctr-valkey-old",
			ContainerName: "valkey",
			AgentID:       "agent-nas",
			ContainerUID:  "uid-valkey-old",
		}},
	}

	var resolved []map[string]interface{}
	svc := newOrphanTestService(store, []ContainerInfo{{
		UID:        "uid-valkey-web",
		AgentID:    "agent-web",
		ExternalID: "ctr-valkey-web",
		Name:       "valkey",
		Image:      "valkey/valkey:8.1.3",
	}}, resolvedAgents(t, &resolved))

	svc.runScan(context.Background())

	require.Len(t, resolved, 1)
	assert.Equal(t, "ctr-valkey-old", resolved[0]["container_id"])
	assert.Equal(t, "uid-valkey-old", resolved[0]["container_uid"])
	assert.Equal(t, "agent-nas", resolved[0]["agent_id"], "the vanished container's host, not an empty one")
}

// FR-009, data-model rule "uid du conteneur d'une entrée": an orphan whose
// container row is gone (no uid from the store) still resolves the alert of its
// host, by the uid derived from (agent_id, container_id).
func TestRunScan_ResolvedOrphanWithoutContainerRowDerivesItsUID(t *testing.T) {
	for _, tc := range []struct {
		agentID, externalID, name string
	}{
		{"agent-nas", "ctr-valkey-nas-old", "valkey"},
		{uid.LocalAgent, "ctr-immich-old", "immich_server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &orphanStore{
				stubStore: &stubStore{},
				orphans: []StaleImageUpdate{{
					ContainerID:   tc.externalID,
					ContainerName: tc.name,
					AgentID:       tc.agentID,
				}},
			}

			var resolved []map[string]interface{}
			svc := newOrphanTestService(store, []ContainerInfo{{
				UID:        "uid-other",
				AgentID:    "agent-web",
				ExternalID: "ctr-other",
				Name:       "other",
				Image:      "acme/other:1.0.0",
			}}, resolvedAgents(t, &resolved))

			svc.runScan(context.Background())

			require.Len(t, resolved, 1)
			assert.Equal(t, tc.externalID, resolved[0]["container_id"])
			assert.Equal(t, uid.Container(tc.agentID, tc.externalID), resolved[0]["container_uid"])
			assert.Equal(t, tc.agentID, resolved[0]["agent_id"])
		})
	}
}

// US1 scenario 6, US2 scenario 5, FR-009, contract "Résolution": the host
// recreated its container under a new id (docker compose up -d), so the stale
// entry's container is no longer in the scan set. The recovery event carries the
// host of the entry and the uid of the old container, the one the alert holds.
func TestRunScan_ResolvedStaleEntryKeepsItsHost(t *testing.T) {
	for _, tc := range []struct {
		agentID, name, image, oldID, newID string
	}{
		{"agent-nas", "valkey", "valkey/valkey:8.1.3", "ctr-valkey-nas-old", "ctr-valkey-nas-new"},
		{uid.LocalAgent, "immich_server", "ghcr.io/immich-app/immich-server:1.120.0", "ctr-immich-old", "ctr-immich-new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &hostPendingStore{stubStore: &stubStore{}, pending: []StaleImageUpdate{
				{ContainerID: tc.oldID, ContainerName: tc.name, AgentID: tc.agentID},
			}}

			var resolved []map[string]interface{}
			svc := NewService(Deps{
				Store:   store,
				Scanner: newTestScanner(&stubRegistry{}, store),
				Containers: stubLister{containers: []ContainerInfo{{
					UID:        uid.Container(tc.agentID, tc.newID),
					AgentID:    tc.agentID,
					ExternalID: tc.newID,
					Name:       tc.name,
					Image:      tc.image,
				}}},
				Logger:        testLogger(),
				EventCallback: resolvedAgents(t, &resolved),
			})

			svc.runScan(context.Background())

			require.Len(t, resolved, 1)
			assert.Equal(t, tc.oldID, resolved[0]["container_id"])
			assert.Equal(t, tc.name, resolved[0]["container_name"])
			assert.Equal(t, tc.agentID, resolved[0]["agent_id"])
			assert.Equal(t, uid.Container(tc.agentID, tc.oldID), resolved[0]["container_uid"],
				"the alert is keyed on the old container's uid")
			assert.Empty(t, store.pending, "the recreated container's entry is dropped")
		})
	}
}

// US1 scenario 5, FR-003, edge case "Hôte absent du scan", contract "Résolution":
// a host whose containers are missing from the scan (agent not back yet after a
// server restart) keeps its entry and its alert, even when the same name is up
// to date on another host. A name whose hosts were all scanned is still cleaned.
func TestRunScan_AbsentHostKeepsItsPendingUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, image, absentAgent, presentAgent string
	}{
		{"valkey", "valkey/valkey:8.1.3", "agent-nas", "agent-web"},
		{"immich_redis", "redis:7.4.1", "agent-edge", uid.LocalAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			absent := StaleImageUpdate{ContainerID: "ctr-" + tc.name + "-absent", ContainerName: tc.name, AgentID: tc.absentAgent}
			store := &hostPendingStore{stubStore: &stubStore{}, pending: []StaleImageUpdate{
				absent,
				{ContainerID: "ctr-" + tc.name + "-present", ContainerName: tc.name, AgentID: tc.presentAgent},
				{ContainerID: "ctr-pg-main", ContainerName: "pg-main", AgentID: tc.presentAgent},
			}}

			var resolved []map[string]interface{}
			svc := NewService(Deps{
				Store:   store,
				Scanner: newTestScanner(&stubRegistry{}, store),
				Containers: stubLister{containers: []ContainerInfo{
					{
						UID:        uid.Container(tc.presentAgent, "ctr-"+tc.name+"-present"),
						AgentID:    tc.presentAgent,
						ExternalID: "ctr-" + tc.name + "-present",
						Name:       tc.name,
						Image:      tc.image,
					},
					{
						UID:        uid.Container(tc.presentAgent, "ctr-pg-main"),
						AgentID:    tc.presentAgent,
						ExternalID: "ctr-pg-main",
						Name:       "pg-main",
						Image:      "postgres:16.4",
					},
				}},
				Logger:        testLogger(),
				EventCallback: resolvedAgents(t, &resolved),
			})

			svc.runScan(context.Background())

			for _, m := range resolved {
				assert.NotEqual(t, absent.ContainerID, m["container_id"], "the absent host's update must not be resolved")
			}
			assert.Contains(t, store.pending, absent, "the absent host keeps its entry")

			var resolvedIDs []string
			for _, m := range resolved {
				id, _ := m["container_id"].(string)
				resolvedIDs = append(resolvedIDs, id)
			}
			assert.Contains(t, resolvedIDs, "ctr-pg-main", "a name scanned on every host it runs on is still cleaned")
			for _, su := range store.pending {
				assert.NotEqual(t, "pg-main", su.ContainerName, "pg-main is up to date on its only host")
			}
		})
	}
}
