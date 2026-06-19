-- Bar-remediation (#3 / RFC §8.1 amendment): memory→topic association so a grant's
-- topic_filter can slice the shared memories (RFC §5.3). A memory is linked to the
-- extraction topic(s) it pertains to, tagged by the extractor (D-089). Mirrors the
-- entities/keywords/queries junction shape.
CREATE TABLE IF NOT EXISTS memory_topics (
    id        TEXT NOT NULL PRIMARY KEY,
    memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    topic_key TEXT NOT NULL,
    tenant_id TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_topics_memory ON memory_topics(memory_id);
CREATE INDEX IF NOT EXISTS idx_memory_topics_key ON memory_topics(tenant_id, topic_key);
