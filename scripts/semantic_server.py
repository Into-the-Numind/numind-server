#!/usr/bin/env python3
"""
Semantic Splitter Microservice
Loads the model once and serves splitting requests via HTTP.
"""

import sys
import os

import uvicorn
from fastapi import FastAPI
from pydantic import BaseModel, Field
from typing import List, Optional
import semantic_splitter
from semantic_splitter import semantic_split, get_model

# Initialize FastAPI app
app = FastAPI(title="Semantic Splitter Service")

# Request model
class SplitRequest(BaseModel):
    text: str
    threshold: float = Field(0.6, description="Similarity threshold")
    min_chunk_size: int = Field(100, description="Minimum chunk size")
    max_chunk_size: int = Field(1000, description="Maximum chunk size")
    overlap_size: int = Field(100, description="Overlap size")

# Response model
class SplitResponse(BaseModel):
    success: bool
    chunks: List[dict]
    total_chunks: int
    error: Optional[str] = None

@app.on_event("startup")
async def startup_event():
    """Load models on startup"""
    try:
        print("Loading semantic model on startup...", file=sys.stderr)
        get_model()
        print("Semantic model loaded successfully!", file=sys.stderr)
    except Exception as e:
        print(f"Failed to load model during startup: {e}", file=sys.stderr)

@app.post("/split", response_model=SplitResponse)
async def split_text(req: SplitRequest):
    try:
        chunks = semantic_split(
            req.text, 
            req.threshold, 
            req.min_chunk_size, 
            req.max_chunk_size,
            req.overlap_size
        )
        return {
            "success": True, 
            "chunks": chunks,
            "total_chunks": len(chunks)
        }
    except Exception as e:
        print(f"Split error: {e}", file=sys.stderr)
        return {
            "success": False,
            "chunks": [],
            "total_chunks": 0,
            "error": str(e)
        }

@app.get("/health")
async def health_check():
    return {
        "status": "ok",
        "model_ready": semantic_splitter._model is not None,
        "semantic_model_loaded": semantic_splitter._model is not None
    }

if __name__ == "__main__":
    # Run server
    uvicorn.run(app, host="0.0.0.0", port=9093)
