"""Temporary deterministic wiring batch for PR 112; removed before review."""
from pathlib import Path
import re
import subprocess
import os

def edit(path, old, new):
    p=Path(path); s=p.read_text()
    if new in s: return
    if s.count(old)!=1: raise RuntimeError(f'{path}: expected one anchor, got {s.count(old)}: {old[:90]}')
    p.write_text(s.replace(old,new,1))

# Keep the complete integration catalog, but give every operation task guidance.
p=Path('internal/mcpserver/server.go'); s=p.read_text()
pattern=r'tool\.New\[([^\]]+)\]\("([^"]+)"\)\.\s*Describe\(.*?\)\.\s*Handler\('
s,n=re.subn(pattern,lambda m:f'declare[{m[1]}]("{m[2]}").\n\t\tDescribe(toolDescription("{m[2]}" )).\n\t\tHandler(',s,flags=re.S)
if n not in (0,24): raise RuntimeError(f'expected 24 registrations, got {n}')
if n: s=s.replace('\n\t"github.com/hurtener/dockyard/runtime/tool"',''); p.write_text(s)
# The revised contract moves wire-size details to integration documentation.
p=Path('internal/mcpserver/server_test.go'); s=p.read_text()
s=s.replace('TestMemoryRetrieveDescribe_M4WireTruth','TestMemoryRetrieveDescribe_TaskTriggers')
s=s.replace('"context, not the wire"','"prior context"').replace('"payload grows"','"self-contained"').replace('"larger payload"','"prior evidence"')
s=s.replace('missing the M4 wire-truth phrase','missing task-selection guidance').replace('must state the payload GROWS (not shrinks)','must explain evidence limitations')
p.write_text(s)
# Registration and goldens share the same inferred-schema decoration.
edit('internal/mcpserver/golden_test.go','\treturn inJSON, outJSON, err\n}', '\tif err == nil { inJSON, err = mcpserver.DescribeInputJSON(name, inJSON) }\n\treturn inJSON, outJSON, err\n}')
edit('internal/mcpserver/agent.go','meta := requestMeta(ctx)','meta := server.RequestMeta(ctx)')
# Count-only text must not replace the actual result in text-only hosts.
edit('internal/mcpserver/catalog.go','r, err := d.handler(ctx, input)\n', 'r, err := d.handler(ctx, input)\n\t\t\tif err == nil && readerTool(d.name) { r.Text = readerJSON(r.Structured) }\n')
p=Path('internal/mcpserver/catalog.go'); s=p.read_text()
if 'func readerTool(' not in s:
    s+='''\n// readerTool names read/inspection outputs whose useful data must reach Text.
func readerTool(name string) bool {
    switch name {
    case "memory_playbook", "memory_get", "memory_inspect", "memory_drilldown", "memory_episodes", "memory_browse", "memory_causal", "memory_verify", "memory_trace", "memory_topics", "memory_suggestions", "memory_review": return true
    default: return false
    }
}\n'''; p.write_text(s)
for path in ['internal/mcpserver/handlers.go','internal/api/retrieve_handler.go','sdk/stowage/embedded.go']:
    p=Path(path); s=p.read_text(); s=s.replace('retrieval.RenderReadBody(resp.Items)','retrieval.RenderReadResponse(resp)'); p.write_text(s)
# Preserve benchmark prompt bytes while replacing imperative MCP headings.
edit('internal/retrieval/render.go','b.WriteString("CURRENT memories (answer from these):\\n")','if mode == RenderMCP { b.WriteString("CURRENT memories (prior statements; verify freshness):\\n") } else { b.WriteString("CURRENT memories (answer from these):\\n") }')
edit('internal/retrieval/render.go','b.WriteString("\\nSUPERSEDED memories (earlier values the user CHANGED — history only, NEVER answer with these):\\n")','if mode == RenderMCP { b.WriteString("\\nSUPERSEDED memories (historical values; use for historical questions, not as current facts):\\n") } else { b.WriteString("\\nSUPERSEDED memories (earlier values the user CHANGED — history only, NEVER answer with these):\\n") }')
edit('internal/retrieval/render.go','superseded = append(superseded, dated)','historical := dated\n\t\t\tif mode == RenderMCP && it.SupersededByContent != "" { historical += " | Replaced by: " + withDate(it.SupersededByContent, it.SupersededByDate) }\n\t\t\tsuperseded = append(superseded, historical)')
# New thin HTTP and SDK commands over the same core.
edit('internal/api/server.go','\t// Memory management — Phase 18 (D-064, D-065).','\t// Source-backed explicit commands; authentication remains Pengui/keyring-owned.\n\tmux.HandleFunc("POST /v1/remember", srv.authMiddleware(srv.handleRemember, false))\n\tmux.HandleFunc("POST /v1/correct", srv.authMiddleware(srv.handleCorrect, false))\n\n\t// Memory management — Phase 18 (D-064, D-065).')
edit('sdk/stowage/client.go','type Client interface {','type Client interface {\n\t// Remember preserves an exact user quotation with a durable idempotent receipt.\n\tRemember(context.Context, RememberRequest) (MemoryReceipt, error)\n\t// Correct replaces an inspected memory with newer user evidence, reversibly.\n\tCorrect(context.Context, CorrectRequest) (MemoryReceipt, error)\n')
p=Path('sdk/stowage/explicit.go'); p.write_text(p.read_text().replace('c.stack.Retriever.Cache()', 'c.scopeInvalidator()'))
# Inspection returns the optimistic revision needed by all correction clients.
edit('internal/api/memories_handler.go','type memoryResponse struct {','type memoryResponse struct {\n\tRevision string `json:"revision"`\n')
edit('internal/api/memories_handler.go','resp := memoryResponse{','resp := memoryResponse{\n\t\tRevision: store.MemoryRevision(mem),')
for p in Path('sdk/stowage').glob('*.go'):
    s=p.read_text()
    if 'type GetMemoryResponse struct {' in s and 'Revision string `json:"revision"`' not in s:
        p.write_text(s.replace('type GetMemoryResponse struct {','type GetMemoryResponse struct {\n\tRevision string `json:"revision"`\n',1))
edit('sdk/stowage/embedded.go','resp := GetMemoryResponse{','resp := GetMemoryResponse{\n\t\tRevision: store.MemoryRevision(view.Memory),')
# Actual production mounting, no new identity mechanism and no dynamic catalog.
edit('cmd/stowage/main.go','\t\tconfigPath string\n\t\thttpAddr   string','\t\tconfigPath string\n\t\thttpAddr   string\n\t\tcatalog = "agent"')
edit('cmd/stowage/main.go','\t\tcase "--http":','\t\tcase "--catalog":\n\t\t\tif i+1 >= len(args) || (args[i+1] != "agent" && args[i+1] != "full") { fmt.Fprintln(os.Stderr, "stowage mcp: --catalog must be agent or full"); os.Exit(2) }; catalog = args[i+1]; i++\n\t\tcase "--http":')
edit('cmd/stowage/main.go','\tsrv, err := mcpserver.New(server.Info{','\tconstructor := mcpserver.New\n\tif httpAddr == "" && catalog == "agent" { constructor = mcpserver.NewAgent }\n\tsrv, err := constructor(server.Info{')
edit('cmd/stowage/main.go','\t\thttpSrv := &http.Server{\n\t\t\tAddr:              httpAddr,','\t\thandler, hErr = withAgentMCP(handler, svc, cfg.Auth.Mode, cfg.Server.MCPTrustProxy)\n\t\tif hErr != nil { stk.Log.Error("stowage mcp: agent catalog", "err", hErr); os.Exit(1) }\n\t\thttpSrv := &http.Server{\n\t\t\tAddr:              httpAddr,')
edit('cmd/stowage/main.go','mcpHTTPHandler = mcpAuthHandler(cfg.Auth.Mode, authn, mcpHandler)','dualHandler, agentErr := withAgentMCP(mcpHandler, mcpSvc, cfg.Auth.Mode, cfg.Server.MCPTrustProxy)\n\t\tif agentErr != nil { stk.Log.Error("stowage serve: agent catalog", "err", agentErr); os.Exit(1) }\n\t\tmcpHTTPHandler = mcpAuthHandler(cfg.Auth.Mode, authn, dualHandler)')
edit('cmd/stowage/main.go','mcpAccessLog(stk.Log, mcpRootRewrite(mcpHTTPHandler))','mcpAccessLog(stk.Log, mcpHTTPHandler)')
# The linked framework's pinned public API does not offer MCP annotations; do
# not invent UI metadata to pretend they are authorization or read-only hints.
# Format actual source, then refresh checked-in schemas through production code.
files=subprocess.check_output(['git','ls-files','--','*.go'],text=True).splitlines()
subprocess.run(['gofmt','-w',*files],check=True)
env=dict(os.environ,UPDATE_GOLDEN='1')
subprocess.run(['go','test','./internal/mcpserver','-run','TestSchemaGoldens|TestAgentCatalogGoldens','-count=1'],check=True,env=env)
