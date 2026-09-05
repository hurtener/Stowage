"""Temporary final regression edits; removed before review."""
from pathlib import Path
import subprocess

def edit(path,old,new):
    p=Path(path);s=p.read_text()
    if new in s:return
    if s.count(old)!=1:raise RuntimeError(f'{path}: anchor count {s.count(old)}')
    p.write_text(s.replace(old,new,1))

p=Path('internal/retrieval/render_test.go');s=p.read_text()
start=s.index('func TestRender_MCPGolden_LiveSlots(');end=s.index('\nfunc ',start+1)
part=s[start:end].replace('CURRENT memories (answer from these):','CURRENT memories (prior statements; verify freshness):').replace('SUPERSEDED memories (earlier values the user CHANGED — history only, NEVER answer with these):','SUPERSEDED memories (historical values; use for historical questions, not as current facts):').replace("[S1] User's commute was 45 minutes. [cite:","[S1] User's commute was 45 minutes. | Replaced by: User's commute is now 30 minutes. | When: 2023-05-16 [cite:")
s=s[:start]+part+s[end:]
start=s.index('func TestRenderReadBody_Empty(');end=s.index('\nfunc ',start+1)
part=s[start:end].replace('CURRENT memories (answer from these):','CURRENT memories (prior statements; verify freshness):')
s=s[:start]+part+s[end:]
start=s.index('func TestRender_ConcurrentReuse(');end=s.index('\nfunc ',start+1)
part=s[start:end].replace('for i := 1; i < n; i++ {\n\t\tif results[i].ContextBlock != results[0].ContextBlock {','for i := 0; i < n; i++ {\n\t\tmode := RenderEval\n\t\tif i%2 == 0 { mode = RenderMCP }\n\t\twant := Render(mode, fixture)\n\t\tif results[i].ContextBlock != want.ContextBlock {').replace('ContextBlock diverged from goroutine 0','ContextBlock diverged from its mode-specific serial rendering')
s=s[:start]+part+s[end:];p.write_text(s)

p=Path('internal/reconcile/remember_durability_test.go');s=p.read_text()
s=s.replace('"os"','"os"\n\t"net/url"\n\t"strings"\n\t"github.com/jackc/pgx/v5"',1) if '"net/url"' not in s else s
old='cfg.Store.DSN = dsn\n'
new='''// Other packages truncate the public test tables. Isolate this suite's
    // schema so concurrent package tests cannot erase command evidence.
    admin, err := pgx.Connect(ctx, dsn)
    if err != nil { t.Fatal(err) }
    defer func(){ _ = admin.Close(ctx) }()
    schema := "ae13_" + strings.ToLower(ulid.Make().String())
    quoted := pgx.Identifier{schema}.Sanitize()
    if _, err := admin.Exec(ctx, "CREATE SCHEMA " + quoted); err != nil { t.Fatal(err) }
    defer func(){ if _, err := admin.Exec(ctx, "DROP SCHEMA " + quoted + " CASCADE"); err != nil { t.Error(err) } }()
    if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
        u, err := url.Parse(dsn)
        if err != nil { t.Fatal(err) }
        q := u.Query(); q.Set("search_path", schema); u.RawQuery = q.Encode()
        cfg.Store.DSN = u.String()
    } else { cfg.Store.DSN = dsn + " search_path=" + schema }
'''
if 'Other packages truncate the public test tables.' not in s:
    if s.count(old)!=1:raise RuntimeError('Postgres DSN anchor mismatch')
    s=s.replace(old,new,1)
s=s.replace('t.Fatalf("expected one winner and one conflict; got %d/%d", len(results), len(errs))','for err := range errs { t.Logf("correction error: %v", err) }\n\t\tt.Fatalf("expected one winner and one conflict; got %d results", len(results))')
p.write_text(s)
files=subprocess.check_output(['git','ls-files','--','*.go'],text=True).splitlines()
subprocess.run(['gofmt','-w',*files],check=True)
subprocess.run(['go','test','./internal/retrieval','-run','TestRender|TestReader','-count=1'],check=True)
subprocess.run(['go','test','./internal/reconcile','-run','^TestExplicitCommandsPostgres$','-count=1','-v'],check=True)
subprocess.run(['go','test','-count=1','./...'],check=True,cwd='adapters/harbor')
