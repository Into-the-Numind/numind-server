import time
import uuid
from pathlib import Path

import requests

from .base import Pipeline, ParseResult


class ChatbotPipeline(Pipeline):
    name = "chatbot"

    def __init__(self, base_url: str, token: str):
        self.base_url = base_url.rstrip("/")
        self.headers = {"Authorization": f"Bearer {token}"}

    def _create_kb(self) -> int:
        resp = requests.post(
            f"{self.base_url}/v1/config/knowledge-bases",
            headers=self.headers,
            json={"name": f"eval-{uuid.uuid4().hex[:8]}", "description": "parse_eval temp"},
            timeout=10,
        )
        resp.raise_for_status()
        body = resp.json()
        if body.get("code") != 0:
            raise RuntimeError(f"create_kb failed: {body}")
        return body["data"]["id"]

    def _delete_kb(self, kb_id: int) -> None:
        try:
            requests.delete(
                f"{self.base_url}/v1/config/knowledge-bases/{kb_id}",
                headers=self.headers,
                timeout=10,
            )
        except Exception:
            pass

    def _fetch_doc_content(self, kb_id: int, doc_id: int) -> str:
        det = requests.get(
            f"{self.base_url}/v1/config/knowledge-bases/{kb_id}",
            headers=self.headers,
            timeout=30,
        )
        det.raise_for_status()
        data = det.json().get("data", {})
        for doc in data.get("documents", []):
            if doc.get("id") == doc_id:
                return doc.get("content", "")
        return ""

    def parse(self, sample_path: Path) -> ParseResult:
        started = time.time()
        kb_id = None
        try:
            kb_id = self._create_kb()
            with open(sample_path, "rb") as f:
                up = requests.post(
                    f"{self.base_url}/v1/config/knowledge-bases/{kb_id}/documents",
                    headers=self.headers,
                    files={"file": (sample_path.name, f)},
                    timeout=120,
                )
            up.raise_for_status()
            body = up.json()
            if body.get("code") != 0:
                return ParseResult(self.name, sample_path, "", f"api_error: {body}", time.time() - started)
            results = body.get("data", {}).get("results", [])
            if not results:
                return ParseResult(self.name, sample_path, "", f"no_upload_results: {body}", time.time() - started)
            first = results[0]
            if not first.get("success", True):
                return ParseResult(self.name, sample_path, "", f"upload_failed: {first}", time.time() - started)

            content = first.get("content", "")
            if not content:
                doc_id = first.get("document_id")
                if doc_id is not None:
                    content = self._fetch_doc_content(kb_id, doc_id)

            return ParseResult(self.name, sample_path, content, None, time.time() - started)
        except Exception as e:
            return ParseResult(self.name, sample_path, "", f"exception: {e}", time.time() - started)
        finally:
            if kb_id is not None:
                self._delete_kb(kb_id)
