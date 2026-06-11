-- Phase 03 day-one schema — PostgreSQL dialect (D-037, D-038)
-- Timestamps: BIGINT unix-millis. IDs: TEXT (ULID). Hash: BYTEA.
-- memory_vectors deferred to Phase 09 (D-038).

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT   NOT NULL PRIMARY KEY,
    checksum   TEXT   NOT NULL,
    applied_at BIGINT NOT NULL
);

-- ---------------------------------------------------------------------------
-- Verbatim fidelity layer
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS records (
    id             TEXT   NOT NULL PRIMARY KEY,
    tenant_id      TEXT   NOT NULL,
    project_id     TEXT,
    user_id        TEXT,
    session_id     TEXT,
    branch_id      TEXT   NOT NULL DEFAULT '',
    role           TEXT   NOT NULL CHECK(role IN ('user','assistant','tool')),
    content        TEXT   NOT NULL,
    source_agent   TEXT   NOT NULL DEFAULT '',
    response_id    TEXT   NOT NULL DEFAULT '',
    outcome        TEXT   NOT NULL DEFAULT '' CHECK(outcome IN ('','success','failure')),
    outcome_detail TEXT   NOT NULL DEFAULT '',
    token_estimate BIGINT NOT NULL DEFAULT 0,
    occurred_at    BIGINT NOT NULL,
    created_at     BIGINT NOT NULL,
    processed_at   BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_records_tenant_occurred       ON records(tenant_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_records_tenant_session_branch ON records(tenant_id, session_id, branch_id);
CREATE INDEX IF NOT EXISTS idx_records_processed_at          ON records(processed_at) WHERE processed_at = 0;

-- ---------------------------------------------------------------------------
-- Abstraction layer
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS memories (
    id               TEXT             NOT NULL PRIMARY KEY,
    tenant_id        TEXT             NOT NULL,
    project_id       TEXT,
    user_id          TEXT,
    session_id       TEXT,
    kind             TEXT             NOT NULL CHECK(kind IN ('fact','preference','decision','gotcha','pattern','task','narrative','strategy','failure_mode')),
    content          TEXT             NOT NULL,
    context          TEXT             NOT NULL DEFAULT '',
    status           TEXT             NOT NULL DEFAULT 'active' CHECK(status IN ('active','pending_confirmation','pending_review','superseded','quarantined','expired','deleted')),
    importance       INTEGER          NOT NULL DEFAULT 3,
    confidence       DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    trust_source     TEXT             NOT NULL DEFAULT 'llm_extracted',
    match_count      BIGINT           NOT NULL DEFAULT 0,
    inject_count     BIGINT           NOT NULL DEFAULT 0,
    use_count        BIGINT           NOT NULL DEFAULT 0,
    save_count       BIGINT           NOT NULL DEFAULT 0,
    fail_count       BIGINT           NOT NULL DEFAULT 0,
    noise_count      BIGINT           NOT NULL DEFAULT 0,
    stability        DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    last_accessed_at BIGINT           NOT NULL DEFAULT 0,
    valid_from       BIGINT           NOT NULL DEFAULT 0,
    valid_until      BIGINT           NOT NULL DEFAULT 0,
    episode_id       TEXT             NOT NULL DEFAULT '',
    supersedes_id    TEXT             NOT NULL DEFAULT '',
    superseded_by_id TEXT             NOT NULL DEFAULT '',
    privacy_zone     TEXT             NOT NULL DEFAULT 'work',
    created_at       BIGINT           NOT NULL,
    updated_at       BIGINT           NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memories_tenant_status ON memories(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_memories_tenant_kind   ON memories(tenant_id, kind);

CREATE TABLE IF NOT EXISTS memory_entities (
    id        TEXT NOT NULL PRIMARY KEY,
    memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    entity    TEXT NOT NULL,
    tenant_id TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_entities_memory ON memory_entities(memory_id);

CREATE TABLE IF NOT EXISTS memory_keywords (
    id        TEXT NOT NULL PRIMARY KEY,
    memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    keyword   TEXT NOT NULL,
    tenant_id TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_keywords_memory ON memory_keywords(memory_id);

CREATE TABLE IF NOT EXISTS memory_queries (
    id        TEXT NOT NULL PRIMARY KEY,
    memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    query     TEXT NOT NULL,
    tenant_id TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_queries_memory ON memory_queries(memory_id);

CREATE TABLE IF NOT EXISTS provenance (
    id         TEXT   NOT NULL PRIMARY KEY,
    memory_id  TEXT   NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    record_id  TEXT   NOT NULL REFERENCES records(id),
    span_start INTEGER NOT NULL DEFAULT 0,
    span_end   INTEGER NOT NULL DEFAULT 0,
    tenant_id  TEXT   NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_provenance_memory ON provenance(memory_id);

-- ---------------------------------------------------------------------------
-- Injections — attribution backbone (D-025)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS injections (
    id          TEXT             NOT NULL PRIMARY KEY,
    tenant_id   TEXT             NOT NULL,
    project_id  TEXT,
    user_id     TEXT,
    session_id  TEXT,
    response_id TEXT             NOT NULL,
    memory_id   TEXT             NOT NULL REFERENCES memories(id),
    rank        INTEGER          NOT NULL DEFAULT 0,
    score       DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    lane        TEXT             NOT NULL DEFAULT '',
    was_cited   BOOLEAN          NOT NULL DEFAULT FALSE,
    feedback    TEXT             NOT NULL DEFAULT '',
    created_at  BIGINT           NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_injections_response ON injections(response_id);

-- ---------------------------------------------------------------------------
-- Link graph (D-026)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS links (
    id          TEXT             NOT NULL PRIMARY KEY,
    tenant_id   TEXT             NOT NULL,
    from_memory TEXT             NOT NULL REFERENCES memories(id),
    to_memory   TEXT             NOT NULL REFERENCES memories(id),
    type        TEXT             NOT NULL CHECK(type IN ('supports','contradicts','depends_on','caused_by','led_to','relates_to')),
    source      TEXT             NOT NULL DEFAULT 'explicit' CHECK(source IN ('explicit','reconciler','inferred')),
    confidence  DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    created_at  BIGINT           NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_links_from ON links(from_memory, type);
CREATE INDEX IF NOT EXISTS idx_links_to   ON links(to_memory);

-- ---------------------------------------------------------------------------
-- Episodes and branches (D-026, D-029)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS episodes (
    id                  TEXT   NOT NULL PRIMARY KEY,
    tenant_id           TEXT   NOT NULL,
    project_id          TEXT,
    user_id             TEXT,
    session_id          TEXT,
    title               TEXT   NOT NULL DEFAULT '',
    status              TEXT   NOT NULL DEFAULT 'open' CHECK(status IN ('open','closed','archived')),
    started_at          BIGINT NOT NULL,
    ended_at            BIGINT NOT NULL DEFAULT 0,
    narrative_memory_id TEXT   NOT NULL DEFAULT '',
    outcome             TEXT   NOT NULL DEFAULT '',
    created_at          BIGINT NOT NULL,
    updated_at          BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS branches (
    id               TEXT   NOT NULL PRIMARY KEY,
    tenant_id        TEXT   NOT NULL,
    project_id       TEXT,
    user_id          TEXT,
    session_id       TEXT   NOT NULL,
    parent_branch_id TEXT   NOT NULL DEFAULT '',
    status           TEXT   NOT NULL DEFAULT 'open' CHECK(status IN ('open','merged','discarded')),
    created_at       BIGINT NOT NULL,
    updated_at       BIGINT NOT NULL
);

-- ---------------------------------------------------------------------------
-- Topics and buffers (D-007)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS topics (
    id          TEXT   NOT NULL PRIMARY KEY,
    tenant_id   TEXT   NOT NULL,
    project_id  TEXT,
    user_id     TEXT,
    session_id  TEXT,
    key         TEXT   NOT NULL,
    description TEXT   NOT NULL DEFAULT '',
    status      TEXT   NOT NULL DEFAULT 'active' CHECK(status IN ('active','paused','deleted')),
    pack        TEXT   NOT NULL DEFAULT '',
    created_at  BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL,
    UNIQUE(tenant_id, project_id, user_id, session_id, key)
);

CREATE TABLE IF NOT EXISTS buffer_items (
    id             TEXT   NOT NULL PRIMARY KEY,
    tenant_id      TEXT   NOT NULL,
    project_id     TEXT,
    user_id        TEXT,
    session_id     TEXT,
    buffer_key     TEXT   NOT NULL,
    branch_id      TEXT   NOT NULL DEFAULT '',
    record_id      TEXT   NOT NULL DEFAULT '',
    token_estimate BIGINT NOT NULL DEFAULT 0,
    flushed_at     BIGINT NOT NULL DEFAULT 0,
    created_at     BIGINT NOT NULL
);

-- ---------------------------------------------------------------------------
-- Groups, members and grants (D-016)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS groups (
    id         TEXT   NOT NULL PRIMARY KEY,
    tenant_id  TEXT   NOT NULL,
    name       TEXT   NOT NULL,
    created_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS group_members (
    id         TEXT   NOT NULL PRIMARY KEY,
    group_id   TEXT   NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id    TEXT   NOT NULL,
    tenant_id  TEXT   NOT NULL,
    created_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS grants (
    id                TEXT   NOT NULL PRIMARY KEY,
    tenant_id         TEXT   NOT NULL,
    project_id        TEXT,
    user_id           TEXT,
    session_id        TEXT,
    group_id          TEXT   NOT NULL REFERENCES groups(id),
    access            TEXT   NOT NULL DEFAULT 'read' CHECK(access IN ('read','contribute')),
    topic_filter      TEXT   NOT NULL DEFAULT '',
    kind_filter       TEXT   NOT NULL DEFAULT '',
    zone_ceiling      TEXT   NOT NULL DEFAULT 'work',
    redaction_profile TEXT   NOT NULL DEFAULT '',
    revoked_at        BIGINT NOT NULL DEFAULT 0,
    created_at        BIGINT NOT NULL,
    updated_at        BIGINT NOT NULL
);

-- ---------------------------------------------------------------------------
-- Feedback and suggestions (D-028)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS feedback (
    id           TEXT   NOT NULL PRIMARY KEY,
    tenant_id    TEXT   NOT NULL,
    memory_id    TEXT   NOT NULL REFERENCES memories(id),
    injection_id TEXT   NOT NULL DEFAULT '',
    response_id  TEXT   NOT NULL DEFAULT '',
    signal       TEXT   NOT NULL CHECK(signal IN ('useful','wrong_citation','dismissed','noise')),
    note         TEXT   NOT NULL DEFAULT '',
    created_at   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS suggestions (
    id            TEXT    NOT NULL PRIMARY KEY,
    tenant_id     TEXT    NOT NULL,
    project_id    TEXT,
    user_id       TEXT,
    session_id    TEXT,
    trigger_kind  TEXT    NOT NULL DEFAULT '',
    memory_id     TEXT    NOT NULL DEFAULT '',
    episode_id    TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','accepted','dismissed','expired')),
    accept_count  INTEGER NOT NULL DEFAULT 0,
    dismiss_count INTEGER NOT NULL DEFAULT 0,
    created_at    BIGINT  NOT NULL,
    updated_at    BIGINT  NOT NULL
);

-- ---------------------------------------------------------------------------
-- Scope settings (D-028)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS scope_settings (
    id         TEXT   NOT NULL PRIMARY KEY,
    tenant_id  TEXT   NOT NULL,
    project_id TEXT,
    user_id    TEXT,
    session_id TEXT,
    key        TEXT   NOT NULL,
    value      TEXT   NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    UNIQUE(tenant_id, project_id, user_id, session_id, key)
);

-- ---------------------------------------------------------------------------
-- API keys (D-030)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS api_keys (
    id         TEXT   NOT NULL PRIMARY KEY,
    tenant_id  TEXT   NOT NULL,
    role       TEXT   NOT NULL CHECK(role IN ('agent','admin')),
    hash       BYTEA  NOT NULL,
    created_at BIGINT NOT NULL,
    revoked_at BIGINT NOT NULL DEFAULT 0
);

-- ---------------------------------------------------------------------------
-- Events — audit trail (D-024)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS events (
    id         TEXT   NOT NULL PRIMARY KEY,
    tenant_id  TEXT   NOT NULL,
    project_id TEXT,
    user_id    TEXT,
    session_id TEXT,
    type       TEXT   NOT NULL,
    subject_id TEXT   NOT NULL DEFAULT '',
    reason     TEXT   NOT NULL DEFAULT '',
    payload    TEXT   NOT NULL DEFAULT '{}',
    created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_tenant_created ON events(tenant_id, created_at);

-- ---------------------------------------------------------------------------
-- Dead letters and job markers (RFC §11)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS dead_letters (
    id          TEXT    NOT NULL PRIMARY KEY,
    stage       TEXT    NOT NULL,
    item_id     TEXT    NOT NULL,
    error       TEXT    NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 1,
    resolved_at BIGINT  NOT NULL DEFAULT 0,
    created_at  BIGINT  NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dead_letters_stage_resolved ON dead_letters(stage, resolved_at);

CREATE TABLE IF NOT EXISTS job_markers (
    id      TEXT   NOT NULL PRIMARY KEY,
    job     TEXT   NOT NULL,
    marker  TEXT   NOT NULL,
    ran_at  BIGINT NOT NULL,
    UNIQUE(job, marker)
);
