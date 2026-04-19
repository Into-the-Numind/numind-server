import time
from pathlib import Path

import requests

from .base import Pipeline, ParseResult

POLL_INTERVAL = 3
POLL_TIMEOUT = 300


class SalesRagPipeline(Pipeline):
    name = "salesrag"

    def __init__(self, base_url: str, token: str):
        self.base_url = base_url.rstrip("/")
        self.headers = {"Authorization": f"Bearer {token}"}

    def parse(self, sample_path: Path) -> ParseResult:
        started = time.time()
        try:
            with open(sample_path, "rb") as f:
                up = requests.post(
                    f"{self.base_url}/v1/sales-rag/ingest",
                    headers=self.headers,
                    files={"file": (sample_path.name, f)},
                    timeout=120,
                )
            up.raise_for_status()
            body = up.json()
            if body.get("code") != 0:
                return ParseResult(self.name, sample_path, "", f"api_error: {body}", time.time() - started)
            doc_id = body["data"]["document_id"]

            deadline = time.time() + POLL_TIMEOUT
            status = "PENDING"
            while time.time() < deadline:
                st = requests.get(
                    f"{self.base_url}/v1/sales-rag/documents/{doc_id}",
                    headers=self.headers,
                    timeout=10,
                )
                st.raise_for_status()
                status = st.json().get("data", {}).get("status", "UNKNOWN")
                if status == "COMPLETED":
                    break
                if status == "FAILED":
                    return ParseResult(self.name, sample_path, "", "pipeline_failed", time.time() - started)
                time.sleep(POLL_INTERVAL)
            if status != "COMPLETED":
                return ParseResult(self.name, sample_path, "", f"poll_timeout status={status}", time.time() - started)

            ch = requests.get(
                f"{self.base_url}/v1/sales-rag/documents/{doc_id}/chunks",
                headers=self.headers,
                params={"limit": 10000},
                timeout=30,
            )
            ch.raise_for_status()
            chunks = ch.json().get("data", [])
            if not isinstance(chunks, list):
                chunks = chunks.get("list", []) if isinstance(chunks, dict) else []
            content = "\n\n".join(c.get("content", "") for c in chunks)
            return ParseResult(self.name, sample_path, content, None, time.time() - started)
        except Exception as e:
            return ParseResult(self.name, sample_path, "", f"exception: {e}", time.time() - started)
