// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kolapsis/maintenant/internal/endpoint"
	"github.com/kolapsis/maintenant/internal/uid"
)

func checkedAgo(agentID string, interval time.Duration, ago time.Duration) *endpoint.Endpoint {
	t := time.Now().Add(-ago)
	cfg := endpoint.DefaultConfig()
	cfg.Interval = endpoint.Duration(interval)
	return &endpoint.Endpoint{
		AgentID: agentID, Status: endpoint.StatusUp, LastCheckAt: &t, Config: cfg,
	}
}

func TestEndpointHandler_markStale_NotCheckedRecently(t *testing.T) {
	h := &EndpointHandler{sessions: stubK8sSessions{connected: map[string]bool{"agent-ON": true}}}

	cases := []struct {
		name     string
		ep       *endpoint.Endpoint
		wantStal bool
	}{
		{"local recent", checkedAgo(uid.LocalAgent, 30*time.Second, 10*time.Second), false},
		{"local 2.5 intervals", checkedAgo(uid.LocalAgent, 30*time.Second, 75*time.Second), false},
		{"local 2 min, 30s interval", checkedAgo(uid.LocalAgent, 30*time.Second, 2*time.Minute), true},
		{"empty agent 2 min", checkedAgo("", 30*time.Second, 2*time.Minute), true},
		{"1 min interval, 2 min ago is fine", checkedAgo(uid.LocalAgent, time.Minute, 2*time.Minute), false},
		{"1 min interval, 4 min ago", checkedAgo(uid.LocalAgent, time.Minute, 4*time.Minute), true},
		{"10s interval, 45s ago", checkedAgo(uid.LocalAgent, 10*time.Second, 45*time.Second), true},
		{"zero interval falls back to 30s, 80s ago", checkedAgo(uid.LocalAgent, 0, 80*time.Second), false},
		{"zero interval falls back to 30s, 2 min ago", checkedAgo(uid.LocalAgent, 0, 2*time.Minute), true},
		{"negative interval falls back to 30s, 2 min ago", checkedAgo("", -time.Second, 2*time.Minute), true},
		{"connected remote agent is not judged by the server", checkedAgo("agent-ON", 30*time.Second, 2*time.Minute), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.markStale(c.ep)
			if c.wantStal {
				require.NotNil(t, got.Stale)
				assert.True(t, *got.Stale)
				assert.Nil(t, got.AgentOffline, "not-checked is distinct from agent offline")
			} else {
				assert.Nil(t, got.Stale)
			}
		})
	}
}

func TestEndpointHandler_markStale_NeverCheckedIsNotStale(t *testing.T) {
	h := &EndpointHandler{}
	ep := &endpoint.Endpoint{AgentID: uid.LocalAgent, Config: endpoint.DefaultConfig()}
	assert.Nil(t, h.markStale(ep).Stale)
}

func TestEndpointHandler_markStale_JSONShape(t *testing.T) {
	h := &EndpointHandler{sessions: stubK8sSessions{connected: map[string]bool{}}}

	b, err := json.Marshal(h.markStale(checkedAgo(uid.LocalAgent, 30*time.Second, 5*time.Minute)))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, true, m["stale"])
	assert.NotContains(t, m, "agent_offline")

	b, err = json.Marshal(h.markStale(checkedAgo("agent-OFF", 30*time.Second, time.Second)))
	require.NoError(t, err)
	m = nil
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, true, m["stale"])
	assert.Equal(t, true, m["agent_offline"])

	b, err = json.Marshal(h.markStale(checkedAgo(uid.LocalAgent, 30*time.Second, time.Second)))
	require.NoError(t, err)
	m = nil
	require.NoError(t, json.Unmarshal(b, &m))
	assert.NotContains(t, m, "stale")
}
