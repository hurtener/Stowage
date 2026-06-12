#!/usr/bin/env python3
"""
smoke.py — Phase-17 Python client smoke test.

Runs a minimal ingest → retrieve → feedback round-trip against a live Stowage
server to verify the stdlib client works end-to-end.

Usage:
    python3 clients/python/smoke.py <server_url> <api_key>

Exit codes:
    0  all checks passed
    1  one or more checks failed
    2  usage error
"""
from __future__ import annotations

import sys
import os

# Allow running from the repo root without installing the package.
_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from stowage_client import (
    StowageClient,
    IngestRequest,
    RecordInput,
    RetrieveRequest,
    FeedbackRequest,
    StowageError,
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_fails = 0


def ok(msg: str) -> None:
    print(f"OK   {msg}")


def fail(msg: str) -> None:
    global _fails
    print(f"FAIL {msg}", file=sys.stderr)
    _fails += 1


# ---------------------------------------------------------------------------
# Smoke checks
# ---------------------------------------------------------------------------

def run(server_url: str, api_key: str) -> None:
    client = StowageClient(server_url, api_key)

    # ── AC-5.1: ingest ────────────────────────────────────────────────────
    session = "smoke-py-session"
    try:
        resp = client.ingest(IngestRequest(records=[
            RecordInput(
                role="user",
                content="The project deadline is next Friday.",
                session_id=session,
            ),
            RecordInput(
                role="assistant",
                content="Understood — I will prioritise accordingly.",
                session_id=session,
            ),
        ]))
        if len(resp.ids) == 2:
            ok(f"ingest: 2 records accepted (ids={resp.ids[:1]}...)")
        else:
            fail(f"ingest: expected 2 ids, got {len(resp.ids)}")
    except StowageError as exc:
        fail(f"ingest: HTTP {exc.status}: {exc.body}")
        return
    except Exception as exc:  # noqa: BLE001
        fail(f"ingest: unexpected error: {exc}")
        return

    # ── AC-5.2: retrieve ──────────────────────────────────────────────────
    response_id = ""
    try:
        rresp = client.retrieve(RetrieveRequest(
            query="project deadline",
            limit=5,
            session_id=session,
        ))
        response_id = rresp.response_id
        # In offline/mock mode the response may be degraded with zero items —
        # that is acceptable per AC-2 ("degraded retrieval allowed").
        ok(
            f"retrieve: response_id={response_id!r} "
            f"items={len(rresp.items)} degraded={rresp.degraded}"
        )
    except StowageError as exc:
        fail(f"retrieve: HTTP {exc.status}: {exc.body}")
        return
    except Exception as exc:  # noqa: BLE001
        fail(f"retrieve: unexpected error: {exc}")
        return

    # ── AC-5.3: feedback (only when we have a response_id) ───────────────
    if response_id:
        try:
            fresp = client.feedback(FeedbackRequest(
                signal="use",
                response_id=response_id,
            ))
            ok(f"feedback: applied={fresp.applied} signal={fresp.signal!r}")
        except StowageError as exc:
            fail(f"feedback: HTTP {exc.status}: {exc.body}")
        except Exception as exc:  # noqa: BLE001
            fail(f"feedback: unexpected error: {exc}")
    else:
        ok("feedback: skipped (no response_id from retrieve — degraded mode)")

    # ── AC-5.4: topics ────────────────────────────────────────────────────
    try:
        tresp = client.topics()
        ok(f"topics: {len(tresp.topics)} topic(s) returned")
    except StowageError as exc:
        fail(f"topics: HTTP {exc.status}: {exc.body}")
    except Exception as exc:  # noqa: BLE001
        fail(f"topics: unexpected error: {exc}")


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main() -> int:
    if len(sys.argv) != 3:
        print(
            f"Usage: {sys.argv[0]} <server_url> <api_key>",
            file=sys.stderr,
        )
        return 2

    server_url, api_key = sys.argv[1], sys.argv[2]
    run(server_url, api_key)

    if _fails == 0:
        print(f"python smoke: all checks passed")
        return 0
    print(f"python smoke: {_fails} check(s) FAILED", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
