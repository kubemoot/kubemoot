# syntax=docker/dockerfile:1
#
# Kubemoot RAG Query Service
# FastAPI service for semantic search against vector stores.
# Auto-deployed by the operator for each RAGSource.
#

# uv as its own stage so Dependabot keeps its tag and digest current (it reads FROM lines only)
FROM ghcr.io/astral-sh/uv:0.12.23@sha256:61d393e44e249f2e4b526b6c7ddcecce245946826e608e11c93ad4f5bba55b21 AS uv

FROM docker.io/library/python:3.14-slim@sha256:f85c5697265c178cc6887276c55fe16cf3d14ca35c3df6a5eab3b360534a55d2 AS builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    libpq-dev \
    gcc \
    && rm -rf /var/lib/apt/lists/*

# Install uv for faster dependency installation
COPY --from=uv /uv /usr/local/bin/uv

COPY requirements.txt .

RUN --mount=type=cache,target=/root/.cache/uv \
    uv pip install --system --require-hashes --target /app/deps -r requirements.txt

# Runtime stage
FROM docker.io/library/python:3.14-slim@sha256:f85c5697265c178cc6887276c55fe16cf3d14ca35c3df6a5eab3b360534a55d2

WORKDIR /app

# Install runtime dependencies only
RUN apt-get update && apt-get install -y --no-install-recommends \
    libpq5 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -m -u 1000 kubemoot \
    && chown -R kubemoot:kubemoot /app

# Copy Python dependencies from builder
COPY --from=builder /app/deps /app/deps

# Copy application and version
COPY --chown=kubemoot:kubemoot query-service.py /app/
COPY --chown=kubemoot:kubemoot VERSION /app/

USER kubemoot

ENV PYTHONPATH=/app/deps:/app \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

# Default port
EXPOSE 8000

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD python -c "import httpx; httpx.get('http://localhost:8000/health').raise_for_status()"

CMD ["python", "query-service.py"]
