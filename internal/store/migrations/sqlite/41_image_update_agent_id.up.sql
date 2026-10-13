ALTER TABLE image_updates ADD COLUMN agent_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';

-- Unqualified on purpose: before the UUID conversion containers has no agent_id, and the name falls back to image_updates.agent_id.
UPDATE image_updates SET agent_id = COALESCE(
    (SELECT agent_id FROM containers
     WHERE external_id = image_updates.container_id
     ORDER BY archived LIMIT 1),
    '00000000-0000-0000-0000-000000000000');

DROP INDEX uq_image_updates_name_image_tag;
CREATE UNIQUE INDEX uq_image_updates_agent_name_image_tag ON image_updates(agent_id, container_name, image, latest_tag);
