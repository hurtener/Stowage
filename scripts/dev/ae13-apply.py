"""Temporary final integration edits for PR 112; removed before review."""
from pathlib import Path
import re
import subprocess
import os

def edit(path,old,new):
    p=Path(path);s=p.read_text()
    if new in s:return
    if s.count(old)!=1:raise RuntimeError(f'{path}: anchor count {s.count(old)} for {old[:70]}')
    p.write_text(s.replace(old,new,1))

for typ in ['RememberRequest','CorrectRequest']:
    name='Remember' if typ=='RememberRequest' else 'Correct'
    old=f'func (c *httpClient) {name}(ctx context.Context, req {typ}) (MemoryReceipt, error) {{'
    edit('sdk/stowage/explicit.go',old,old+'\n\treq.ProjectID, req.UserID = c.effScope(req.ProjectID, req.UserID)')
# Preserve warning-aware parity checks, rather than loosening the new output.
edit('internal/mcpserver/handlers_test.go','want := retrieval.RenderReadBody(reconstructMemoryItems(result.Structured.Items))','fixture := &retrieval.Response{Items: reconstructMemoryItems(result.Structured.Items), Degraded: result.Structured.Degraded, DegradedRerank: result.Structured.DegradedRerank, DegradedTopicFilter: result.Structured.DegradedTopicFilter, DegradedAgentFilter: result.Structured.DegradedAgentFilter, DegradedView: result.Structured.DegradedView}\n\tfixture.Support.Strength = result.Structured.Support.Strength\n\twant := retrieval.RenderReadResponse(fixture)')
edit('internal/mcpserver/handlers_test.go','want := "CURRENT memories (answer from these):\\n(no current memories retrieved)\\n"\n\tif result.Text != want {','want := "CURRENT memories (prior statements; verify freshness):\\n(no current memories retrieved)\\n"\n\tif !strings.Contains(result.Text, want) || !strings.Contains(result.Text, "not proof") {')
# Legacy direct inspection also exposes the revision used by the new correction.
edit('internal/mcpserver/contracts.go','type GetOutput struct {','type GetOutput struct {\n\tRevision string `json:"revision"`\n')
edit('internal/mcpserver/handlers.go','out := GetOutput{','out := GetOutput{\n\t\t\tRevision: store.MemoryRevision(view.Memory),')
# Correcting to identical content must not certify an old uncited assertion.
edit('internal/reconcile/remember.go','if NormalizeContent(target.Content) == NormalizeContent(req.Quote) {','if NormalizeContent(target.Content) == NormalizeContent(req.Quote) {\n\t\t\tif len(junctions.Provenance) == 0 { return nil, fmt.Errorf("%w: matching target lacks source provenance", store.ErrCommandConflict) }')
# Document command-line migration where users discover transport choices.
p=Path('cmd/stowage/main.go');s=p.read_text()
if '  --catalog agent|full' not in s:
    start=s.index('const mcpUsage')
    end=s.index('`',s.index('`',start)+1)
    s=s[:end]+'\n  --catalog agent|full  Stdio catalog (default agent); HTTP retains both endpoints.\n'+s[end:]
    p.write_text(s)
# Append a scoped RFC amendment and a non-duplicated decision entry.
amend='''\n\n## Agent-facing interface and explicit-write amendment (ae13)

The ordinary agent surface is a five-tool projection (retrieve, inspect,
remember, correct, playbook), separately mounted from the retained full runtime/
curator surface. Stdio defaults to the agent projection with an explicit full
compatibility option. HTTP uses static catalogs, identity-independent discovery,
and the existing authentication/scoping seam. No new issuer or permissions
inference is authorized. See `docs/agent-memory.md` for the binding endpoint and
source-binding contract.

Explicit remember/correct commands are a new provenance-preserving lifecycle
path, not aliases for direct assertion. They commit an exact quotation from a
durable, owned user record; no generated summary can claim user provenance.
Corrections require the inspected semantic revision and newer source evidence,
retain the old value and a reversible event, and fail on competing edits. Store
CommitSet carries an optional command guard. Receipt reservation in the existing
events table, evidence and revision checks, memory/provenance changes and audit
events occur in one SQLite/Postgres transaction. Receipt IDs are deterministic
per owner scope and host key (canonical request digest when omitted); reuse with
another payload fails. No new schema table or migration is necessary.

Explicit intent bypasses extraction magnets, never scopes or provenance. Exact
active same-session content with provenance may be reused. Vector backfill and
ranking are not prerequisites for the durable receipt; current status and
retrieval eligibility are separately observed. Current topic curation and session
cooldown still apply. A replay does not re-save a deleted/superseded value.

Model-facing projections retain limitations, conflicts and evidence; evaluation
rendering remains independently pinned. Historical evidence may answer historical
questions and never becomes a higher-priority instruction. The service/contract
baseline is captured from the immutable pre-change commit; no spontaneous model
selection result is inferred from deterministic tests.

No selective forgetting tool is exposed until suppression, source retention,
re-extraction, dependents, caches, audit and backup policy have a complete
contract. Derived-item deletion and reversible correction are not erasure;
existing authorized whole-user DSAR remains a separate capability.
'''
p=Path('RFC-001-Stowage.md');s=p.read_text()
if '## Agent-facing interface and explicit-write amendment (ae13)' not in s:p.write_text(s+amend)
p=Path('docs/decisions.md');s=p.read_text()
if 'Agent catalog and source-backed explicit commands (ae13)' not in s:
    nums=[int(x) for x in re.findall(r'^#{1,6}\s+D-(\d+)',s,re.M)]
    number=max(nums,default=158)+1
    s+=f'''\n\n## D-{number:03d} — Agent catalog and source-backed explicit commands (ae13)

**Date:** 2026-09-05. **Status:** accepted by owner scope.

Separate the ordinary five-tool planner catalog from retained runtime/admin
compatibility operations. Use task-oriented descriptions and generated-schema
constraints; preserve response-level evidence warnings in every model-facing
read projection. Keep Pengui-issued identity authority unchanged.

Remember/correct use the shared transactional reconciliation store, validate an
exact owned user-source span, attach provenance, and return durable receipts.
Corrections check an inspected semantic revision and remain reversible. Reuse of
a scoped idempotency key with another request fails; successful replay observes
current state without reviving it. Reuse the existing events table for atomic
receipts rather than add a second command store. Do not alias direct assertions,
and do not expose selective forgetting as a promise of erasure.

See the ae13 RFC amendment, `docs/plans/phase-ae13-agent-interface.md`, and
`docs/agent-memory.md`. Baseline catalog/service observations are not live-model
selection metrics.
''';p.write_text(s)
p=Path('docs/glossary.md');s=p.read_text()
if '### Source-backed explicit command (ae13)' not in s:
    p.write_text(s+'''\n\n### Source-backed explicit command (ae13)
An intentional remember/correct operation that validates a durable user quotation
and stores its provenance through the shared transactional lifecycle seam. It is
not a model-authored assertion or a transcript reconstructed by an agent.

### Processing receipt (ae13)
Durable command outcome plus an independently observed current memory status.
Replay returns the original outcome without repeating effects; eligible does not
promise rank, completed embeddings, topic-view inclusion, or bypassing cooldown.

### Agent catalog (ae13)
A small static planner projection, separate from runtime and curator operations.
Absence from this server's catalog also means no registered handler for that
name. It does not replace authentication on any endpoint.
''')
p=Path('docs/plans/README.md');s=p.read_text()
if 'phase-ae13-agent-interface.md' not in s:p.write_text(s+'\n\n### ae13 — Agent interface and source-backed explicit commands\n\nSee [ae13](phase-ae13-agent-interface.md) and [integration contract](../agent-memory.md).\n')
files=subprocess.check_output(['git','ls-files','--','*.go'],text=True).splitlines()
subprocess.run(['gofmt','-w',*files],check=True)
subprocess.run(['go','test','./internal/mcpserver','-run','TestSchemaGoldens|TestAgentCatalogGoldens','-count=1'],check=True,env=dict(os.environ,UPDATE_GOLDEN='1'))
