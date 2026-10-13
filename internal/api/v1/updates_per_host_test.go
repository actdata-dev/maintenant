// Copyright 2026 Benjamin Touchard (kOlapsis)
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kolapsis/maintenant/internal/agent"
	"github.com/kolapsis/maintenant/internal/container"
	"github.com/kolapsis/maintenant/internal/event"
	"github.com/kolapsis/maintenant/internal/store"
	"github.com/kolapsis/maintenant/internal/store/storetest"
	"github.com/kolapsis/maintenant/internal/uid"
	"github.com/kolapsis/maintenant/internal/update"
)

var agentKeys = []string{"agent_id", "agent_hostname", "agent_label"}

// scanLister plays the runtimes: the containers each host reports at scan time.
type scanLister struct {
	mu    sync.Mutex
	infos []update.ContainerInfo
}

func (l *scanLister) ListContainerInfos(context.Context) ([]update.ContainerInfo, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.infos), nil
}

func (l *scanLister) remove(externalID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = slices.DeleteFunc(l.infos, func(c update.ContainerInfo) bool { return c.ExternalID == externalID })
}

func (l *scanLister) setImage(externalID, image string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.infos {
		if l.infos[i].ExternalID == externalID {
			l.infos[i].Image = image
		}
	}
}

func startUpdateRegistry(t *testing.T, tags map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	for repo, list := range tags {
		for _, tag := range list {
			img, err := random.Image(64, 1)
			require.NoError(t, err)
			ref, err := name.ParseReference(host + "/" + repo + ":" + tag)
			require.NoError(t, err)
			require.NoError(t, remote.Write(ref, img))
		}
	}
	return srv
}

type scannedContainer struct {
	agentID    string
	externalID string
	name       string
	repo       string
	tag        string
}

type scanFixture struct {
	handler    http.Handler
	svc        *update.Service
	lister     *scanLister
	agents     *store.AgentStore
	containers *store.ContainerStore
	registry   *httptest.Server
	host       string
	completed  chan struct{}
}

func newScanFixture(t *testing.T, agents []agent.Agent, tags map[string][]string, containers []scannedContainer) *scanFixture {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := storetest.Open(t, logger)

	agentStore := store.NewAgentStore(db)
	for i := range agents {
		require.NoError(t, agentStore.Insert(ctx, &agents[i]))
	}

	srv := startUpdateRegistry(t, tags)
	host := strings.TrimPrefix(srv.URL, "http://")

	containerStore := store.NewContainerStore(db)
	lister := &scanLister{}
	for _, c := range containers {
		runContainer(t, containerStore, lister, host, c)
	}

	completed := make(chan struct{}, 8)
	updateStore := store.NewUpdateStore(db)
	svc := update.NewService(update.Deps{
		Store:      updateStore,
		Scanner:    update.NewScanner(update.NewRegistryClient(), updateStore, logger),
		Containers: lister,
		Logger:     logger,
		EventCallback: func(eventType string, _ interface{}) {
			if eventType == event.UpdateScanCompleted {
				completed <- struct{}{}
			}
		},
	})

	containerSvc := container.NewService(container.Deps{Store: containerStore, Logger: logger})
	r := NewRouter(HandlerDeps{
		Logger:           logger,
		Containers:       containerSvc,
		UpdateSvc:        svc,
		UpdateStore:      updateStore,
		ContainerAdapter: update.NewContainerServiceAdapter(containerSvc, logger),
		AgentStore:       agentStore,
	})

	return &scanFixture{
		handler:    r.Handler(),
		svc:        svc,
		lister:     lister,
		agents:     agentStore,
		containers: containerStore,
		registry:   srv,
		host:       host,
		completed:  completed,
	}
}

// runContainer records a running container on its host and makes the runtime report it.
func runContainer(t *testing.T, cs *store.ContainerStore, lister *scanLister, host string, c scannedContainer) {
	t.Helper()
	image := host + "/" + c.repo + ":" + c.tag
	now := time.Now()
	id, err := cs.InsertContainer(context.Background(), &container.Container{
		ExternalID:        c.externalID,
		AgentID:           c.agentID,
		Name:              c.name,
		Image:             image,
		State:             container.StateRunning,
		AlertSeverity:     container.SeverityWarning,
		RestartThreshold:  3,
		RuntimeType:       "docker",
		FirstSeenAt:       now,
		LastStateChangeAt: now,
	})
	require.NoError(t, err)
	lister.mu.Lock()
	defer lister.mu.Unlock()
	lister.infos = append(lister.infos, update.ContainerInfo{
		UID:         id,
		AgentID:     c.agentID,
		ExternalID:  c.externalID,
		Name:        c.name,
		Image:       image,
		RuntimeType: "docker",
	})
}

func (f *scanFixture) do(method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// scan runs one scan through POST /api/v1/updates/scan and waits for its end.
func (f *scanFixture) scan(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return !f.svc.IsScanning() }, 10*time.Second, 10*time.Millisecond)
	rec := f.do(http.MethodPost, "/api/v1/updates/scan")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	select {
	case <-f.completed:
	case <-time.After(60 * time.Second):
		t.Fatal("the scan never completed")
	}
	require.Eventually(t, func() bool { return !f.svc.IsScanning() }, 10*time.Second, 10*time.Millisecond)
}

func (f *scanFixture) listUpdates(t *testing.T) []map[string]any {
	t.Helper()
	rec := f.do(http.MethodGet, "/api/v1/updates")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Updates []map[string]any `json:"updates"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Updates
}

func (f *scanFixture) containerUpdate(t *testing.T, externalID string) map[string]any {
	t.Helper()
	rec := f.do(http.MethodGet, "/api/v1/updates/container/"+externalID)
	require.Equal(t, http.StatusOK, rec.Code, "detail of %s: %s", externalID, rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func entriesByContainer(t *testing.T, entries []map[string]any) map[string]map[string]any {
	t.Helper()
	out := make(map[string]map[string]any, len(entries))
	for _, e := range entries {
		id, _ := e["container_id"].(string)
		_, dup := out[id]
		require.False(t, dup, "container %s listed twice", id)
		out[id] = e
	}
	return out
}

func assertNoAgentFields(t *testing.T, m map[string]any) {
	t.Helper()
	for _, k := range agentKeys {
		assert.NotContains(t, m, k, "a local entry carries no %s", k)
	}
}

// US1 scenarios 1 to 3, FR-001, FR-002, FR-003, FR-006, SC-001, SC-003 and the
// "Compteurs", "Agent renommé" and "Échec de scan" edge cases, through a real
// scan against a registry, the real store and the real router.
func TestUpdatesAPI_PerHost(t *testing.T) {
	f := newScanFixture(t,
		[]agent.Agent{
			{AgentID: "agent-nas", Hostname: "nas-02", Label: "NAS cave", Status: "active", DetectedRuntime: "docker", CreatedAt: time.Now()},
			{AgentID: "agent-web", Hostname: "web-07", Status: "active", DetectedRuntime: "docker", CreatedAt: time.Now()},
		},
		map[string][]string{"valkey/valkey": {"8.0.1", "8.1.3"}},
		[]scannedContainer{
			{uid.LocalAgent, "ctr-valkey-local", "valkey", "valkey/valkey", "8.0.1"},
			{"agent-nas", "ctr-valkey-nas", "valkey", "valkey/valkey", "8.0.1"},
			{"agent-web", "ctr-valkey-web", "valkey", "valkey/valkey", "8.0.1"},
		})

	f.scan(t)
	entries := f.listUpdates(t)

	t.Run("one_entry_per_host", func(t *testing.T) {
		require.Len(t, entries, 3, "valkey waits for 8.1.3 on three hosts")
		byID := entriesByContainer(t, entries)
		assert.ElementsMatch(t, []string{"ctr-valkey-local", "ctr-valkey-nas", "ctr-valkey-web"}, slices.Collect(maps.Keys(byID)))
		for _, e := range entries {
			assert.Equal(t, "valkey", e["container_name"])
			assert.Equal(t, "8.1.3", e["latest_tag"])
		}

		nas := byID["ctr-valkey-nas"]
		assert.Equal(t, "agent-nas", nas["agent_id"])
		assert.Equal(t, "nas-02", nas["agent_hostname"])
		assert.Equal(t, "NAS cave", nas["agent_label"])

		web := byID["ctr-valkey-web"]
		assert.Equal(t, "agent-web", web["agent_id"])
		assert.Equal(t, "web-07", web["agent_hostname"])
		assert.NotContains(t, web, "agent_label", "an empty label is left out")

		assertNoAgentFields(t, byID["ctr-valkey-local"])
	})

	t.Run("detail_names_the_host", func(t *testing.T) {
		nas := f.containerUpdate(t, "ctr-valkey-nas")
		assert.Equal(t, "ctr-valkey-nas", nas["container_id"])
		assert.Equal(t, "agent-nas", nas["agent_id"])
		assert.Equal(t, "nas-02", nas["agent_hostname"])
		assert.Equal(t, "NAS cave", nas["agent_label"])

		web := f.containerUpdate(t, "ctr-valkey-web")
		assert.Equal(t, "ctr-valkey-web", web["container_id"])
		assert.Equal(t, "agent-web", web["agent_id"])
		assert.Equal(t, "web-07", web["agent_hostname"])
		assert.NotContains(t, web, "agent_label")

		local := f.containerUpdate(t, "ctr-valkey-local")
		assert.Equal(t, "ctr-valkey-local", local["container_id"])
		assertNoAgentFields(t, local)
	})

	t.Run("summary_counts_one_update_per_host", func(t *testing.T) {
		rec := f.do(http.MethodGet, "/api/v1/updates/summary")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			Counts update.UpdateSummary `json:"counts"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, 3, body.Counts.Critical+body.Counts.Recommended+body.Counts.Available)
		assert.Equal(t, 0, body.Counts.UpToDate)
	})

	t.Run("renamed_agent_shows_its_current_name", func(t *testing.T) {
		require.NoError(t, f.agents.UpdateLabel(context.Background(), "agent-nas", "NAS grenier"))
		nas := entriesByContainer(t, f.listUpdates(t))["ctr-valkey-nas"]
		require.NotNil(t, nas)
		assert.Equal(t, "NAS grenier", nas["agent_label"])
	})

	f.lister.setImage("ctr-valkey-nas", f.host+"/valkey/valkey:8.1.3")
	f.scan(t)
	afterUpgrade := entriesByContainer(t, f.listUpdates(t))

	t.Run("upgrade_on_one_host_removes_only_its_entry", func(t *testing.T) {
		assert.ElementsMatch(t, []string{"ctr-valkey-local", "ctr-valkey-web"}, slices.Collect(maps.Keys(afterUpgrade)))
		if web, ok := afterUpgrade["ctr-valkey-web"]; assert.True(t, ok) {
			assert.Equal(t, "agent-web", web["agent_id"])
		}
	})

	f.registry.Close()
	f.scan(t)
	afterFailure := entriesByContainer(t, f.listUpdates(t))

	t.Run("registry_failure_keeps_every_host", func(t *testing.T) {
		assert.ElementsMatch(t, []string{"ctr-valkey-local", "ctr-valkey-web"}, slices.Collect(maps.Keys(afterFailure)))
	})
}

// US1 scenario 4, FR-007, SC-004: without a remote agent the entries keep the
// exact shape they had before the feature.
func TestUpdatesAPI_SingleHostResponseUnchanged(t *testing.T) {
	f := newScanFixture(t, nil,
		map[string][]string{"acme/postgres": {"16.1", "16.4"}},
		[]scannedContainer{{uid.LocalAgent, "ctr-pg-main", "pg-main", "acme/postgres", "16.1"}})

	f.scan(t)
	entries := f.listUpdates(t)

	require.Len(t, entries, 1)
	assert.Equal(t, "16.4", entries[0]["latest_tag"])
	assert.ElementsMatch(t, []string{
		"id", "container_id", "container_name", "image", "current_tag", "current_digest",
		"latest_tag", "latest_digest", "update_type", "risk_score", "status", "detected_at",
	}, slices.Collect(maps.Keys(entries[0])))

	assertNoAgentFields(t, f.containerUpdate(t, "ctr-pg-main"))
}

// US1 scenarios 5 and 6, FR-003, FR-009, edge case "Hôte absent du scan": a scan
// that misses a host leaves its entry on the page; recreating the container under
// a new id (docker compose up -d) drops the entry of that host only.
func TestUpdatesAPI_AbsentAndRecreatedHosts(t *testing.T) {
	nas := scannedContainer{"agent-nas", "ctr-valkey-nas", "valkey", "valkey/valkey", "8.0.1"}
	web := scannedContainer{"agent-web", "ctr-valkey-web", "valkey", "valkey/valkey", "8.0.1"}
	local := scannedContainer{uid.LocalAgent, "ctr-valkey-local", "valkey", "valkey/valkey", "8.0.1"}
	f := newScanFixture(t,
		[]agent.Agent{
			{AgentID: "agent-nas", Hostname: "nas-02", Label: "NAS cave", Status: "active", DetectedRuntime: "docker", CreatedAt: time.Now()},
			{AgentID: "agent-web", Hostname: "web-07", Status: "active", DetectedRuntime: "docker", CreatedAt: time.Now()},
		},
		map[string][]string{"valkey/valkey": {"8.0.1", "8.1.3"}},
		[]scannedContainer{nas, web, local})

	f.scan(t)
	require.Len(t, f.listUpdates(t), 3, "precondition: one entry per host")

	f.lister.remove(nas.externalID)
	f.lister.setImage(web.externalID, f.host+"/valkey/valkey:8.1.3")
	f.scan(t)
	afterAbsence := entriesByContainer(t, f.listUpdates(t))

	t.Run("absent_host_keeps_its_entry", func(t *testing.T) {
		if e, ok := afterAbsence[nas.externalID]; assert.True(t, ok, "the NAS was not scanned: its entry stays") {
			assert.Equal(t, "agent-nas", e["agent_id"])
			assert.Equal(t, "NAS cave", e["agent_label"])
			assert.Equal(t, "8.1.3", e["latest_tag"])
		}
		assert.Contains(t, afterAbsence, local.externalID)
	})

	require.NoError(t, f.containers.ArchiveContainer(context.Background(), uid.Container(nas.agentID, nas.externalID), time.Now()))
	runContainer(t, f.containers, f.lister, f.host, scannedContainer{"agent-nas", "ctr-valkey-nas-2", "valkey", "valkey/valkey", "8.1.3"})
	f.scan(t)
	afterRecreate := entriesByContainer(t, f.listUpdates(t))

	t.Run("recreated_container_drops_only_its_entry", func(t *testing.T) {
		assert.ElementsMatch(t, []string{local.externalID}, slices.Collect(maps.Keys(afterRecreate)),
			"the NAS and web-07 run 8.1.3, the local runtime still waits for it")
	})
}
