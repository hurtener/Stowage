"""One-time exact-anchor edits for PR 112; removed after generated commit."""
from pathlib import Path
import re
import subprocess

def edit(path, old, new):
    p = Path(path)
    s = p.read_text()
    if new in s:
        return
    if s.count(old) != 1:
        raise RuntimeError(f'{path}: expected one anchor, got {s.count(old)} for {old[:80]}')
    p.write_text(s.replace(old, new, 1))

edit('cmd/stowage/main.go', '(args[i+1] != "agent" && args[i+1] != "full")', '(args[i+1] != "agent" && args[i+1] != "runtime" && args[i+1] != "full")')
edit('cmd/stowage/main.go', 'stowage mcp: --catalog must be agent or full', 'stowage mcp: --catalog must be agent, runtime, or full')
edit('cmd/stowage/main.go', '--catalog agent|full  Stdio catalog (default agent); HTTP retains both endpoints.', '--catalog agent|runtime|full  Stdio catalog (default agent); HTTP mounts all three profiles.')
edit('cmd/stowage/main.go', '\tconstructor := mcpserver.New\n\tif httpAddr == "" && catalog == "agent" {\n\t\tconstructor = mcpserver.NewAgent\n\t}\n\tsrv, err := constructor(server.Info{', '\tprofile := "full" // HTTP keeps the compatibility root and mounts both other profiles.\n\tif httpAddr == "" {\n\t\tprofile = catalog\n\t}\n\tsrv, err := newMCPCatalog(profile, server.Info{')

p = Path('scripts/smoke/phase-ae13.sh')
s = p.read_text()
if "-run '^TestRuntime'" not in s:
    s = s.replace("printf '%s\\n'", "go test -race -count=1 ./internal/mcpserver -run '^TestRuntime'\ngo test -race -count=1 ./cmd/stowage -run '^TestMCPCatalogSelection$'\nprintf '%s\\n'", 1)
    p.write_text(s)

p = Path('README.md')
s = p.read_text()
new = '''## Agent-facing memory interface

For **Pengui / Harbor with run-end capture**, attach `/mcp/runtime` (shared HTTP)
or `/runtime` (dedicated MCP). It registers the five ordinary memory operations
plus `memory_ingest_run` on the SAME capability connection. Use Pengui's existing
tool-exposure control to disable that sink **for the planner**, while keeping the
run-completion hook enabled. Deferred loading is not sufficient.

Pure agent clients without automatic capture can use `/mcp/agent` or `/agent`.
The legacy full endpoint is unchanged. Stdio supports `--catalog runtime`,
`--catalog agent` (default), and `--catalog full`. See the
[source-binding and rollout guide](docs/agent-memory.md), including the distinction
between planner exclusion, hook disablement, and erased history.

'''
if 'For **Pengui / Harbor with run-end capture**' not in s:
    if '## Agent-facing memory interface' in s:
        s, n = re.subn(r'## Agent-facing memory interface\n.*?(?=\n## |\Z)', new.rstrip()+'\n', s, count=1, flags=re.S)
        if n != 1: raise RuntimeError('README agent section missing')
    else:
        s = new + s
    p.write_text(s)

amendment = '''\n\n## Runtime-completion compatibility correction (ae13, PR #112)

The five-tool ordinary projection is preserved, but it is NOT the correct sole
connection for Pengui automatic capture. Pengui discovers the completion target
on the same attached memory capability; a sink on an unattached full endpoint
is not usable. NewRuntime therefore composes the same five registrations with
memory_ingest_run (six host-facing tools) at /mcp/runtime or /runtime and through
--catalog runtime. The existing full catalog and versioned sink are unchanged.

The host excludes the sink from planner List AND Resolve/dispatch using Harbor's
existing DisabledTools projection, while its trusted completion path resolves
the full catalog. Deferred loading does not close planner execution. This is
host-governed exposure, NOT new provider authorization; no _meta marker can grant
runtime rights. Pengui configures the hook, Harbor executes it, Stowage processes
the transcript. Disabling planner access does not disable automatic capture.

This correction supersedes ae13's earlier endpoint-only Pengui migration advice.
It does not automatically modify Pengui activation defaults or deployed revisions;
operators must pair sink exclusion with the existing save-hook control. It adds
no second issuer, duplicate transcript collector, new ingestion payload, retries,
or exactly-once delivery promise. See docs/agent-memory.md for the safe sequence.
'''
p = Path('RFC-001-Stowage.md'); s = p.read_text()
if '## Runtime-completion compatibility correction (ae13, PR #112)' not in s: p.write_text(s+amendment)
p = Path('docs/plans/phase-ae13-agent-interface.md'); s = p.read_text()
if '## Runtime-completion compatibility correction' not in s:
    p.write_text(s+amendment+'''\n### Additional acceptance checks

The runtime catalog has six tools; its ordinary registrations match the pure
agent catalog and its sink matches full compatibility discovery. All three HTTP
profiles and stdio selectors remain explicit. A real Harbor v1.31.4 fixture must
reject planner calls to the disabled sink yet deliver the actual terminal
transcript through its trusted hook. Verify distinct users, cancellation,
hook-disabled and missing-sink outcomes against Stowage's durable store. The
read-only runtime-memory-hook workflow owns that cross-repository regression.
''')
p = Path('docs/decisions.md'); s = p.read_text()
if 'Preserve automatic ingestion in the agent-surface migration' not in s:
    number = max([int(x) for x in re.findall(r'^#{1,6}\s+D-(\d+)', s, re.M)], default=0)+1
    p.write_text(s+f'''\n\n## D-{number:03d} — Preserve automatic ingestion in the agent-surface migration

**Date:** 2026-09-05. **Status:** owner-approved PR #112 correction.

Add a six-tool host-facing runtime profile composing the five ordinary tools and
the existing memory_ingest_run sink on ONE MCP connection. Pengui's same-source
hook discovery makes the previous endpoint-only rollout advice insufficient.
Keep the five-tool pure agent profile and full compatibility catalog unchanged.
Harbor's planner exclusion and trusted completion dispatch provide the split;
deferral does not. No new issuer, caller-controlled privilege marker, capture
pipeline or ingestion-delivery guarantee. See the ae13 RFC correction and
`docs/agent-memory.md`. Existing Pengui controls can apply the rollout; automatic
Pengui activation-policy changes and deployed acceptance are not claimed here.
''')
p = Path('docs/glossary.md'); s = p.read_text()
if '### Runtime memory profile (ae13)' not in s:
    p.write_text(s+'''\n\n### Runtime memory profile (ae13)
The host-facing six-tool MCP catalog: five ordinary operations and the existing
run-completion sink. The host retains all six for execution but excludes the
sink from the planner. Endpoint choice, deferred loading and descriptive hints
are not authorization. Pengui configures capture; Harbor invokes its trusted
completion hook; Stowage accepts the transcript.
''')
p = Path('CHANGELOG.md'); s = p.read_text()
if 'Runtime-completion compatibility correction' not in s:
    s = s.replace('### Added\n', '### Added\n\n- Runtime-completion compatibility correction: `/mcp/runtime`, `/runtime`, and\n  `--catalog runtime` expose the five ordinary memory tools plus the existing\n  ingestion sink on one host connection. Pengui must exclude the sink from the\n  planner while retaining its completion hook; deferred loading is not enough.\n  The pure agent and full compatibility profiles remain unchanged. A real\n  pinned-Harbor/MCP/SQLite regression covers the hidden-sink boundary.\n', 1)
    p.write_text(s)
files = subprocess.check_output(['git', 'ls-files', '--', '*.go'], text=True).splitlines()
subprocess.run(['gofmt', '-w', *files], check=True)
subprocess.run(['go', 'test', '-race', '-count=1', '-run', '^TestRuntime', './internal/mcpserver'], check=True)
subprocess.run(['go', 'test', '-race', '-count=1', '-run', 'TestAgentHTTPMount|TestMCPCatalogSelection', './cmd/stowage'], check=True)
