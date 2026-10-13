ALTER TABLE image_updates ADD COLUMN agent_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';

UPDATE image_updates SET agent_id = COALESCE(
    (SELECT c.agent_id FROM containers c
     WHERE c.external_id = image_updates.container_id
     ORDER BY c.archived LIMIT 1),
    '00000000-0000-0000-0000-000000000000');

DROP INDEX uq_image_updates_name_image_tag;
CREATE UNIQUE INDEX uq_image_updates_agent_name_image_tag ON image_updates(agent_id, container_name, image, latest_tag);
