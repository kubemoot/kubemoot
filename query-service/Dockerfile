# syntax=docker/dockerfile:1
#
# Kubemoot RAG Query Service
# FastAPI service for semantic search against vector stores.
# Auto-deployed by the operator for each RAGSource.
#

FROM python:3.13-slim AS builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    libpq-dev \
    gcc \
    && rm -rf /var/lib/apt/lists/*

# Install uv for faster dependency installation
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

COPY requirements.txt .

RUN --mount=type=cache,target=/root/.cache/uv \
    uv pip install --system --target /app/deps -r requirements.txt

# Runtime stage
FROM python:3.13-slim

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
