#!/usr/bin/env python3
"""A stub MCP server (HTTP+SSE transport) for the native image check.

The check starts the native agent-runtime binary and needs one MCP server it can reach
without a cluster. This serves two tools over the SSE transport the agent-runtime's MCP
client speaks: GET /sse opens the event stream and announces a per-session message URL, and
POST /message?sessionId=... takes JSON-RPC requests whose answers come back on the stream.
GET /ready answers 200 once the server accepts connections, for a caller to wait on.
It implements initialize, ping, tools/list and tools/call, and nothing else.

Usage: stub-mcp-server.py PORT      (listens on 127.0.0.1)
Standard library only.
"""
import json
import queue
import sys
import threading
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

TOOLS = [
    {
        "name": "echo",
        "description": "Returns the text it is given.",
        "inputSchema": {
            "type": "object",
            "properties": {"text": {"type": "string", "description": "Text to return"}},
            "required": ["text"],
        },
    },
    {
        "name": "list_pods",
        "description": "Returns a fixed pod list as YAML.",
        "inputSchema": {
            "type": "object",
            "properties": {"namespace": {"type": "string", "description": "Namespace"}},
        },
    },
]

PODS_YAML = "- name: web-1\n  ready: true\n- name: web-2\n  ready: false\n"

SESSIONS = {}
LOCK = threading.Lock()


def call_tool(params):
    """The content a tools/call answer carries."""
    name = params.get("name")
    args = params.get("arguments") or {}
    if name == "echo":
        return {"content": [{"type": "text", "text": str(args.get("text", ""))}], "isError": False}
    if name == "list_pods":
        return {"content": [{"type": "text", "text": PODS_YAML}], "isError": False}
    return {"content": [{"type": "text", "text": "unknown tool " + str(name)}], "isError": True}


def answer(request):
    """The JSON-RPC answer to one request, or None for a notification."""
    if "id" not in request:
        return None
    method = request.get("method")
    params = request.get("params") or {}
    result = None
    if method == "initialize":
        result = {
            "protocolVersion": params.get("protocolVersion", "2024-11-05"),
            "capabilities": {"tools": {"listChanged": False}},
            "serverInfo": {"name": "stub-mcp-server", "version": "0.0.0"},
        }
    elif method == "ping":
        result = {}
    elif method == "tools/list":
        result = {"tools": TOOLS}
    elif method == "tools/call":
        result = call_tool(params)
    if result is None:
        return {"jsonrpc": "2.0", "id": request["id"],
                "error": {"code": -32601, "message": "method not found: " + str(method)}}
    return {"jsonrpc": "2.0", "id": request["id"], "result": result}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        sys.stderr.write("stub-mcp-server: " + (fmt % args) + "\n")

    def do_GET(self):
        if urlparse(self.path).path == "/ready":
            body = b"ok"
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if urlparse(self.path).path != "/sse":
            self.send_error(404)
            return
        session = uuid.uuid4().hex
        events = queue.Queue()
        with LOCK:
            SESSIONS[session] = events
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        try:
            self.wfile.write(f"event: endpoint\ndata: /message?sessionId={session}\n\n".encode())
            self.wfile.flush()
            while True:
                try:
                    message = events.get(timeout=15)
                except queue.Empty:
                    self.wfile.write(b": keep-alive\n\n")
                    self.wfile.flush()
                    continue
                self.wfile.write(f"event: message\ndata: {json.dumps(message)}\n\n".encode())
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            with LOCK:
                SESSIONS.pop(session, None)

    def do_POST(self):
        url = urlparse(self.path)
        session = (parse_qs(url.query).get("sessionId") or [""])[0]
        with LOCK:
            events = SESSIONS.get(session)
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length)
        if url.path != "/message" or events is None:
            self.send_error(404)
            return
        try:
            request = json.loads(body)
        except ValueError:
            self.send_error(400)
            return
        reply = answer(request) if isinstance(request, dict) else None
        if reply is not None:
            events.put(reply)
        self.send_response(202)
        self.send_header("Content-Length", "0")
        self.end_headers()


def main(argv):
    if len(argv) != 2 or not argv[1].isdigit():
        sys.stderr.write("usage: stub-mcp-server.py PORT\n")
        return 64
    server = ThreadingHTTPServer(("127.0.0.1", int(argv[1])), Handler)
    server.daemon_threads = True
    server.serve_forever()
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
