"""
stowage_client.py — Stowage Python SDK (stdlib-only)

A minimal HTTP client for the Stowage memory API.  Uses only stdlib
(urllib, json, dataclasses) — no third-party dependencies.

Usage:

    from stowage_client import StowageClient, IngestRequest, RecordInput

    client = StowageClient("http://localhost:8080", "sk_...")
    resp = client.ingest(IngestRequest(records=[RecordInput(role="user", content="hello")]))
    print(resp.ids)

Retry behaviour: 5xx responses are retried up to MAX_RETRIES times with
exponential back-off (base 0.5 s, multiplier 2, capped at 8 s).
"""

from __future__ import annotations

import json
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field, asdict
from typing import Any, Dict, List, Optional


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

MAX_RETRIES: int = 3
BACKOFF_BASE: float = 0.5   # seconds
BACKOFF_CAP: float = 8.0    # seconds
DEFAULT_TIMEOUT: float = 30.0  # seconds


# ---------------------------------------------------------------------------
# Request / response dataclasses
# ---------------------------------------------------------------------------

@dataclass
class RecordInput:
    content: str
    role: str = "user"
    session_id: str = ""
    branch_id: str = ""
    project_id: str = ""
    user_id: str = ""
    source_agent: str = ""
    response_id: str = ""
    outcome: str = ""
    outcome_detail: str = ""
    occurred_at: int = 0
    buffer_key: str = ""


@dataclass
class IngestRequest:
    records: List[RecordInput] = field(default_factory=list)


@dataclass
class IngestResponse:
    ids: List[str]
    enqueued: bool


@dataclass
class RetrieveRequest:
    query: str
    limit: int = 10
    session_id: str = ""
    profile: str = ""
    debug: bool = False
    response_id: str = ""
    include_lanes: bool = False


@dataclass
class MemoryItem:
    id: str
    kind: str
    content: str
    context: str
    score: float
    citation: str
    lanes: Optional[List[str]] = None
    breakdown: Optional[Dict[str, Any]] = None


@dataclass
class RetrieveResponse:
    response_id: str
    items: List[MemoryItem]
    degraded: bool
    cache_hit: bool
    api: str


@dataclass
class FeedbackRequest:
    signal: str
    response_id: str = ""
    memory_id: str = ""
    citation: str = ""


@dataclass
class FeedbackResponse:
    applied: int
    signal: str


@dataclass
class TopicView:
    key: str
    description: str
    status: str
    pack: str
    source: str


@dataclass
class TopicsResponse:
    topics: List[TopicView]


# ---------------------------------------------------------------------------
# Client
# ---------------------------------------------------------------------------

class StowageError(Exception):
    """Raised when the server returns a non-2xx status after retries."""
    def __init__(self, status: int, body: str) -> None:
        super().__init__(f"stowage: HTTP {status}: {body}")
        self.status = status
        self.body = body


class StowageClient:
    """
    HTTP client for the Stowage memory API.

    Parameters
    ----------
    base_url : str
        Server base URL, e.g. ``http://localhost:8080``.
    api_key : str
        Bearer token (sk_...) issued by the Stowage key-management endpoint.
    timeout : float
        Per-request timeout in seconds (default 30).
    """

    def __init__(self, base_url: str, api_key: str, timeout: float = DEFAULT_TIMEOUT) -> None:
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.timeout = timeout

    # ------------------------------------------------------------------
    # Public methods
    # ------------------------------------------------------------------

    def ingest(self, req: IngestRequest) -> IngestResponse:
        """Append conversation records (fire-and-forget; durable on ACK)."""
        payload = {"records": [_clean(asdict(r)) for r in req.records]}
        data = self._post("/v1/records", payload)
        return IngestResponse(
            ids=data.get("ids", []),
            enqueued=data.get("enqueued", False),
        )

    def retrieve(self, req: RetrieveRequest) -> RetrieveResponse:
        """Four-lane fusion retrieval."""
        payload = _clean(asdict(req))
        data = self._post("/v1/retrieve", payload)
        items = [
            MemoryItem(
                id=i.get("id", ""),
                kind=i.get("kind", ""),
                content=i.get("content", ""),
                context=i.get("context", ""),
                score=float(i.get("score", 0.0)),
                citation=i.get("citation", ""),
                lanes=i.get("lanes"),
                breakdown=i.get("breakdown"),
            )
            for i in data.get("items", [])
        ]
        return RetrieveResponse(
            response_id=data.get("response_id", ""),
            items=items,
            degraded=data.get("degraded", False),
            cache_hit=data.get("cache_hit", False),
            api=data.get("api", "v1"),
        )

    def feedback(self, req: FeedbackRequest) -> FeedbackResponse:
        """Apply a quality signal to a retrieval response, memory, or citation."""
        payload = _clean(asdict(req))
        data = self._post("/v1/feedback", payload)
        return FeedbackResponse(
            applied=data.get("applied", 0),
            signal=data.get("signal", req.signal),
        )

    def topics(self) -> TopicsResponse:
        """List the effective memory topics for this agent's scope."""
        data = self._get("/v1/topics")
        views = [
            TopicView(
                key=t.get("key", ""),
                description=t.get("description", ""),
                status=t.get("status", ""),
                pack=t.get("pack", ""),
                source=t.get("source", ""),
            )
            for t in data.get("topics", [])
        ]
        return TopicsResponse(topics=views)

    # ------------------------------------------------------------------
    # HTTP helpers
    # ------------------------------------------------------------------

    def _post(self, path: str, payload: dict) -> dict:
        return self._request("POST", path, payload)

    def _get(self, path: str) -> dict:
        return self._request("GET", path, None)

    def _request(self, method: str, path: str, payload: Optional[dict]) -> dict:
        url = self.base_url + path
        body = json.dumps(payload).encode() if payload is not None else None
        headers = {
            "Authorization": f"Bearer {self.api_key}",
            "Content-Type": "application/json",
            "Accept": "application/json",
        }

        last_exc: Optional[Exception] = None
        for attempt in range(MAX_RETRIES + 1):
            if attempt > 0:
                delay = min(BACKOFF_BASE * (2 ** (attempt - 1)), BACKOFF_CAP)
                time.sleep(delay)

            req = urllib.request.Request(url, data=body, headers=headers, method=method)
            try:
                with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                    raw = resp.read()
                    return json.loads(raw) if raw else {}
            except urllib.error.HTTPError as exc:
                raw = exc.read()
                text = raw.decode("utf-8", errors="replace") if raw else ""
                if exc.code < 500:
                    # 4xx: do not retry
                    raise StowageError(exc.code, text) from exc
                last_exc = StowageError(exc.code, text)
            except (urllib.error.URLError, OSError) as exc:
                last_exc = exc

        raise last_exc  # type: ignore[misc]


# ---------------------------------------------------------------------------
# Private helpers
# ---------------------------------------------------------------------------

def _clean(d: dict) -> dict:
    """Remove falsy/empty values to keep request payloads compact."""
    return {k: v for k, v in d.items() if v or v == 0}
