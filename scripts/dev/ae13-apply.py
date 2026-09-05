"""Temporary final integration edits; removed before review."""
from pathlib import Path
import subprocess

def edit(path,old,new):
    p=Path(path);s=p.read_text()
    if new in s:return
    if s.count(old)!=1:raise RuntimeError(f'{path}: bad anchor count {s.count(old)}')
    p.write_text(s.replace(old,new,1))

edit('internal/api/explicit_handler_test.go','Name: "memory_correct", Arguments:', 'Meta: mcpsdk.Meta{"project": "p"}, Name: "memory_correct", Arguments:')
edit('internal/api/explicit_handler_test.go','t.Fatalf("MCP correction failed: %+v", result)','b, _ := json.Marshal(result.Content)\n\t\tt.Fatalf("MCP correction failed: %s", b)')
edit('internal/mcpserver/agent.go','DrilldownInput{MemoryID: in.MemoryID, Citation: in.Citation}','DrilldownInput(in)')
edit('adapters/harbor/harbor.go','func Tools(client stowage.Client) []harbortools.ToolDescriptor {','func LegacyTools(client stowage.Client) []harbortools.ToolDescriptor {')
p=Path('adapters/harbor/harbor.go');s=p.read_text();s=s.replace('// Tools registers the seven','// LegacyTools registers the seven');p.write_text(s)
p=Path('adapters/harbor/harbor_test.go');s=p.read_text();s=s.replace('descs := Tools(client)','descs := LegacyTools(client)');p.write_text(s)
p=Path('docs/agent-memory.md');s=p.read_text()
if '## In-process Harbor adapter' not in s:
    s+='''\n\n## In-process Harbor adapter

`harbor.Tools(client)` now returns the same five agent concepts with the existing
`stowage_` naming prefix. The former seven-tool runtime/curator catalog remains
available only through explicit `harbor.LegacyTools(client)`. Do not attach both
catalogs to one planner. Runtime outcome wiring is unchanged by this phase.

For a current user message, the host first calls `client.Ingest` with that actual
message, obtains its record ID, and supplies `harbor.WithMemorySource(ctx, id,
commandID)` to the tool execution context. This is not model-filled identity or
permission: the SDK client is constructed with authorized scope and the service
still verifies the record and exact quotation. An agent with no bound or existing
source gets a clear error, not a fabricated save. Retrieval returns one useful
rendered context including response-level warnings; older SDK responses without
rendered context have a complete structured fallback.
''';p.write_text(s)
p=Path('README.md');s=p.read_text()
marker='## Four surfaces, one core'
if 'Agent-facing memory interface' not in s:
    s=s.replace(marker,'## Agent-facing memory interface\n\nConnect ordinary planners to `/mcp/agent` (shared HTTP port) or `/agent` (dedicated MCP port).\nThe five tools are recall, inspect, source-backed remember, correct, and playbook.\nKeep runtime transcript capture on the existing full endpoint. Stdio defaults to the agent\ncatalog; `--catalog full` preserves the integration catalog. See\n[the source-binding and migration guide](docs/agent-memory.md) for truthful receipts,\nidempotent corrections, and the distinction between deleted memories and erased history.\n\n'+marker,1);p.write_text(s)
files=subprocess.check_output(['git','ls-files','--','*.go'],text=True).splitlines()
subprocess.run(['gofmt','-w',*files],check=True)
subprocess.run(['go','test','./internal/api','-run','^TestSourceBackedCommandsHTTPAndMCP$','-count=1'],check=True)
subprocess.run(['go','test','./cmd/stowage','-run','^TestAgentHTTPMount','-count=1'],check=True)
