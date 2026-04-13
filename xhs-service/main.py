"""
xhs-service: Xiaohongshu content fetching microservice.

Provides HTTP API for the Go backend (numind-server) to fetch
Xiaohongshu blogger notes, note details, and download videos.

Uses xiaohongshu-cli (pip install xiaohongshu-cli) for XHS API access.
Cookies are provided per-request via X-XHS-Cookies header (JSON format),
or via QR code login flow endpoints.
"""

import json
import os
import random
import tempfile
import threading
import time
from datetime import datetime
from typing import Any, Optional

import httpx
from fastapi import FastAPI, HTTPException, Query, Request
from fastapi.responses import FileResponse, JSONResponse

app = FastAPI(title="xhs-service", version="2.0.0")

# ========== QR Login Session Management ==========

# In-memory store for QR login sessions, keyed by qr_id.
# Each entry: {"client": XhsClient, "a1": str, "web_id": str, "qr_id": str, "code": str, "created_at": float}
_qr_sessions: dict[str, dict] = {}
_qr_sessions_lock = threading.Lock()

# Session expiry: 4 minutes
QR_SESSION_TTL_SECONDS = 240


def _generate_a1() -> str:
    """Generate a1 cookie value (from xiaohongshu-cli source)."""
    prefix = "".join(random.choices("0123456789abcdef", k=24))
    ts = str(int(time.time() * 1000))
    suffix = "".join(random.choices("0123456789abcdef", k=15))
    return prefix + ts + suffix


def _generate_webid() -> str:
    """Generate webId cookie value."""
    return "".join(random.choices("0123456789abcdef", k=32))


def _cleanup_expired_sessions():
    """Remove QR sessions older than TTL."""
    now = time.time()
    with _qr_sessions_lock:
        expired = [qr_id for qr_id, sess in _qr_sessions.items()
                   if now - sess["created_at"] > QR_SESSION_TTL_SECONDS]
        for qr_id in expired:
            del _qr_sessions[qr_id]


def get_client_from_request(request: Request):
    """Create XhsClient per-request from X-XHS-Cookies header."""
    from xhs_cli.client import XhsClient

    cookies_header = request.headers.get("X-XHS-Cookies", "")
    if not cookies_header:
        raise HTTPException(status_code=401, detail="Missing X-XHS-Cookies header")
    try:
        cookies = json.loads(cookies_header)
    except json.JSONDecodeError:
        raise HTTPException(
            status_code=400,
            detail="X-XHS-Cookies is not valid JSON. Expected format: {\"web_session\": \"...\", ...}",
        )
    return XhsClient(cookies=cookies, timeout=30.0, request_delay=2.0, max_retries=3)


# ========== Health ==========

@app.get("/health")
async def health():
    _cleanup_expired_sessions()
    return {
        "status": "ok",
        "timestamp": datetime.now().isoformat(),
        "active_qr_sessions": len(_qr_sessions),
    }


# ========== QR Code Login Endpoints ==========

@app.post("/xhs/qr/create")
async def create_qr_login():
    """Start QR code login flow. Returns qr_id, code, and qr_url for the frontend to display."""
    from xhs_cli.client import XhsClient

    _cleanup_expired_sessions()

    a1 = _generate_a1()
    web_id = _generate_webid()

    client = XhsClient(
        cookies={"a1": a1, "webId": web_id},
        timeout=30.0,
        request_delay=2.0,
        max_retries=3,
    )

    try:
        # Activate the session
        client.login_activate()

        # Create QR login
        qr_res = client.create_qr_login()
        qr_id = qr_res.get("qr_id", "")
        code = qr_res.get("code", "")
        url = qr_res.get("url", "")

        if not qr_id or not code:
            raise HTTPException(status_code=502, detail="Failed to create QR login: missing qr_id or code")

        # Store session
        with _qr_sessions_lock:
            _qr_sessions[qr_id] = {
                "client": client,
                "a1": a1,
                "web_id": web_id,
                "qr_id": qr_id,
                "code": code,
                "created_at": time.time(),
            }

        return {"qr_id": qr_id, "code": code, "qr_url": url}

    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"Failed to create QR login: {str(e)}")


@app.get("/xhs/qr/status/{qr_id}")
async def check_qr_status(qr_id: str):
    """Poll QR scan status. Returns status: 0=waiting, 1=scanned, 2=confirmed."""
    with _qr_sessions_lock:
        session = _qr_sessions.get(qr_id)

    if not session:
        raise HTTPException(status_code=404, detail="QR session not found or expired")

    # Check if expired
    if time.time() - session["created_at"] > QR_SESSION_TTL_SECONDS:
        with _qr_sessions_lock:
            _qr_sessions.pop(qr_id, None)
        raise HTTPException(status_code=410, detail="QR session expired")

    try:
        client = session["client"]
        code = session["code"]
        result = client.check_qr_status(qr_id, code)

        # Normalize status from the client response
        status = result.get("status", 0)
        message_map = {0: "waiting", 1: "scanned", 2: "confirmed"}
        message = message_map.get(status, "unknown")

        return {"status": status, "message": message}

    except Exception as e:
        raise HTTPException(status_code=502, detail=f"Failed to check QR status: {str(e)}")


@app.post("/xhs/qr/complete/{qr_id}")
async def complete_qr_login(qr_id: str):
    """Complete QR login after user confirms. Returns cookies and user info."""
    with _qr_sessions_lock:
        session = _qr_sessions.get(qr_id)

    if not session:
        raise HTTPException(status_code=404, detail="QR session not found or expired")

    try:
        client = session["client"]
        code = session["code"]
        a1 = session["a1"]
        web_id = session["web_id"]

        # Complete login — this fetches the final session cookies
        login_result = client.complete_qr_login(qr_id, code)

        # Extract cookies from client session
        # The client should now have web_session and web_session_sec cookies set
        client_cookies = {}
        if hasattr(client, "session") and hasattr(client.session, "cookies"):
            for cookie in client.session.cookies.jar:
                client_cookies[cookie.name] = cookie.value
        elif hasattr(client, "_session") and hasattr(client._session, "cookies"):
            for cookie in client._session.cookies.jar:
                client_cookies[cookie.name] = cookie.value

        # Build final cookie dict
        cookies = {
            "a1": a1,
            "webId": web_id,
        }
        # Merge any cookies from the client session
        for key in ["web_session", "web_session_sec", "websectiga", "sec_poison_id"]:
            if key in client_cookies:
                cookies[key] = client_cookies[key]

        # Also check login_result for cookies
        if isinstance(login_result, dict):
            for key in ["web_session", "web_session_sec"]:
                if key in login_result:
                    cookies[key] = login_result[key]

        # Validate that web_session cookie was obtained
        if "web_session" not in cookies or not cookies["web_session"]:
            raise HTTPException(
                status_code=502,
                detail="Login completed but web_session cookie not found. Please try again.",
            )

        # Extract user info if available
        user_id = ""
        nickname = ""
        if isinstance(login_result, dict):
            user_id = login_result.get("user_id", "")
            nickname = login_result.get("nickname", "")

        # Clean up session
        with _qr_sessions_lock:
            _qr_sessions.pop(qr_id, None)

        return {
            "cookies": cookies,
            "user_id": user_id,
            "nickname": nickname,
        }

    except HTTPException:
        raise
    except Exception as e:
        # Clean up on failure too
        with _qr_sessions_lock:
            _qr_sessions.pop(qr_id, None)
        raise HTTPException(status_code=502, detail=f"Failed to complete QR login: {str(e)}")


# ========== Data Endpoints (per-request cookies via X-XHS-Cookies header) ==========

@app.get("/xhs/user-info/{user_id}")
async def get_user_info(user_id: str, request: Request):
    """Fetch XHS user profile (nickname, avatar, bio, followers)."""
    client = get_client_from_request(request)
    try:
        result = client.get_user_info(user_id)
        if not result:
            raise HTTPException(status_code=404, detail=f"XHS user {user_id} not found")

        # Extract key fields from response
        user_data = result.get("basic_info", result)
        return {
            "user_id": user_id,
            "nickname": user_data.get("nickname", ""),
            "avatar": user_data.get("image", user_data.get("images", "")),
            "desc": user_data.get("desc", ""),
            "fans": user_data.get("fans", 0),
            "follows": user_data.get("follows", 0),
            "notes": user_data.get("notes", 0),
            "raw": result,
        }
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"Failed to fetch user info: {str(e)}")


@app.get("/xhs/user-notes/{user_id}")
async def get_user_notes(user_id: str, request: Request, cursor: str = "", limit: int = Query(default=20, le=50)):
    """
    Fetch latest notes for a given XHS user.
    Returns a list of note summaries.
    """
    client = get_client_from_request(request)
    try:
        result = client.get_user_notes(user_id, cursor=cursor)

        # Parse notes from response
        notes_data = result.get("notes", [])
        has_more = result.get("has_more", False)
        next_cursor = result.get("cursor", "")

        notes = []
        for note in notes_data[:limit]:
            notes.append(
                {
                    "note_id": note.get("note_id", ""),
                    "title": note.get("display_title", note.get("title", "")),
                    "note_type": "video" if note.get("type") == "video" else "image",
                    "likes": int(note.get("interact_info", {}).get("liked_count", 0) or 0),
                    "cover": note.get("cover", {}).get("url", ""),
                }
            )

        return {
            "user_id": user_id,
            "notes": notes,
            "total": len(notes),
            "has_more": has_more,
            "cursor": next_cursor,
        }
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"Failed to fetch user notes: {str(e)}")


@app.get("/xhs/note-detail/{note_id}")
async def get_note_detail(note_id: str, request: Request, xsec_token: str = "", xsec_source: str = ""):
    """
    Fetch full note details including content, images, video URL.
    """
    client = get_client_from_request(request)
    try:
        # Try get_note_by_id first (more reliable), fall back to get_note_detail
        try:
            result = client.get_note_by_id(note_id, xsec_token=xsec_token, xsec_source=xsec_source or "pc_feed")
        except Exception:
            result = client.get_note_detail(note_id, xsec_token=xsec_token, xsec_source=xsec_source)

        if not result:
            raise HTTPException(status_code=404, detail=f"Note {note_id} not found")

        # Extract note data — structure varies, handle common patterns
        note = result.get("note_card", result.get("data", result))
        interact = note.get("interact_info", {})

        # Extract images
        image_list = note.get("image_list", [])
        images = []
        for img in image_list:
            url = img.get("url_default", img.get("url", ""))
            if url:
                images.append(url)

        # Extract video URL
        video_url = ""
        video = note.get("video", {})
        if video:
            # Try different video URL fields
            for key in ["url", "h264", "h265", "av1"]:
                v = video.get(key)
                if isinstance(v, list) and v:
                    video_url = v[0].get("url", v[0].get("master_url", ""))
                    break
                elif isinstance(v, str) and v:
                    video_url = v
                    break

        # Extract tags
        tag_list = note.get("tag_list", [])
        tags = [t.get("name", "") for t in tag_list if t.get("name")]

        return {
            "note_id": note_id,
            "title": note.get("title", note.get("display_title", "")),
            "content": note.get("desc", note.get("content", "")),
            "note_type": "video" if video_url or note.get("type") == "video" else "image",
            "tags": tags,
            "likes": int(interact.get("liked_count", 0) or 0),
            "comments": int(interact.get("comment_count", 0) or 0),
            "collects": int(interact.get("collected_count", 0) or 0),
            "shares": int(interact.get("share_count", 0) or 0),
            "images": images,
            "video_url": video_url,
            "published_at": note.get("time", note.get("create_time", "")),
            "user": {
                "user_id": note.get("user", {}).get("user_id", ""),
                "nickname": note.get("user", {}).get("nickname", ""),
            },
            "raw": result,
        }
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"Failed to fetch note detail: {str(e)}")


@app.get("/xhs/download-video/{note_id}")
async def download_video(note_id: str, request: Request, xsec_token: str = "", xsec_source: str = ""):
    """
    Download video for a given note and return as file.
    First fetches note detail to get video URL, then downloads the video.
    """
    # Get note detail to find video URL
    client = get_client_from_request(request)
    try:
        try:
            result = client.get_note_by_id(note_id, xsec_token=xsec_token, xsec_source=xsec_source or "pc_feed")
        except Exception:
            result = client.get_note_detail(note_id, xsec_token=xsec_token, xsec_source=xsec_source)
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"Failed to fetch note for video download: {str(e)}")

    note = result.get("note_card", result.get("data", result))
    video = note.get("video", {})
    video_url = ""
    for key in ["url", "h264", "h265", "av1"]:
        v = video.get(key)
        if isinstance(v, list) and v:
            video_url = v[0].get("url", v[0].get("master_url", ""))
            break
        elif isinstance(v, str) and v:
            video_url = v
            break

    if not video_url:
        raise HTTPException(status_code=404, detail=f"No video found for note {note_id}")

    # Download video to temp file
    try:
        async with httpx.AsyncClient(timeout=120.0) as http_client:
            resp = await http_client.get(video_url, follow_redirects=True)
            resp.raise_for_status()

            tmp = tempfile.NamedTemporaryFile(delete=False, suffix=".mp4", dir="/tmp")
            tmp.write(resp.content)
            tmp.close()

            return FileResponse(
                tmp.name,
                media_type="video/mp4",
                filename=f"{note_id}.mp4",
                background=None,  # Don't delete after response — caller cleans up
            )
    except httpx.HTTPError as e:
        raise HTTPException(status_code=502, detail=f"Failed to download video: {str(e)}")


if __name__ == "__main__":
    import uvicorn

    port = int(os.environ.get("PORT", 8100))
    uvicorn.run(app, host="0.0.0.0", port=port)
