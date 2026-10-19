"""Tests for kubemoot-vllm/files/launcher.py, run by test-kubemoot-vllm.sh.

A fake `vllm` stands in for the real server: it answers /health, /is_sleeping,
/sleep, /wake_up and /collective_rpc on its port and appends what happens to an
events file, so a test reads the order the launcher did things in.

    python3 -I launcher_test.py
"""

import importlib.util
import json
import os
import signal
import stat
import sys
import tempfile
import threading
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent

FAKE_VLLM = '''#!/usr/bin/env python3
import json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse

args = sys.argv[1:]
name = next(a.split("=", 1)[1] for a in args if a.startswith("--served-model-name="))
port = int(next(a.split("=", 1)[1] for a in args if a.startswith("--port=")))
events = os.environ["EVENTS"]
state = {"asleep": False}


def record(what):
    with open(events, "a") as f:
        f.write("%s %s\\n" % (what, name))


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def reply(self, code=200, body=b""):
        self.send_response(code)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = urlparse(self.path).path
        if path == "/health":
            self.reply()
        elif path == "/is_sleeping":
            self.reply(200, json.dumps({"is_sleeping": state["asleep"]}).encode())
        else:
            self.reply(404)

    def do_POST(self):
        parsed = urlparse(self.path)
        length = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(length)
        if parsed.path == "/sleep":
            state["asleep"] = True
            record("sleep" + parsed.query.replace("level=", ""))
        elif parsed.path == "/wake_up":
            state["asleep"] = parsed.query == "tags=weights"
            record("wake" + ("-" + parsed.query if parsed.query else ""))
        elif parsed.path == "/collective_rpc":
            record("reload")
        else:
            return self.reply(404)
        self.reply()


record("start")
HTTPServer(("127.0.0.1", port), Handler).serve_forever()
'''

# A fake that exits at once, as a server that cannot load its model does.
FAKE_CRASH = "#!/bin/sh\nexit 3\n"


def load_launcher():
    spec = importlib.util.spec_from_file_location("launcher", HERE / "kubemoot-vllm" / "files" / "launcher.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def free_ports(count):
    import socket
    socks = [socket.socket() for _ in range(count)]
    for s in socks:
        s.bind(("127.0.0.1", 0))
    ports = [s.getsockname()[1] for s in socks]
    for s in socks:
        s.close()
    return ports


class LauncherTest(unittest.TestCase):
    def setUp(self):
        self.launcher = load_launcher()
        self.launcher.HEALTH_POLL_S = 0.05
        self.launcher.READY_FILE = os.path.join(tempfile.mkdtemp(), "ready")
        self.tmp = tempfile.mkdtemp()
        self.events = os.path.join(self.tmp, "events")
        Path(self.events).touch()
        self.bin = os.path.join(self.tmp, "bin")
        os.mkdir(self.bin)
        self.install_vllm(FAKE_VLLM)
        os.environ["PATH"] = self.bin + os.pathsep + os.environ["PATH"]
        os.environ["EVENTS"] = self.events
        self.instances = []

    def tearDown(self):
        for instance in self.instances:
            instance.stop_all()

    def install_vllm(self, body):
        path = os.path.join(self.bin, "vllm")
        Path(path).write_text(body)
        os.chmod(path, os.stat(path).st_mode | stat.S_IEXEC)

    def make(self, names, level=1):
        ports = free_ports(len(names))
        servers = [{"name": n, "port": p, "args": ["serve", "repo/" + n, "--served-model-name=" + n, "--port=%d" % p]}
                   for n, p in zip(names, ports)]
        instance = self.launcher.Launcher({"sleepLevel": level, "servers": servers})
        self.instances.append(instance)
        return instance

    def seen(self):
        return Path(self.events).read_text().split("\n")[:-1]

    def mark(self):
        return len(self.seen())

    def test_starts_one_server_at_a_time_puts_each_to_sleep_and_wakes_the_first(self):
        instance = self.make(["a", "b", "c"])
        instance.start_all()
        self.assertEqual(self.seen(), [
            "start a", "sleep1 a", "start b", "sleep1 b", "start c", "sleep1 c", "wake a"])
        self.assertTrue(os.path.exists(self.launcher.READY_FILE))
        self.assertFalse(instance.servers[0].is_sleeping())
        self.assertTrue(instance.servers[1].is_sleeping())

    def test_a_dead_asleep_server_restarts_with_the_awake_one_asleep_first_and_woken_after(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        mark = self.mark()
        os.killpg(instance.servers[1].proc.pid, signal.SIGKILL)
        instance.servers[1].proc.wait()

        self.assertTrue(instance.check_once())
        self.assertEqual(self.seen()[mark:], ["sleep1 a", "start b", "sleep1 b", "wake a"])
        self.assertFalse(instance.servers[0].is_sleeping())

    def test_a_dead_awake_server_restarts_asleep_and_the_first_server_is_woken(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        mark = self.mark()
        os.killpg(instance.servers[0].proc.pid, signal.SIGKILL)
        instance.servers[0].proc.wait()

        self.assertTrue(instance.check_once())
        self.assertEqual(self.seen()[mark:], ["start a", "sleep1 a", "wake a"])

    def test_nothing_restarts_while_every_server_runs(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        mark = self.mark()
        self.assertFalse(instance.check_once())
        self.assertEqual(self.seen()[mark:], [])

    def test_a_restart_that_fails_is_retried_until_the_server_comes_up(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        os.killpg(instance.servers[1].proc.pid, signal.SIGKILL)
        instance.servers[1].proc.wait()
        self.launcher.BACKOFF_START_S = 0.05
        real = instance.recover
        attempts = []

        def flaky(dead):
            attempts.append(dead.name)
            if len(attempts) == 1:
                raise self.launcher.ServerExited("b exited with 3 before it answered /health")
            real(dead)

        instance.recover = flaky
        self.assertTrue(instance.check_once())
        self.assertEqual(attempts, ["b", "b"])
        self.assertTrue(instance.servers[1].running())

    def kill(self, server):
        os.killpg(server.proc.pid, signal.SIGKILL)
        server.proc.wait()

    def test_two_dead_servers_are_restarted_one_after_the_other_without_waking_a_dead_one(self):
        instance = self.make(["a", "b", "c"])
        instance.start_all()
        self.kill(instance.servers[0])
        self.kill(instance.servers[1])

        self.assertTrue(instance.check_once())  # restarts a; b is still dead and is not woken
        self.assertTrue(instance.servers[0].running())
        self.assertTrue(instance.check_once())  # restarts b
        self.assertFalse(instance.check_once())
        self.assertTrue(all(s.running() for s in instance.servers))
        self.assertEqual(sum(1 for s in instance.servers if not s.is_sleeping()), 1)

    def test_a_server_that_does_not_answer_fails_the_attempt_not_the_launcher(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        self.launcher.BACKOFF_START_S = 0.05
        self.kill(instance.servers[1])
        real = instance.servers[0].is_sleeping
        calls = []

        def wedged():
            calls.append(1)
            if len(calls) == 1:
                raise OSError("connection reset")
            return real()

        instance.servers[0].is_sleeping = wedged
        self.assertTrue(instance.check_once())
        self.assertGreaterEqual(len(calls), 2)
        self.assertTrue(instance.servers[1].running())

    def test_a_server_that_exits_before_it_answers_fails_the_first_start(self):
        self.install_vllm(FAKE_CRASH)
        instance = self.make(["a"])
        with self.assertRaises(self.launcher.ServerExited):
            instance.start_all()
        self.assertFalse(os.path.exists(self.launcher.READY_FILE))

    def test_level_two_sleeps_at_level_two_and_wakes_in_three_steps(self):
        instance = self.make(["a", "b"], level=2)
        instance.start_all()
        instance.servers[0].sleep()
        mark = self.mark()
        instance.servers[0].wake()
        self.assertEqual(self.seen()[:2], ["start a", "sleep2 a"])
        self.assertEqual(self.seen()[mark:], ["wake-tags=weights a", "reload a", "wake-tags=kv_cache a"])

    def test_stop_ends_every_process_of_every_group(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        pids = [s.proc.pid for s in instance.servers]
        instance.stop_all()
        for pid in pids:
            with self.assertRaises(ProcessLookupError):
                os.killpg(pid, 0)

    def test_a_stop_request_ends_the_wait_for_a_server(self):
        self.install_vllm("#!/bin/sh\nsleep 30\n")  # never answers /health
        instance = self.make(["a"])
        threading.Timer(0.3, instance.stopping.set).start()
        with self.assertRaises(self.launcher.Stopping):
            instance.start_all()

    def test_each_servers_log_lines_are_prefixed_with_its_name(self):
        self.install_vllm("#!/bin/sh\necho hello from vllm\nexec sleep 30\n")
        instance = self.make(["a"])
        server = instance.servers[0]
        import io
        out, real = io.StringIO(), sys.stdout
        sys.stdout = out
        try:
            server.start()
            for _ in range(100):
                if "hello" in out.getvalue():
                    break
                threading.Event().wait(0.05)
        finally:
            sys.stdout = real
        self.assertIn("[a] hello from vllm", out.getvalue())

    def test_the_rendered_servers_file_shape_loads(self):
        path = os.path.join(self.tmp, "servers.json")
        Path(path).write_text(json.dumps({"sleepLevel": 2, "servers": [{"name": "a", "port": 1, "args": ["serve"]}]}))
        with open(path, encoding="utf-8") as f:
            instance = self.launcher.Launcher(json.load(f))
        self.assertEqual((instance.servers[0].level, instance.servers[0].port), (2, 1))


if __name__ == "__main__":
    unittest.main()
