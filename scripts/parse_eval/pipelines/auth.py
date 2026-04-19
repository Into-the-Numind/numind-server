import os

import requests


def login(base_url: str) -> str:
    """Call /v1/web/login, return access_token. Reads E2E_USERNAME/E2E_PASSWORD env vars."""
    username = os.environ["E2E_USERNAME"]
    password = os.environ["E2E_PASSWORD"]
    resp = requests.post(
        f"{base_url.rstrip('/')}/v1/web/login",
        json={"username": username, "password": password},
        timeout=10,
    )
    resp.raise_for_status()
    body = resp.json()
    if body.get("code") != 0:
        raise RuntimeError(f"login failed: {body}")
    return body["data"]["access_token"]
