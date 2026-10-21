"""Unit tests for the query service's error handling, with the database and the
embedding service replaced by mocks."""

import asyncio
import importlib.util
import unittest
from pathlib import Path
from typing import ClassVar
from unittest import mock

import httpx
import psycopg2
import psycopg2.errors
from fastapi import HTTPException
from prometheus_client import REGISTRY

_SPEC = importlib.util.spec_from_file_location(
    "query_service", Path(__file__).parent / "query-service.py"
)
qs = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(qs)


def run(coro):
    return asyncio.run(coro)


class FailingEmbeddingClient:
    """An httpx.AsyncClient stand-in whose requests fail to connect."""

    def __init__(self, *args, **kwargs):
        """Accept and ignore the arguments the real client takes."""

    async def __aenter__(self):
        return self

    async def __aexit__(self, *exc):
        return False

    async def get(self, url):
        raise httpx.ConnectError("connection refused")


class ReadyTest(unittest.TestCase):
    def test_reports_both_failures_as_not_ready(self):
        db_down = psycopg2.OperationalError("db down")
        with (
            mock.patch.object(qs, "get_db_connection", side_effect=db_down),
            mock.patch.object(qs.httpx, "AsyncClient", FailingEmbeddingClient),
            self.assertRaises(HTTPException) as ctx,
        ):
            run(qs.ready())
        self.assertEqual(ctx.exception.status_code, 503)
        self.assertIn("database: db down", ctx.exception.detail)
        self.assertIn("embedding: connection refused", ctx.exception.detail)

    def test_unexpected_error_is_not_reported_as_a_check(self):
        with (
            mock.patch.object(qs, "get_db_connection", side_effect=ValueError("bug")),
            self.assertRaises(ValueError),
        ):
            run(qs.ready())


class LifespanTest(unittest.TestCase):
    def test_starts_with_a_warning_when_the_database_is_down(self):
        async def start_and_stop():
            async with qs.lifespan(qs.app):
                await asyncio.sleep(0)  # the app would serve requests here

        db_down = psycopg2.OperationalError("db down")
        with (
            mock.patch.object(qs, "get_db_connection", side_effect=db_down),
            self.assertLogs("kubemoot-query", level="WARNING") as logs,
        ):
            run(start_and_stop())
        self.assertTrue(any("Database connection: FAILED (db down)" in line for line in logs.output))


class InfoTest(unittest.TestCase):
    def test_document_count_is_unknown_when_the_database_fails(self):
        db_down = psycopg2.OperationalError("db down")
        with mock.patch.object(qs, "get_db_connection", side_effect=db_down):
            body = run(qs.info())
        self.assertIsNone(body["stats"]["document_count"])


def sample(name):
    """The value Prometheus would scrape for this RAGSource's metric."""
    return REGISTRY.get_sample_value(name, {"ragsource": qs.RAGSOURCE_NAME}) or 0.0


def errors():
    """The failed-query count Prometheus would scrape."""
    return sample("kubemoot_query_errors_total")


class MetricsTest(unittest.TestCase):
    def test_metrics_are_prometheus_text_with_help(self):
        response = run(qs.prometheus_metrics())
        self.assertTrue(response.media_type.startswith("text/plain"), response.media_type)
        body = response.body.decode()
        for name in ("kubemoot_query_requests_total", "kubemoot_query_success_total", "kubemoot_query_errors_total",
                     "kubemoot_embedding_time_ms_total", "kubemoot_search_time_ms_total"):
            self.assertIn(f'{name}{{ragsource="{qs.RAGSOURCE_NAME}"}}', body)
        for name in ("kubemoot_query_requests", "kubemoot_query_errors", "kubemoot_search_time_ms"):
            self.assertRegex(body, rf"# HELP {name}_total \S")

    def test_an_answered_query_counts_its_success(self):
        cursor = mock.MagicMock()
        cursor.__enter__.return_value.fetchall.return_value = [{"content": "c", "metadata": {}, "score": 0.9}]
        conn = mock.MagicMock()
        conn.cursor.return_value = cursor
        before = sample("kubemoot_query_success_total")
        with (
            mock.patch.object(qs, "get_embedding", mock.AsyncMock(return_value=[0.1])),
            mock.patch.object(qs, "get_db_connection", return_value=conn),
        ):
            body = run(qs.query(qs.QueryRequest(query="q")))
        self.assertEqual(len(body.results), 1)
        self.assertEqual(sample("kubemoot_query_success_total"), before + 1)

    def test_a_query_is_counted(self):
        before = sample("kubemoot_query_requests_total")
        with (
            mock.patch.object(qs, "get_embedding", mock.AsyncMock(return_value=[0.1])),
            mock.patch.object(qs, "get_db_connection", side_effect=RuntimeError("boom")),
            self.assertLogs("kubemoot-query", level="ERROR"),
            self.assertRaises(HTTPException),
        ):
            run(qs.query(qs.QueryRequest(query="q")))
        self.assertEqual(sample("kubemoot_query_requests_total"), before + 1)


class QueryTest(unittest.TestCase):
    def setUp(self):
        embed = mock.patch.object(qs, "get_embedding", mock.AsyncMock(return_value=[0.1]))
        embed.start()
        self.addCleanup(embed.stop)
        self.errors_before = errors()

    def query(self):
        return run(qs.query(qs.QueryRequest(query="what runs here")))

    def test_missing_collection_is_404(self):
        missing = psycopg2.errors.UndefinedTable()
        with (
            mock.patch.object(qs, "get_db_connection", side_effect=missing),
            self.assertRaises(HTTPException) as ctx,
        ):
            self.query()
        self.assertEqual(ctx.exception.status_code, 404)
        self.assertIsInstance(ctx.exception.__cause__, psycopg2.errors.UndefinedTable)
        self.assertEqual(errors(), self.errors_before + 1)

    def test_embedding_failure_is_502(self):
        failing = mock.AsyncMock(side_effect=httpx.ConnectError("refused"))
        with (
            mock.patch.object(qs, "get_embedding", failing),
            self.assertRaises(HTTPException) as ctx,
        ):
            self.query()
        self.assertEqual(ctx.exception.status_code, 502)
        self.assertIn("refused", ctx.exception.detail)

    def test_any_other_failure_is_500_and_logged_with_its_traceback(self):
        with (
            mock.patch.object(qs, "get_db_connection", side_effect=RuntimeError("boom")),
            self.assertLogs("kubemoot-query", level="ERROR") as logs,
            self.assertRaises(HTTPException) as ctx,
        ):
            self.query()
        self.assertEqual(ctx.exception.status_code, 500)
        self.assertEqual(ctx.exception.detail, "boom")
        self.assertIn("Traceback", logs.output[0])
        self.assertEqual(errors(), self.errors_before + 1)


if __name__ == "__main__":
    unittest.main()


class RecordingEmbeddingClient:
    """An httpx.AsyncClient stand-in that records the embedding request and answers with a
    fixed, unnormalized vector the way vLLM does."""

    calls: ClassVar[list] = []

    def __init__(self, *args, **kwargs):
        """Accept and ignore the arguments the real client takes."""

    async def __aenter__(self):
        return self

    async def __aexit__(self, *exc):
        return False

    async def post(self, url, headers=None, json=None):
        type(self).calls.append((url, json))
        request = httpx.Request("POST", url)
        return httpx.Response(200, json={"data": [{"embedding": [3.0, 0.0, 4.0]}]}, request=request)

    async def get(self, url):
        type(self).calls.append((url, None))
        return httpx.Response(200, json={"data": []}, request=httpx.Request("GET", url))


class EmbeddingTest(unittest.TestCase):
    def setUp(self):
        RecordingEmbeddingClient.calls = []

    def test_l2_normalize_scales_to_unit_length(self):
        self.assertEqual(qs.l2_normalize([3.0, 0.0, 4.0]), [0.6, 0.0, 0.8])

    def test_l2_normalize_leaves_a_zero_vector_alone(self):
        self.assertEqual(qs.l2_normalize([0.0, 0.0]), [0.0, 0.0])
        self.assertEqual(qs.l2_normalize([]), [])

    def test_embeds_through_the_openai_compatible_api_and_normalizes(self):
        with mock.patch.object(qs.httpx, "AsyncClient", RecordingEmbeddingClient):
            vector = run(qs.get_embedding("hello"))
        url, body = RecordingEmbeddingClient.calls[0]
        self.assertTrue(url.endswith("/v1/embeddings"), url)
        self.assertEqual(body["input"], "hello")
        self.assertEqual(vector, [0.6, 0.0, 0.8])

    def test_ready_checks_the_embedding_server_on_the_openai_models_route(self):
        with (
            mock.patch.object(qs, "get_db_connection"),
            mock.patch.object(qs.httpx, "AsyncClient", RecordingEmbeddingClient),
        ):
            run(qs.ready())
        self.assertTrue(RecordingEmbeddingClient.calls[0][0].endswith("/v1/models"))

    def test_an_embedding_server_error_is_not_swallowed(self):
        class Refusing(RecordingEmbeddingClient):
            async def post(self, url, headers=None, json=None):
                request = httpx.Request("POST", url)
                return httpx.Response(400, json={"error": "no"}, request=request)

        with (
            mock.patch.object(qs.httpx, "AsyncClient", Refusing),
            self.assertRaises(httpx.HTTPStatusError),
        ):
            run(qs.get_embedding("hello"))
