-- The repositories rec-deploy administers, and the history of what it deployed.

CREATE TABLE repos (
    id             INTEGER PRIMARY KEY,
    repository     TEXT    NOT NULL UNIQUE,
    token          TEXT    NOT NULL UNIQUE,
    secret         TEXT    NOT NULL,
    github_key_id  INTEGER NOT NULL DEFAULT 0,
    github_hook_id INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Only the four fields a notification needs are kept from a push payload; the
-- raw GitHub body is never persisted.
CREATE TABLE deploys (
    id          INTEGER PRIMARY KEY,
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    delivery_id TEXT,
    ref         TEXT    NOT NULL,
    sha         TEXT    NOT NULL DEFAULT '',
    message     TEXT    NOT NULL DEFAULT '',
    author      TEXT    NOT NULL DEFAULT '',
    started_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    finished_at TEXT,
    status      TEXT    NOT NULL DEFAULT 'running'
);

-- The replay guard: a repeated X-GitHub-Delivery cannot start a second deploy.
-- Partial, so the NULL delivery_id of a manual `rec-deploy deploy` never collides.
CREATE UNIQUE INDEX deploys_delivery_id ON deploys(delivery_id) WHERE delivery_id IS NOT NULL;

CREATE INDEX deploys_repo_id ON deploys(repo_id, started_at DESC);

CREATE TABLE deploy_paths (
    id           INTEGER PRIMARY KEY,
    deploy_id    INTEGER NOT NULL REFERENCES deploys(id) ON DELETE CASCADE,
    path         TEXT    NOT NULL,
    run_as_user  TEXT    NOT NULL DEFAULT '',
    ran_as_root  INTEGER NOT NULL DEFAULT 0,
    previous_sha TEXT    NOT NULL DEFAULT '',
    new_sha      TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL,
    reason       TEXT    NOT NULL DEFAULT '',
    commands     TEXT    NOT NULL DEFAULT '[]'
);

CREATE INDEX deploy_paths_deploy_id ON deploy_paths(deploy_id);
CREATE INDEX deploy_paths_path ON deploy_paths(path);
