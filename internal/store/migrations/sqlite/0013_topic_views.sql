-- Phase ae1 (D-135/D-146, generalized by the checkpoint decision D-151): the
-- general (subject_kind, subject_id, view_name) -> topic-key policy-binding
-- junction. ae1 is the table's only writer/reader for this phase, always with
-- subject_kind='agent' and view_name='default' (agentID -> subject_id); ae9
-- generalizes the *semantics* (named views, key-id subject) on these same rows
-- with other subject_kind/view_name values, disjoint by construction (the
-- unique index is keyed on the full tuple). NOT one of the 12 scope tables:
-- carries no memory rows and no user_id.
CREATE TABLE IF NOT EXISTS topic_views (
    id           TEXT    NOT NULL PRIMARY KEY,
    tenant_id    TEXT    NOT NULL,
    subject_kind TEXT    NOT NULL DEFAULT 'agent',
    subject_id   TEXT    NOT NULL,
    view_name    TEXT    NOT NULL DEFAULT 'default',
    topic_key    TEXT    NOT NULL,
    effect       TEXT    NOT NULL CHECK(effect IN ('allow','deny')),
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_topic_views_subject
    ON topic_views(tenant_id, subject_kind, subject_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_topic_views
    ON topic_views(tenant_id, subject_kind, subject_id, view_name, topic_key);
