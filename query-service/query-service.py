#!/usr/bin/env python3
"""
Kubemoot RAG Query Service
A FastAPI service for semantic search against vector stores.
Auto-deployed by the operator for each RAGSource.
"""

import os
import re
import time
import logging
from pathlib import Path
from typing import Optional
from contextlib import asynccontextmanager
from urllib.parse import urlparse, parse_qs

# Read version from VERSION file
VERSION_FILE = Path(__file__).parent / "VERSION"
VERSION = VERSION_FILE.read_text().strip() if VERSION_FILE.exists() else "0.0.0-dev"

from fastapi import FastAPI, HTTPException, Request
from pydantic import BaseModel, Field
import httpx
import psycopg2
from psycopg2 import sql
from psycopg2.extras import RealDictCursor

# Configure logging
logging.basicConfig(
    level=os.environ.get("LOG_LEVEL", "INFO"),
    format='{"timestamp":"%(asctime)s","level":"%(levelname)s","logger":"%(name)s","message":"%(message)s"}',
    datefmt='%Y-%m-%dT%H:%M:%SZ'
)
logger = logging.getLogger("kubemoot-query")

# Metrics
metrics = {
    "queries_total": 0,
    "queries_success": 0,
    "queries_error": 0,
    "embedding_time_total_ms": 0,
    "search_time_total_ms": 0,
}


def parse_postgres_endpoint(endpoint: str) -> dict:
    """
    Parse PostgreSQL connection string.
    Supports formats:
    - postgresql://user:pass@host:5432/dbname
    - postgres://user:pass@host:5432/dbname
    - host:port (uses defaults for user/pass/db)
    """
    if endpoint.startswith(("postgresql://", "postgres://")):
        parsed = urlparse(endpoint)
        return {
            "host": parsed.hostname or "localhost",
            "port": parsed.port or 5432,
            "dbname": parsed.path.lstrip("/") or "vectors",
            "user": parsed.username or "postgres",
            "password": parsed.password or "",
        }
    elif ":" in endpoint:
        # Handle host:port or host:port/database format
        parts = endpoint.split(":")
        port_and_db = parts[1].split("/") if len(parts) > 1 else ["5432"]
        port = int(port_and_db[0])
        dbname = port_and_db[1] if len(port_and_db) > 1 else os.environ.get("KUBEMOOT_DB_NAME", "vectors")
        return {
            "host": parts[0],
            "port": port,
            "dbname": dbname,
            "user": os.environ.get("KUBEMOOT_DB_USER", "postgres"),
            "password": os.environ.get("KUBEMOOT_DB_PASSWORD", ""),
        }
    else:
        return {
            "host": endpoint,
            "port": 5432,
            "dbname": os.environ.get("KUBEMOOT_DB_NAME", "vectors"),
            "user": os.environ.get("KUBEMOOT_DB_USER", "postgres"),
            "password": os.environ.get("KUBEMOOT_DB_PASSWORD", ""),
        }


# Configuration from environment (set by the operator controller)
VECTORSTORE_TYPE = os.environ.get("KUBEMOOT_VECTORSTORE_TYPE", "pgvector")
VECTORSTORE_ENDPOINT = os.environ.get("KUBEMOOT_VECTORSTORE_ENDPOINT", "")
COLLECTION = os.environ.get("KUBEMOOT_VECTORSTORE_COLLECTION",
                            os.environ.get("KUBEMOOT_COLLECTION", "default"))
DIMENSIONS = int(os.environ.get("KUBEMOOT_VECTORSTORE_DIMENSIONS", "768"))

EMBEDDING_TYPE = os.environ.get("KUBEMOOT_EMBEDDING_TYPE", "ollama")
EMBEDDING_ENDPOINT = os.environ.get("KUBEMOOT_EMBEDDING_ENDPOINT", "http://localhost:11434")
EMBEDDING_MODEL = os.environ.get("KUBEMOOT_EMBEDDING_MODEL", "nomic-embed-text")

DEFAULT_TOP_K = int(os.environ.get("KUBEMOOT_DEFAULT_TOP_K", "5"))
PORT = int(os.environ.get("KUBEMOOT_QUERY_PORT", os.environ.get("PORT", "8000")))

# Listen address. Defaults to 0.0.0.0 because this runs inside a Kubernetes pod and
# must be reachable via the Service ClusterIP; access is controlled by the k8s Service
# and NetworkPolicy, not the listen address. Override with KUBEMOOT_QUERY_HOST to
# restrict the bind (e.g. to 127.0.0.1 in non-cluster contexts).
HOST = os.environ.get("KUBEMOOT_QUERY_HOST", "0.0.0.0")

RAGSOURCE_NAME = os.environ.get("KUBEMOOT_RAGSOURCE_NAME", "unknown")
RAGSOURCE_NAMESPACE = os.environ.get("KUBEMOOT_RAGSOURCE_NAMESPACE", "default")


def get_db_config() -> dict:
    """
    Get database configuration from environment variables.
    Supports both new-style (KUBEMOOT_VECTORSTORE_ENDPOINT) and legacy (KUBEMOOT_DB_*) variables.
    """
    # New style: full endpoint URL or host:port
    if VECTORSTORE_ENDPOINT:
        return parse_postgres_endpoint(VECTORSTORE_ENDPOINT)

    # Legacy style: individual KUBEMOOT_DB_* variables
    db_host = os.environ.get("KUBEMOOT_DB_HOST", "localhost")
    db_port = int(os.environ.get("KUBEMOOT_DB_PORT", "5432"))
    db_name = os.environ.get("KUBEMOOT_DB_NAME", "vectors")
    db_user = os.environ.get("KUBEMOOT_DB_USER", "postgres")
    db_password = os.environ.get("KUBEMOOT_DB_PASSWORD", "")

    return {
        "host": db_host,
        "port": db_port,
        "dbname": db_name,
        "user": db_user,
        "password": db_password,
    }


# Parse database connection
db_config = get_db_config()

# Global connection
db_conn = None


def get_db_connection():
    """Get database connection with reconnection support."""
    global db_conn
    try:
        if db_conn is None or db_conn.closed:
            db_conn = psycopg2.connect(**db_config)
            db_conn.autocommit = True
        # Test connection
        with db_conn.cursor() as cur:
            cur.execute("SELECT 1")
        return db_conn
    except (psycopg2.OperationalError, psycopg2.InterfaceError):
        # Reconnect on connection errors
        db_conn = psycopg2.connect(**db_config)
        db_conn.autocommit = True
        return db_conn


async def get_embedding_ollama(text: str) -> list[float]:
    """Get embedding from Ollama."""
    async with httpx.AsyncClient(timeout=60.0) as client:
        response = await client.post(
            f"{EMBEDDING_ENDPOINT}/api/embed",
            json={"model": EMBEDDING_MODEL, "input": text}
        )
        response.raise_for_status()
        data = response.json()
        # Ollama returns embeddings in 'embeddings' array
        if "embeddings" in data and len(data["embeddings"]) > 0:
            return data["embeddings"][0]
        # Fallback for older API format
        elif "embedding" in data:
            return data["embedding"]
        else:
            raise ValueError(f"Unexpected Ollama response format: {data.keys()}")


async def get_embedding_openai(text: str) -> list[float]:
    """Get embedding from OpenAI-compatible API."""
    api_key = os.environ.get("KUBEMOOT_EMBEDDING_API_KEY", "")
    async with httpx.AsyncClient(timeout=60.0) as client:
        headers = {}
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"
        response = await client.post(
            f"{EMBEDDING_ENDPOINT}/v1/embeddings",
            headers=headers,
            json={"model": EMBEDDING_MODEL, "input": text}
        )
        response.raise_for_status()
        data = response.json()
        return data["data"][0]["embedding"]


async def get_embedding(text: str) -> list[float]:
    """Get embedding based on configured provider."""
    if EMBEDDING_TYPE == "ollama":
        return await get_embedding_ollama(text)
    elif EMBEDDING_TYPE in ("openai", "vllm"):
        return await get_embedding_openai(text)
    else:
        # Default to Ollama format
        return await get_embedding_ollama(text)


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Startup and shutdown logic."""
    logger.info("Starting Kubemoot RAG Query Service for %s", RAGSOURCE_NAME)
    logger.info("  Vector Store: %s @ %s:%s/%s", VECTORSTORE_TYPE,
                db_config['host'], db_config['port'], db_config['dbname'])
    logger.info("  Collection: %s", COLLECTION)
    logger.info("  Embedding: %s/%s @ %s", EMBEDDING_TYPE, EMBEDDING_MODEL, EMBEDDING_ENDPOINT)
    logger.info("  Default Top-K: %s", DEFAULT_TOP_K)

    # Test database connection
    try:
        get_db_connection()
        logger.info("  Database connection: OK")
    except Exception as e:
        logger.warning("  Database connection: FAILED (%s)", e)

    yield

    if db_conn and not db_conn.closed:
        db_conn.close()
    logger.info("Shutting down Kubemoot RAG Query Service")


app = FastAPI(
    title="Kubemoot RAG Query Service",
    description=f"Semantic search API for RAGSource '{RAGSOURCE_NAME}'",
    version=VERSION,
    lifespan=lifespan,
)


class QueryRequest(BaseModel):
    """Query request model."""
    query: str = Field(..., description="The search query text")
    top_k: Optional[int] = Field(None, description="Number of results to return", ge=1, le=100)
    threshold: Optional[float] = Field(None, description="Minimum similarity score (0-1)", ge=0, le=1)
    filter: Optional[dict] = Field(None, description="Metadata filters")


class SearchResult(BaseModel):
    """Individual search result."""
    content: str
    metadata: dict
    score: float = Field(..., description="Similarity score (higher is better)")


class QueryResponse(BaseModel):
    """Query response model."""
    query: str
    results: list[SearchResult]
    collection: str
    embedding_time_ms: int
    search_time_ms: int


class HealthResponse(BaseModel):
    """Health check response."""
    status: str
    ragsource: str
    vectorstore: str
    embedding: str


@app.get("/version")
async def version():
    """Return service version."""
    return {"version": VERSION, "service": "kubemoot-query-service"}


@app.get("/health", response_model=HealthResponse)
async def health():
    """Health check endpoint."""
    return HealthResponse(
        status="healthy",
        ragsource=RAGSOURCE_NAME,
        vectorstore=f"{VECTORSTORE_TYPE}:{COLLECTION}",
        embedding=f"{EMBEDDING_TYPE}/{EMBEDDING_MODEL}",
    )


@app.get("/ready")
async def ready():
    """Readiness check - verifies database and embedding service connectivity."""
    errors = []

    # Check database
    try:
        conn = get_db_connection()
        with conn.cursor() as cur:
            cur.execute("SELECT 1")
    except Exception as e:
        errors.append(f"database: {e}")

    # Check embedding service
    try:
        async with httpx.AsyncClient(timeout=5.0) as client:
            if EMBEDDING_TYPE == "ollama":
                response = await client.get(f"{EMBEDDING_ENDPOINT}/api/tags")
            else:
                response = await client.get(f"{EMBEDDING_ENDPOINT}/v1/models")
            response.raise_for_status()
    except Exception as e:
        errors.append(f"embedding: {e}")

    if errors:
        raise HTTPException(status_code=503, detail="; ".join(errors))

    return {"status": "ready", "checks": {"database": "ok", "embedding": "ok"}}


@app.post("/query", response_model=QueryResponse)
async def query(request: QueryRequest):
    """
    Perform semantic search against the vector store.

    The query text is embedded using the configured embedding model,
    then similarity search is performed against the vector store.
    """
    global metrics
    metrics["queries_total"] += 1

    top_k = request.top_k or DEFAULT_TOP_K
    # Quote table name to handle special characters like hyphens
    table_name = sql.Identifier(f"data_{COLLECTION}")

    query_preview = request.query[:80] + "..." if len(request.query) > 80 else request.query
    logger.info("Query: '%s' top_k=%s", query_preview, top_k)

    try:
        # Get embedding for query
        embed_start = time.time()
        embedding = await get_embedding(request.query)
        embed_time_ms = int((time.time() - embed_start) * 1000)
        metrics["embedding_time_total_ms"] += embed_time_ms

        # Search pgvector
        search_start = time.time()
        conn = get_db_connection()
        with conn.cursor(cursor_factory=RealDictCursor) as cur:
            # Format embedding as PostgreSQL vector literal
            embedding_str = "[" + ",".join(str(x) for x in embedding) + "]"

            # Build query with optional threshold using sql.SQL for safe formatting
            query_sql = sql.SQL("""
                SELECT
                    content,
                    metadata,
                    1 - (embedding <=> %s::vector) AS score
                FROM {}
            """).format(table_name)
            params = [embedding_str]

            if request.threshold is not None:
                query_sql = sql.SQL("""
                    SELECT
                        content,
                        metadata,
                        1 - (embedding <=> %s::vector) AS score
                    FROM {}
                    WHERE 1 - (embedding <=> %s::vector) >= %s
                    ORDER BY embedding <=> %s::vector LIMIT %s
                """).format(table_name)
                params = [embedding_str, embedding_str, request.threshold, embedding_str, top_k]
            else:
                query_sql = sql.SQL("""
                    SELECT
                        content,
                        metadata,
                        1 - (embedding <=> %s::vector) AS score
                    FROM {}
                    ORDER BY embedding <=> %s::vector LIMIT %s
                """).format(table_name)
                params = [embedding_str, embedding_str, top_k]

            cur.execute(query_sql, params)
            rows = cur.fetchall()

        search_time_ms = int((time.time() - search_start) * 1000)
        metrics["search_time_total_ms"] += search_time_ms

        results = []
        for row in rows:
            results.append(SearchResult(
                content=row["content"],
                metadata=row["metadata"] if row["metadata"] else {},
                score=float(row["score"]),
            ))

        metrics["queries_success"] += 1
        logger.info("Found %s results (embed=%sms, search=%sms)",
                    len(results), embed_time_ms, search_time_ms)

        return QueryResponse(
            query=request.query,
            results=results,
            collection=COLLECTION,
            embedding_time_ms=embed_time_ms,
            search_time_ms=search_time_ms,
        )

    except psycopg2.errors.UndefinedTable:
        metrics["queries_error"] += 1
        raise HTTPException(
            status_code=404,
            detail=f"Collection '{COLLECTION}' not found. Has indexing completed?"
        )
    except httpx.HTTPError as e:
        metrics["queries_error"] += 1
        logger.error("Embedding service error: %s", e)
        raise HTTPException(status_code=502, detail=f"Embedding service error: {e}")
    except Exception as e:
        metrics["queries_error"] += 1
        logger.error("Query failed: %s", e)
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/info")
async def info():
    """Get information about this query service instance."""
    # Get collection stats if available
    stats = {}
    try:
        conn = get_db_connection()
        table_name = sql.Identifier(f"data_{COLLECTION}")
        with conn.cursor() as cur:
            cur.execute(sql.SQL("SELECT COUNT(*) FROM {}").format(table_name))
            stats["document_count"] = cur.fetchone()[0]
    except Exception:
        # Stats are best-effort; any failure (missing table, db down) leaves count unknown.
        stats["document_count"] = None

    return {
        "ragsource": {
            "name": RAGSOURCE_NAME,
            "namespace": RAGSOURCE_NAMESPACE,
        },
        "vectorstore": {
            "type": VECTORSTORE_TYPE,
            "host": db_config["host"],
            "port": db_config["port"],
            "database": db_config["dbname"],
            "collection": COLLECTION,
            "dimensions": DIMENSIONS,
        },
        "embedding": {
            "type": EMBEDDING_TYPE,
            "endpoint": EMBEDDING_ENDPOINT,
            "model": EMBEDDING_MODEL,
        },
        "config": {
            "default_top_k": DEFAULT_TOP_K,
        },
        "stats": stats,
    }


@app.get("/metrics")
async def prometheus_metrics():
    """Prometheus-compatible metrics endpoint."""
    lines = [
        "# HELP kubemoot_query_requests_total Total number of query requests",
        "# TYPE kubemoot_query_requests_total counter",
        f'kubemoot_query_requests_total{{ragsource="{RAGSOURCE_NAME}"}} {metrics["queries_total"]}',
        "",
        "# HELP kubemoot_query_success_total Successful query requests",
        "# TYPE kubemoot_query_success_total counter",
        f'kubemoot_query_success_total{{ragsource="{RAGSOURCE_NAME}"}} {metrics["queries_success"]}',
        "",
        "# HELP kubemoot_query_errors_total Failed query requests",
        "# TYPE kubemoot_query_errors_total counter",
        f'kubemoot_query_errors_total{{ragsource="{RAGSOURCE_NAME}"}} {metrics["queries_error"]}',
        "",
        "# HELP kubemoot_embedding_time_ms_total Total embedding generation time",
        "# TYPE kubemoot_embedding_time_ms_total counter",
        f'kubemoot_embedding_time_ms_total{{ragsource="{RAGSOURCE_NAME}"}} {metrics["embedding_time_total_ms"]}',
        "",
        "# HELP kubemoot_search_time_ms_total Total vector search time",
        "# TYPE kubemoot_search_time_ms_total counter",
        f'kubemoot_search_time_ms_total{{ragsource="{RAGSOURCE_NAME}"}} {metrics["search_time_total_ms"]}',
    ]
    return "\n".join(lines)


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host=HOST, port=PORT, log_level="warning")
