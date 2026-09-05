"""Temporary, deterministic source-edit batch for PR 112. Removed before review."""
from pathlib import Path

def replace(path, old, new):
    p = Path(path)
    s = p.read_text()
    if new in s:
        return
    if s.count(old) != 1:
        raise RuntimeError(f'{path}: expected one exact edit anchor; found {s.count(old)}')
    p.write_text(s.replace(old, new, 1))

replace('internal/store/types.go', 'type CommitSet struct {', 'type CommitSet struct {\n\t// Command atomically guards a source-backed write and reserves its durable receipt.\n\tCommand *CommandGuard\n')
replace('internal/store/store.go', 'type EventStore interface {', 'type EventStore interface {\n\t// Get reads an event by ID at the EXACT scope leaf; inaccessible is ErrNotFound.\n\tGet(ctx context.Context, scope identity.Scope, id string) (*Event, error)\n')
for driver, call in [('sqlitestore', 'guardCommandSQLite(tx, scope, cs.Command, now)'), ('pgstore', 'guardCommandPG(ctx, tx, scope, cs.Command, now)')]:
    p = Path(f'internal/store/{driver}/memories.go')
    s = p.read_text()
    if call not in s:
        name = 'func execCommitSQLite' if driver == 'sqlitestore' else 'func execCommitPG'
        pos = s.index('now := time.Now().UnixMilli()', s.index(name)) + len('now := time.Now().UnixMilli()')
        s = s[:pos] + f'\n\tif err := {call}; err != nil {{\n\t\treturn err\n\t}}\n' + s[pos:]
        p.write_text(s)
for package in ['grants', 'views']:
    p = Path(f'internal/{package}/{package}_test.go')
    s = p.read_text()
    if 'func (e *mockEventStore) Get(' not in s:
        s += '''\nfunc (e *mockEventStore) Get(_ context.Context, _ identity.Scope, id string) (*store.Event, error) {
    e.mu.Lock()
    defer e.mu.Unlock()
    for _, ev := range e.events { if ev.ID == id { return &ev, nil } }
    return nil, store.ErrNotFound
}\n'''
        p.write_text(s)
