"""Unit tests for the query service's error handling, with the database and the
embedding service replaced by mocks."""

import asyncio
import importlib.util
import unittest
from pathlib import Path
from unittest import mock

import httpx
import psycopg2
import psycopg2.errors
from fastapi import HTTPException

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


class QueryTest(unittest.TestCase):
    def setUp(self):
        embed = mock.patch.object(qs, "get_embedding", mock.AsyncMock(return_value=[0.1]))
        embed.start()
        self.addCleanup(embed.stop)
        self.errors_before = qs.metrics["queries_error"]

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
        self.assertEqual(qs.metrics["queries_error"], self.errors_before + 1)

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
        self.assertEqual(qs.metrics["queries_error"], self.errors_before + 1)


if __name__ == "__main__":
    unittest.main()
