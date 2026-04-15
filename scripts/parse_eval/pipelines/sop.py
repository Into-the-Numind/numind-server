import time
import uuid
from pathlib import Path

import requests

from .base import Pipeline, ParseResult

SOP_ENDPOINT = "/v1/pdf/convert-to-text"


class SopPipeline(Pipeline):
    name = "sop"

    def __init__(self, base_url: str, token: str):
        self.base_url = base_url.rstrip("/")
        self.token = token

    def parse(self, sample_path: Path) -> ParseResult:
        started = time.time()
        run_id = f"eval-{uuid.uuid4().hex[:8]}"
        node_id = "eval-node"
        try:
            with open(sample_path, "rb") as f:
                resp = requests.post(
                    f"{self.base_url}{SOP_ENDPOINT}",
                    headers={"Authorization": f"Bearer {self.token}"},
                    data={"run_id": run_id, "node_id": node_id},
                    files={"file": (sample_path.name, f)},
                    timeout=120,
                )
            resp.raise_for_status()
            body = resp.json()
            if body.get("code") != 0:
                return ParseResult(self.name, sample_path, "", f"api_error: {body}", time.time() - started)
            data = body.get("data", "")
            content = data if isinstance(data, str) else data.get("content", "")
            return ParseResult(self.name, sample_path, content, None, time.time() - started)
        except Exception as e:
            return ParseResult(self.name, sample_path, "", f"exception: {e}", time.time() - started)
