-- Identity and rights (the machines themselves are KubeVirt objects; this says whose they are).

CREATE TABLE members (
  owner      text PRIMARY KEY,          -- auth.OwnerID: hash of issuer + sub
  username     text NOT NULL,           -- at the last sign-in, for display and admins
  signed_in_at timestamptz NOT NULL DEFAULT now()  -- last OIDC sign-in: the bastion honours keys for a session lifetime after it
);

CREATE TABLE machines (
  id         text PRIMARY KEY,          -- vm-xxxxxx, also the VirtualMachine's name
  owner      text NOT NULL REFERENCES members (owner),
  name       text NOT NULL,             -- display name
  image      text NOT NULL,
  size       text NOT NULL,
  cpu        integer NOT NULL,
  memory_mib integer NOT NULL,
  disk_gib   integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX machines_owner ON machines (owner);

CREATE TABLE ssh_keys (
  id         text PRIMARY KEY,          -- hex SHA-256 of the key: one member per key
  owner      text NOT NULL REFERENCES members (owner),
  key        text NOT NULL,             -- "<type> <base64>"
  label      text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ssh_keys_owner ON ssh_keys (owner);
