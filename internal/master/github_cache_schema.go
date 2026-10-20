package master

const githubCacheSchema = `
INSERT OR IGNORE INTO meta(key,value) VALUES('github_cache_revision','1'),('github_cache_config','{}');
CREATE TABLE IF NOT EXISTS github_cache_objects(
 key TEXT PRIMARY KEY,route TEXT NOT NULL,ref BLOB NOT NULL,header BLOB NOT NULL DEFAULT '{}',
 generation INTEGER NOT NULL DEFAULT 1,epoch INTEGER NOT NULL DEFAULT 0,header_revision TEXT NOT NULL DEFAULT '',
 due TEXT NOT NULL DEFAULT '',reason TEXT NOT NULL DEFAULT '',retry TEXT NOT NULL DEFAULT '',updated TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS github_cache_due ON github_cache_objects(route,due,key);
CREATE TABLE IF NOT EXISTS github_cache_datasets(
 object_key TEXT NOT NULL REFERENCES github_cache_objects(key) ON DELETE CASCADE,kind TEXT NOT NULL,sha TEXT NOT NULL,
 ref BLOB NOT NULL,data BLOB NOT NULL,created TEXT NOT NULL,PRIMARY KEY(object_key,kind,sha));
CREATE TABLE IF NOT EXISTS github_cache_current(
 object_key TEXT NOT NULL REFERENCES github_cache_objects(key) ON DELETE CASCADE,kind TEXT NOT NULL,sha TEXT NOT NULL,PRIMARY KEY(object_key,kind));
CREATE TABLE IF NOT EXISTS github_cache_leases(
 id TEXT PRIMARY KEY,object_key TEXT NOT NULL REFERENCES github_cache_objects(key) ON DELETE CASCADE,
 actor TEXT NOT NULL,request_id TEXT NOT NULL,started TEXT NOT NULL,expires TEXT NOT NULL,body BLOB NOT NULL,result TEXT NOT NULL DEFAULT '',UNIQUE(actor,request_id));
CREATE INDEX IF NOT EXISTS github_cache_lease_object ON github_cache_leases(object_key,expires);
CREATE TABLE IF NOT EXISTS github_cache_chunks(
 lease TEXT NOT NULL REFERENCES github_cache_leases(id) ON DELETE CASCADE,kind TEXT NOT NULL,offset INTEGER NOT NULL,
 total INTEGER NOT NULL,sha TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(lease,kind,offset));
CREATE TABLE IF NOT EXISTS github_cache_hints(actor TEXT NOT NULL,request TEXT NOT NULL,object_key TEXT NOT NULL,PRIMARY KEY(actor,request));
`
