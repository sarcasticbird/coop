package master

const schema = `
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS principals(id TEXT PRIMARY KEY,kind TEXT NOT NULL,alias TEXT UNIQUE NOT NULL,body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS placements(agent_id TEXT PRIMARY KEY REFERENCES principals(id),body BLOB NOT NULL,last_seen TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'offline',reason TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS agent_diagnostics(agent_id TEXT PRIMARY KEY REFERENCES principals(id),body BLOB NOT NULL,received_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS threads(id TEXT PRIMARY KEY,engagement TEXT NOT NULL,body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS members(thread_id TEXT REFERENCES threads(id),principal_id TEXT REFERENCES principals(id),PRIMARY KEY(thread_id,principal_id));
CREATE TABLE IF NOT EXISTS messages(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT UNIQUE NOT NULL,thread_id TEXT NOT NULL REFERENCES threads(id),author_id TEXT NOT NULL REFERENCES principals(id),body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS deliveries(id TEXT PRIMARY KEY,message_id TEXT NOT NULL REFERENCES messages(id),agent_id TEXT NOT NULL REFERENCES principals(id),state TEXT NOT NULL,body BLOB NOT NULL,UNIQUE(message_id,agent_id));
CREATE TABLE IF NOT EXISTS requests(actor TEXT NOT NULL REFERENCES principals(id),request_id TEXT NOT NULL,digest TEXT NOT NULL,kind TEXT NOT NULL,result BLOB NOT NULL,PRIMARY KEY(actor,request_id));
CREATE INDEX IF NOT EXISTS task_creation_native_key ON requests(json_extract(result,'$.project_uid'),json_extract(result,'$.provider_key')) WHERE kind='task-create';
CREATE INDEX IF NOT EXISTS task_creation_native_issue ON requests(json_extract(result,'$.project_uid'),json_extract(result,'$.observed_issue_uid')) WHERE kind='task-create';
CREATE TABLE IF NOT EXISTS events(seq INTEGER PRIMARY KEY AUTOINCREMENT,kind TEXT NOT NULL,thread_id TEXT NOT NULL REFERENCES threads(id),message_id TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS messages_thread ON messages(thread_id,seq);
CREATE INDEX IF NOT EXISTS deliveries_pending ON deliveries(agent_id,state,id);
CREATE INDEX IF NOT EXISTS events_thread ON events(thread_id,seq);
CREATE TABLE IF NOT EXISTS goals(id TEXT PRIMARY KEY,thread_id TEXT NOT NULL REFERENCES threads(id),state TEXT NOT NULL,active_sha TEXT NOT NULL DEFAULT '',decision_seq INTEGER NOT NULL DEFAULT 0,approval_seq INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS goal_revisions(goal_id TEXT NOT NULL REFERENCES goals(id),sha TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(goal_id,sha));
CREATE TABLE IF NOT EXISTS goal_decisions(goal_id TEXT NOT NULL REFERENCES goals(id),seq INTEGER NOT NULL,actor TEXT NOT NULL REFERENCES principals(id),body BLOB NOT NULL,PRIMARY KEY(goal_id,seq));
CREATE TABLE IF NOT EXISTS artifacts(goal_id TEXT NOT NULL,revision TEXT NOT NULL,url TEXT NOT NULL,kind TEXT NOT NULL,assignment_sha TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(goal_id,revision,url),FOREIGN KEY(goal_id,revision) REFERENCES goal_revisions(goal_id,sha));
CREATE TABLE IF NOT EXISTS handoffs(id TEXT PRIMARY KEY,goal_id TEXT NOT NULL,revision TEXT NOT NULL,release_key TEXT NOT NULL DEFAULT '',body BLOB NOT NULL,FOREIGN KEY(goal_id,revision) REFERENCES goal_revisions(goal_id,sha));
CREATE INDEX IF NOT EXISTS handoffs_goal ON handoffs(goal_id,revision,id);
CREATE UNIQUE INDEX IF NOT EXISTS handoffs_release ON handoffs(release_key) WHERE release_key<>'';
`

const recoverySchema = `
CREATE TABLE IF NOT EXISTS placement_transitions(agent TEXT NOT NULL,generation INTEGER NOT NULL,body BLOB NOT NULL,PRIMARY KEY(agent,generation));
CREATE TABLE IF NOT EXISTS recovery_evidence(epoch INTEGER NOT NULL,agent TEXT NOT NULL,id TEXT NOT NULL,body BLOB NOT NULL,resolution BLOB,PRIMARY KEY(epoch,agent,id));
CREATE TABLE IF NOT EXISTS recovery_guests(epoch INTEGER NOT NULL,agent TEXT NOT NULL,placement BLOB NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(epoch,agent));
CREATE TABLE IF NOT EXISTS history_imports(thread_id TEXT NOT NULL REFERENCES threads(id),digest TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(thread_id,digest));
CREATE TABLE IF NOT EXISTS source_cutovers(thread_id TEXT PRIMARY KEY REFERENCES threads(id),body BLOB NOT NULL);
`
