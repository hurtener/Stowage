"""One-time shared-port routing fix for PR 112; removed after validation."""
from pathlib import Path

p = Path('cmd/stowage/main.go')
s = p.read_text()
new = '''\t\t// Preserve /mcp/agent and /mcp/runtime until the profile mux selects
\t\t// the leaf handler. Only that leaf rewrites its path for Dockyard.
\t\troot := sharedMCPMux(
\t\t\tmcpAccessLog(stk.Log, mcpHTTPHandler),
\t\t\trestDeadlineHandler(readTimeout, writeTimeout, srv),
\t\t)
'''
if new not in s:
    marker = '\t\troot := http.NewServeMux()\n'
    if s.count(marker) != 1:
        raise RuntimeError('expected exactly one shared HTTP root mux')
    start = s.index(marker)
    end = s.index('\n\t\tapiHTTP = &http.Server{', start)
    old = s[start:end]
    if 'mcpRootRewrite(mcpHTTPHandler)' not in old or 'restDeadlineHandler(readTimeout, writeTimeout, srv)' not in old:
        raise RuntimeError('shared routing anchor does not match the reviewed source')
    p.write_text(s[:start]+new+s[end:])

p = Path('scripts/smoke/phase-ae13.sh'); s = p.read_text()
if '^TestSharedMCPProfiles' not in s:
    s = s.replace("printf '%s\\n'", "go test -race -count=1 ./cmd/stowage -run '^TestSharedMCPProfiles'\nprintf '%s\\n'", 1)
    p.write_text(s)

p = Path('.github/workflows/runtime-memory-hook.yml'); s = p.read_text()
s = s.replace("TestAgentHTTPMount|TestMCPCatalogSelection", "TestAgentHTTPMount|TestMCPCatalogSelection|TestSharedMCPProfiles")
p.write_text(s)
