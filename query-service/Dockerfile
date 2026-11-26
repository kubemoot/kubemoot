# syntax=docker/dockerfile:1
#
# Kubemoot RAG Query Service
# FastAPI service for semantic search against vector stores.
# Auto-deployed by the operator for each RAGSource.
#

# uv as its own stage so Dependabot keeps its tag and digest current (it reads FROM lines only)
FROM ghcr.io/astral-sh/uv:0.13.10@sha256:a3ea1357342c1a4bf9a0c53a3deb1b55379aefa037338d8113d5771efa01774f AS uv

FROM docker.io/library/python:3.15-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928 AS builder

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
FROM docker.io/library/python:3.15-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928

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
