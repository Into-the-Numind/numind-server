"""
xhs-service: Xiaohongshu content fetching microservice.

Provides HTTP API for the Go backend (numind-server) to fetch
Xiaohongshu blogger notes, note details, and download videos.

Currently returns mock data. Real implementation will be added
when xiaohongshu-cli integration is set up.
"""

import os
import time
from datetime import datetime, timedelta
from typing import Optional

from fastapi import FastAPI, HTTPException, Query
from fastapi.responses import JSONResponse

app = FastAPI(title="xhs-service", version="0.1.0")


@app.get("/health")
async def health():
    return {"status": "ok", "timestamp": datetime.now().isoformat()}


@app.get("/xhs/user-notes/{user_id}")
async def get_user_notes(user_id: str, limit: int = Query(default=20, le=50)):
    """
    Fetch latest notes for a given XHS user.
    Returns a list of note summaries.
    """
    # TODO: Replace with real xiaohongshu-cli integration
    # For now, return empty list (no mock data that could mislead)
    return {
        "user_id": user_id,
        "notes": [],
        "total": 0,
        "message": "xhs-service placeholder: real integration pending"
    }


@app.get("/xhs/note-detail/{note_id}")
async def get_note_detail(note_id: str):
    """
    Fetch full note details including content, images, video URL.
    """
    # TODO: Replace with real xiaohongshu-cli integration
    raise HTTPException(
        status_code=501,
        detail="xhs-service placeholder: real integration pending"
    )


@app.get("/xhs/download-video/{note_id}")
async def download_video(note_id: str):
    """
    Download video for a given note and return as streaming response.
    """
    # TODO: Replace with real xiaohongshu-cli integration
    raise HTTPException(
        status_code=501,
        detail="xhs-service placeholder: real integration pending"
    )


if __name__ == "__main__":
    import uvicorn
    port = int(os.environ.get("PORT", 8100))
    uvicorn.run(app, host="0.0.0.0", port=port)
