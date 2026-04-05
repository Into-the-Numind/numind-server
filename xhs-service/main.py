"""
xhs-service: Xiaohongshu content fetching microservice.

Provides HTTP API for the Go backend (numind-server) to fetch
Xiaohongshu blogger notes, note details, and download videos.

Uses xiaohongshu-cli (pip install xiaohongshu-cli) for XHS API access.
Cookie must be provided via XHS_COOKIE environment variable (JSON format).
"""

import json
import os
import tempfile
import time
from datetime import datetime
from typing import Any, Optional

import httpx
from fastapi import FastAPI, HTTPException, Query
from fastapi.responses import FileResponse, JSONResponse

app = FastAPI(title="xhs-service", version="1.0.0")

# Global XhsClient instance (lazy init)
_xhs_client = None


def get_xhs_client():
    """Get or create XhsClient instance from XHS_COOKIE env var."""
    global _xhs_client
    if _xhs_client is not None:
        return _xhs_client

    cookie_str = os.environ.get("XHS_COOKIE", "")
    if not cookie_str:
        raise HTTPException(
            status_code=503,
            detail="XHS_COOKIE environment variable not set. Provide XHS cookies as JSON dict.",
        )

    try:
        cookies = json.loads(cookie_str)
    except json.JSONDecodeError:
        raise HTTPException(
            status_code=503,
            detail="XHS_COOKIE is not valid JSON. Expected format: {\"web_session\": \"...\", ...}",
        )

    from xhs_cli.client import XhsClient

    _xhs_client = XhsClient(
        cookies=cookies,
        timeout=30.0,
        request_delay=2.0,  # 2s delay between requests to avoid rate limiting
        max_retries=3,
    )
    return _xhs_client


def reset_client():
    """Reset client (e.g., when cookie is updated)."""
    global _xhs_client
    _xhs_client = None


@app.get("/health")
async def health():
    has_cookie = bool(os.environ.get("XHS_COOKIE"))
    return {
        "status": "ok",
        "timestamp": datetime.now().isoformat(),
        "cookie_configured": has_cookie,
    }


@app.post("/xhs/reset-cookie")
async def reset_cookie():
    """Reset XhsClient to pick up new XHS_COOKIE value."""
    reset_client()
    return {"status": "ok", "message": "Client reset. Will use new cookie on next request."}


@app.get("/xhs/user-info/{user_id}")
async def get_user_info(user_id: str):
    """Fetch XHS user profile (nickname, avatar, bio, followers)."""
    client = get_xhs_client()
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
async def get_user_notes(user_id: str, cursor: str = "", limit: int = Query(default=20, le=50)):
    """
    Fetch latest notes for a given XHS user.
    Returns a list of note summaries.
    """
    client = get_xhs_client()
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
                    "likes": note.get("interact_info", {}).get("liked_count", "0"),
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
async def get_note_detail(note_id: str, xsec_token: str = "", xsec_source: str = ""):
    """
    Fetch full note details including content, images, video URL.
    """
    client = get_xhs_client()
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
            "likes": interact.get("liked_count", "0"),
            "comments": interact.get("comment_count", "0"),
            "collects": interact.get("collected_count", "0"),
            "shares": interact.get("share_count", "0"),
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
async def download_video(note_id: str, xsec_token: str = "", xsec_source: str = ""):
    """
    Download video for a given note and return as file.
    First fetches note detail to get video URL, then downloads the video.
    """
    # Get note detail to find video URL
    client = get_xhs_client()
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
