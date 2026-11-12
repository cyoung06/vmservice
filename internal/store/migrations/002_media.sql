-- Members' media: ISOs and disk images. The data is a DataVolume named after the id; this says whose
-- it is and what the inspector found. kind/profile/virtual_bytes are known from the file's header
-- when it's added; profile is confirmed by the inspector after the import ("" until then).
CREATE TABLE media (
  id            text PRIMARY KEY,                 -- md-xxxxxx, also the DataVolume's name
  owner         text NOT NULL REFERENCES members (owner),
  name          text NOT NULL,
  source        text NOT NULL,                    -- upload, url
  url           text NOT NULL DEFAULT '',
  kind          text NOT NULL,                    -- iso, disk
  profile       text NOT NULL DEFAULT '',
  size_bytes    bigint NOT NULL,
  virtual_bytes bigint NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX media_owner ON media (owner);

-- A machine made from media: which one and the device profile it got ('' for catalog images).
ALTER TABLE machines ADD COLUMN media text NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN profile text NOT NULL DEFAULT '';
