DROP INDEX uq_image_updates_agent_name_image_tag;

DELETE FROM image_updates WHERE id NOT IN (
    SELECT MIN(id) FROM image_updates GROUP BY container_name, image, latest_tag);

ALTER TABLE image_updates DROP COLUMN agent_id;
CREATE UNIQUE INDEX uq_image_updates_name_image_tag ON image_updates(container_name, image, latest_tag);
