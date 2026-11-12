-- Images (media) can be public and carry cloud-config extras (a "preset"); members can choose the
-- UID of their account on machines; admins can set per-member quotas (NULL: the default).
ALTER TABLE media ADD COLUMN public boolean NOT NULL DEFAULT false;
ALTER TABLE media ADD COLUMN cloud_config jsonb NOT NULL DEFAULT '{}';
CREATE INDEX media_public ON media (public) WHERE public;

ALTER TABLE members ADD COLUMN uid integer;
ALTER TABLE members ADD COLUMN quota_vms integer;
ALTER TABLE members ADD COLUMN quota_cpu integer;
ALTER TABLE members ADD COLUMN quota_memory_mib integer;
ALTER TABLE members ADD COLUMN quota_disk_gib integer;

-- A machine's name is its hostname, unique across everyone. Machines from before with names that
-- aren't valid (or are taken) get their id as their name.
UPDATE machines SET name = id WHERE name !~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$';
UPDATE machines m SET name = m.id WHERE EXISTS (SELECT 1 FROM machines o WHERE o.name = m.name AND o.id < m.id);
CREATE UNIQUE INDEX machines_name ON machines (name);
