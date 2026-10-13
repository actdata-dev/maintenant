// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kolapsis/maintenant/internal/uid"
	"github.com/kolapsis/maintenant/internal/update"
)

// FR-005, edge case "Mise à niveau": entries recorded before the upgrade stay
// visible, attached to the host of the container they name, and the first scan
// after the upgrade adds the other hosts next to them.
func TestMigration41_ExistingUpdatesKeepTheHostOfTheirContainer(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cs := NewContainerStore(db)
	us := NewUpdateStore(db)
	seedAgent(t, db, "agent-nas")

	apply := func(direction string) {
		t.Helper()
		matches, err := fs.Glob(migrationFS, "migrations/"+db.dialect.String()+"/41_*."+direction+".sql")
		require.NoError(t, err)
		require.Len(t, matches, 1, "migration 41 (%s) must exist for %s", direction, db.dialect.String())
		sqlText, err := fs.ReadFile(migrationFS, matches[0])
		require.NoError(t, err)
		_, err = db.ReadDB().ExecContext(ctx, string(sqlText))
		require.NoError(t, err, direction)
	}

	seedHostedContainer(t, cs, "agent-nas", "ctr-valkey-nas", "valkey", "valkey/valkey:8.0.1")
	seedHostedContainer(t, cs, uid.LocalAgent, "ctr-valkey-local", "valkey", "valkey/valkey:8.0.1")
	seedHostedContainer(t, cs, uid.LocalAgent, "ctr-pg-local", "pg-main", "postgres:16.1")

	apply("down")
	insertLegacy := func(containerID, name, image, current, latest string) {
		t.Helper()
		_, err := db.ReadDB().ExecContext(ctx, db.dialect.Rebind(
			`INSERT INTO image_updates (id, container_id, container_name, image, current_tag, current_digest,
				registry, latest_tag, update_type, risk_score, status, detected_at)
			VALUES (?, ?, ?, ?, ?, '', 'registry-1.docker.io', ?, 'minor', 50, 'available', ?)`),
			uid.New(), containerID, name, image+":"+current, current, latest, time.Now().Unix())
		require.NoError(t, err)
	}
	// The merged valkey entry points at the NAS container, the last host scanned.
	insertLegacy("ctr-valkey-nas", "valkey", "valkey/valkey", "8.0.1", "8.1.3")
	insertLegacy("ctr-pg-local", "pg-main", "postgres", "16.1", "16.4")
	insertLegacy("ctr-vanished", "old-job", "busybox", "1.36.0", "1.37.0")
	apply("up")

	var vanishedAgent string
	require.NoError(t, db.ReadDB().QueryRowContext(ctx, db.dialect.Rebind(
		`SELECT agent_id FROM image_updates WHERE container_id = ?`), "ctr-vanished").Scan(&vanishedAgent))
	assert.Equal(t, uid.LocalAgent, vanishedAgent, "an entry whose container is unknown falls back to the local runtime")

	listed, err := us.ListImageUpdates(ctx, update.ListImageUpdatesOpts{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"ctr-valkey-nas": "agent-nas"}, agentsByContainer(listed, "valkey"),
		"the merged entry stays visible, attached to the host of its container")
	assert.Equal(t, map[string]string{"ctr-pg-local": uid.LocalAgent}, agentsByContainer(listed, "pg-main"))

	// First scan after the upgrade: the local valkey gets its own entry and the NAS
	// entry is rewritten in place.
	scanID := seedScan(t, us)
	seedHostedUpdate(t, us, scanID, "agent-nas", "ctr-valkey-nas", "valkey", "valkey/valkey", "8.0.1", "8.1.3")
	seedHostedUpdate(t, us, scanID, uid.LocalAgent, "ctr-valkey-local", "valkey", "valkey/valkey", "8.0.1", "8.1.3")

	listed, err = us.ListImageUpdates(ctx, update.ListImageUpdatesOpts{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"ctr-valkey-nas":   "agent-nas",
		"ctr-valkey-local": uid.LocalAgent,
	}, agentsByContainer(listed, "valkey"))
	assert.Len(t, listed, 3)
}
