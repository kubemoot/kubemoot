"""Tests for kubemoot-vllm/files/launcher.py, run by test-kubemoot-vllm.sh.

A fake `vllm` stands in for the real server: it answers /health, /is_sleeping,
/sleep, /wake_up and /collective_rpc on its port (the launcher never wakes a server; the tests wake one to stand in for the operator's adapter) and appends what happens to an
events file, so a test reads the order the launcher did things in. It is
single-threaded, like a server busy with a wake: while it handles a slow request,
/health and /is_sleeping wait. WAKE_DELAY makes each wake step take that many seconds
and WAKE_LAG keeps /is_sleeping reporting asleep that long after a wake was answered.

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
import time
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent

FAKE_VLLM = '''#!/usr/bin/env python3
import json, os, sys, time
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse

args = sys.argv[1:]
name = next(a.split("=", 1)[1] for a in args if a.startswith("--served-model-name="))
port = int(next(a.split("=", 1)[1] for a in args if a.startswith("--port=")))
events = os.environ["EVENTS"]
delay = float(os.environ.get("WAKE_DELAY", "0"))
lag = float(os.environ.get("WAKE_LAG", "0"))
never_sleep = bool(os.environ.get("NEVER_SLEEP"))
state = {"asleep": False, "lag_until": 0.0}


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
            asleep = state["asleep"] or time.time() < state["lag_until"]
            self.reply(200, json.dumps({"is_sleeping": asleep}).encode())
        else:
            self.reply(404)

    def do_POST(self):
        parsed = urlparse(self.path)
        length = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(length)
        if parsed.path == "/sleep":
            if never_sleep:
                record("sleep-ignored")
                return self.reply()  # accepts the request and never changes state
            state["asleep"] = True
            record("sleep" + parsed.query.replace("level=", ""))
        elif parsed.path == "/wake_up":
            time.sleep(delay)
            if os.environ.get("NEVER_WAKE"):
                record("wake-ignored")
                return self.reply()  # accepts the request and never changes state
            state["asleep"] = parsed.query == "tags=weights"
            if not state["asleep"]:
                state["lag_until"] = time.time() + lag
            record("wake" + ("-" + parsed.query if parsed.query else ""))
        elif parsed.path == "/collective_rpc":
            time.sleep(delay)
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
        L = self.launcher
        L.HEALTH_POLL_S = 0.05
        L.STATE_POLL_S = 0.05
        L.READY_FILE = os.path.join(tempfile.mkdtemp(), "ready")
        L.ALIVE_FILE = os.path.join(tempfile.mkdtemp(), "alive")
        L.HEALTH_TIMEOUT_S = 0.5
        L.BACKOFF_START_S = 0.05
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
        for key in ("WAKE_DELAY", "WAKE_LAG", "NEVER_WAKE", "NEVER_SLEEP"):
            os.environ.pop(key, None)

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

    def wake(self, server):
        """Wakes a server the way the operator's adapter does, standing in for it: the
        launcher itself never wakes one."""
        if server.level == 2:
            steps = [("/wake_up?tags=weights", None), ("/collective_rpc", {"method": "reload_weights"}),
                     ("/wake_up?tags=kv_cache", None)]
        else:
            steps = [("/wake_up", None)]
        server.change(steps, asleep=False)

    def seen(self):
        return Path(self.events).read_text().split("\n")[:-1]

    def mark(self):
        return len(self.seen())

    def kill(self, server):
        os.killpg(server.proc.pid, signal.SIGKILL)
        server.proc.wait()

    def stall(self, server):
        """A server that stays a running process and stops answering, as a hung one does."""
        os.killpg(server.proc.pid, signal.SIGSTOP)

    # ---- start order and restart on exit -------------------------------------

    def test_starts_one_server_at_a_time_puts_each_to_sleep_and_wakes_none(self):
        instance = self.make(["a", "b", "c"])
        instance.start_all()
        self.assertEqual(self.seen(), [
            "start a", "sleep1 a", "start b", "sleep1 b", "start c", "sleep1 c"])
        self.assertTrue(os.path.exists(self.launcher.READY_FILE))
        self.assertTrue(all(s.is_sleeping() for s in instance.servers), "the adapter wakes, not the launcher")

    def test_a_dead_server_restarts_with_the_awake_one_asleep_first_and_nothing_woken(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        self.wake(instance.servers[0])  # the adapter woke a for a call
        mark = self.mark()
        self.kill(instance.servers[1])

        self.assertTrue(instance.check_once())
        self.assertEqual(self.seen()[mark:], ["sleep1 a", "start b", "sleep1 b"])
        self.assertTrue(all(s.is_sleeping() for s in instance.servers))

    def test_a_dead_server_restarts_asleep_and_no_server_is_woken(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        mark = self.mark()
        self.kill(instance.servers[0])

        self.assertTrue(instance.check_once())
        self.assertEqual(self.seen()[mark:], ["start a", "sleep1 a"])

    def test_nothing_restarts_while_every_server_runs(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        mark = self.mark()
        self.assertFalse(instance.check_once())
        self.assertEqual(self.seen()[mark:], [])

    def test_two_dead_servers_are_restarted_one_after_the_other_in_order(self):
        instance = self.make(["a", "b", "c"])
        instance.start_all()
        self.kill(instance.servers[0])
        self.kill(instance.servers[1])

        self.assertTrue(instance.check_once())  # restarts a; b is still dead and is not woken
        self.assertTrue(instance.servers[0].running())
        self.assertTrue(instance.check_once())  # restarts b
        self.assertFalse(instance.check_once())
        self.assertTrue(all(s.running() for s in instance.servers))
        self.assertTrue(all(s.is_sleeping() for s in instance.servers))

    def test_a_restart_that_fails_is_retried_until_the_server_comes_up(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        self.kill(instance.servers[1])
        real = instance.start_server_asleep
        attempts = []

        def flaky(server):
            attempts.append(server.name)
            if len(attempts) == 1:
                raise self.launcher.ServerExited("b exited")
            real(server)

        instance.start_server_asleep = flaky
        self.assertTrue(instance.check_once())
        self.assertEqual(attempts, ["b", "b"])
        self.assertTrue(all(s.is_sleeping() for s in instance.servers))

    def test_a_failed_restart_leaves_every_server_asleep(self):
        instance = self.make(["a", "b", "c"])
        instance.start_all()
        self.wake(instance.servers[2])  # the adapter woke c for a call
        self.kill(instance.servers[1])
        real = instance.start_server_asleep
        attempts = []

        def flaky(server):
            attempts.append(1)
            if len(attempts) == 1:
                raise self.launcher.ServerExited("exited")
            real(server)

        instance.start_server_asleep = flaky
        self.assertTrue(instance.check_once())
        self.assertEqual([s.is_sleeping() for s in instance.servers], [True, True, True])

    def test_a_server_that_exits_before_it_answers_fails_the_first_start(self):
        self.install_vllm(FAKE_CRASH)
        instance = self.make(["a"])
        with self.assertRaises(self.launcher.ServerExited):
            instance.start_all()
        self.assertFalse(os.path.exists(self.launcher.READY_FILE))

    def test_the_ready_file_is_removed_while_a_server_is_recovered_and_back_after(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        self.kill(instance.servers[1])
        during = []
        real = instance.start_server_asleep

        def watching(server):
            during.append(os.path.exists(self.launcher.READY_FILE))
            real(server)

        instance.start_server_asleep = watching
        self.assertTrue(instance.check_once())
        self.assertEqual(during, [False])
        self.assertTrue(os.path.exists(self.launcher.READY_FILE))

    # ---- a running server is never killed -----------------------------------------

    def test_a_running_server_that_stops_answering_is_never_killed(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        server = instance.servers[1]
        pid = server.proc.pid
        self.stall(server)
        for _ in range(10):  # far more checks than any count that once marked a server hung
            self.assertFalse(instance.check_once())
            instance.report_ready()
        self.assertEqual(server.proc.pid, pid)
        self.assertTrue(server.running())
        self.assertFalse(os.path.exists(self.launcher.READY_FILE), "a server that does not answer is not ready")

        os.killpg(server.proc.pid, signal.SIGCONT)  # it answers again
        instance.report_ready()
        self.assertTrue(os.path.exists(self.launcher.READY_FILE))
        self.assertEqual(server.proc.pid, pid)

    def test_a_server_that_does_not_answer_is_skipped_when_the_others_are_put_to_sleep(self):
        instance = self.make(["a", "b", "c"])
        instance.start_all()
        a_pid = instance.servers[0].proc.pid
        self.stall(instance.servers[0])  # the awake one stops answering
        self.assertFalse(instance.is_awake(instance.servers[0]))
        self.kill(instance.servers[2])

        self.assertTrue(instance.check_once())  # c exited: restarted, a is skipped, not killed
        self.assertEqual(instance.servers[0].proc.pid, a_pid)
        self.assertTrue(instance.servers[2].running())
        self.assertNotIn("sleep1 a", self.seen()[-4:])

    def test_a_server_that_does_not_report_its_sleep_state_is_skipped_not_restarted(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        pid = instance.servers[0].proc.pid
        self.kill(instance.servers[1])

        real = instance.servers[0].is_sleeping
        calls = []

        def failing_once():
            calls.append(1)
            if len(calls) == 1:
                raise OSError("connection reset")
            return real()

        instance.servers[0].is_sleeping = failing_once
        self.assertTrue(instance.check_once())
        self.assertEqual(instance.servers[0].proc.pid, pid)
        self.assertTrue(instance.servers[1].running())

    def test_a_server_that_does_not_report_at_first_is_put_to_sleep_once_it_does(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        self.wake(instance.servers[0])
        self.kill(instance.servers[1])
        real = instance.servers[0].is_sleeping
        calls = []

        def silent_twice():
            calls.append(1)
            if len(calls) <= 2:
                raise OSError("no answer")
            return real()

        instance.servers[0].is_sleeping = silent_twice
        real_start = instance.start_server_asleep
        attempts = []

        def flaky(server):
            attempts.append(1)
            if len(attempts) == 1:
                raise self.launcher.ServerExited("exited")
            real_start(server)

        instance.start_server_asleep = flaky
        mark = self.mark()
        self.assertTrue(instance.check_once())
        # The second attempt saw a awake, put it to sleep, then started b.
        self.assertEqual(self.seen()[mark:], ["sleep1 a", "start b", "sleep1 b"])

    def test_the_readiness_of_stalled_servers_is_read_side_by_side(self):
        instance = self.make(["a", "b", "c"])
        instance.start_all()
        self.stall(instance.servers[0])
        self.stall(instance.servers[1])
        start = time.monotonic()
        instance.report_ready()
        self.assertLess(time.monotonic() - start, 0.9)  # one 0.5 s read, not two or three
        self.assertFalse(os.path.exists(self.launcher.READY_FILE))
        for s in instance.servers[:2]:
            os.killpg(s.proc.pid, signal.SIGCONT)

    def test_an_error_answer_to_is_sleeping_is_logged_once_not_waited_on_silently(self):
        import io
        instance = self.make(["a"])
        server = instance.servers[0]

        def refused():
            raise RuntimeError("a: /is_sleeping returned 404")

        server.is_sleeping = refused
        out, real = io.StringIO(), sys.stdout
        sys.stdout = out
        try:
            for _ in range(3):
                self.assertFalse(server.reached(True))
        finally:
            sys.stdout = real
        self.assertEqual(out.getvalue().count("returned 404"), 1)

    def test_a_reply_that_does_not_say_whether_the_server_sleeps_is_an_error(self):
        instance = self.make(["a"])
        server = instance.servers[0]
        self.launcher.http_request = lambda *a, **k: (200, b"{}")
        with self.assertRaises(ValueError):
            server.is_sleeping()

    # ---- sleeping and waking wait on state ------------------------------------------

    def test_level_two_sleeps_at_level_two_and_never_wakes(self):
        instance = self.make(["a", "b"], level=2)
        instance.start_all()
        self.assertEqual(self.seen(), ["start a", "sleep2 a", "start b", "sleep2 b"])

    def test_a_change_slower_than_a_request_timeout_succeeds(self):
        os.environ["WAKE_DELAY"] = "2"  # each step takes four times a health read's timeout
        instance = self.make(["a", "b"], level=2)
        instance.start_all()
        start = time.monotonic()
        self.wake(instance.servers[0])  # three slow steps
        self.assertGreater(time.monotonic() - start, 5)
        self.assertFalse(instance.servers[0].is_sleeping())

    def test_a_server_that_stalls_health_during_a_wake_is_not_killed(self):
        # The fake is single-threaded: for the 3 s of the wake /health and /is_sleeping
        # wait, so every read in that window times out. Nothing kills the server.
        os.environ["WAKE_DELAY"] = "3"
        instance = self.make(["a", "b"])
        instance.start_all()
        pids = [s.proc.pid for s in instance.servers]
        instance.servers[0].sleep()
        self.wake(instance.servers[1])
        self.assertEqual([s.proc.pid for s in instance.servers], pids)
        self.assertFalse(instance.servers[1].is_sleeping())

    def test_a_wake_waits_for_the_state_not_for_the_answer(self):
        os.environ["WAKE_LAG"] = "1.5"  # the wake is answered, but the server reports asleep a while longer
        instance = self.make(["a", "b"])
        instance.start_all()
        start = time.monotonic()
        self.wake(instance.servers[0])
        self.assertGreaterEqual(time.monotonic() - start, 1.0)
        self.assertFalse(instance.servers[0].is_sleeping())

    def test_a_wait_that_makes_no_progress_is_given_up_and_the_heartbeat_stops(self):
        os.environ["NEVER_WAKE"] = "1"  # the POST is answered 200 and nothing ever changes
        self.launcher.NO_PROGRESS_LIMIT_S = 1.5
        instance = self.make(["a"])
        instance.start_server_asleep(instance.servers[0])
        start = time.monotonic()
        with self.assertRaises(self.launcher.NoProgress):
            self.wake(instance.servers[0])
        elapsed = time.monotonic() - start
        self.assertGreaterEqual(elapsed, 1.5)
        self.assertLess(elapsed, 6)
        # No progress is recorded after the launcher gave up.
        beat_at = os.stat(self.launcher.ALIVE_FILE).st_mtime
        time.sleep(0.6)
        self.assertEqual(os.stat(self.launcher.ALIVE_FILE).st_mtime, beat_at)

    def test_no_progress_is_not_swallowed_by_the_recovery_retries(self):
        self.launcher.NO_PROGRESS_LIMIT_S = 1.0
        instance = self.make(["a", "b"])
        for s in instance.servers:
            instance.start_server_asleep(s)
        self.kill(instance.servers[1])
        os.environ["NEVER_SLEEP"] = "1"  # the restarted server accepts /sleep and never sleeps
        with self.assertRaises(self.launcher.NoProgress):
            instance.recover_with_backoff(instance.servers[1])

    def test_a_wait_that_keeps_progressing_past_the_limit_is_not_cut_off(self):
        # Three level 2 steps of 1 s each: 3 s in all, over the 2.5 s limit, but a step
        # is answered every second, so the wait is always changing.
        os.environ["WAKE_DELAY"] = "1"
        self.launcher.NO_PROGRESS_LIMIT_S = 2.5
        instance = self.make(["a", "b"], level=2)
        instance.start_all()
        start = time.monotonic()
        self.wake(instance.servers[0])
        self.assertGreater(time.monotonic() - start, 3)
        self.assertFalse(instance.servers[0].is_sleeping())

    def test_a_wake_ends_with_the_servers_process_exiting(self):
        os.environ["WAKE_DELAY"] = "30"
        instance = self.make(["a", "b"])
        # a is started; b is asked to wake with a slow step, and exits meanwhile
        instance.start_server_asleep(instance.servers[0])
        server = instance.servers[0]
        threading.Timer(0.5, lambda: self.kill(server)).start()
        # Either the exit is seen or the request in flight fails with the connection; both
        # fail the recovery attempt, which restarts the server.
        with self.assertRaises(self.launcher.RECOVERABLE):
            self.wake(server)
        self.assertFalse(server.running())

    def test_a_wake_ends_on_a_stop_request(self):
        os.environ["WAKE_DELAY"] = "30"
        instance = self.make(["a"])
        instance.start_server_asleep(instance.servers[0])
        threading.Timer(0.5, instance.stopping.set).start()
        start = time.monotonic()
        with self.assertRaises(self.launcher.Stopping):
            self.wake(instance.servers[0])
        self.assertLess(time.monotonic() - start, 5)

    # ---- shutdown ----------------------------------------------------------------

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

    def test_a_stop_during_a_failing_recovery_stops_at_once_without_stopping_the_server_again(self):
        instance = self.make(["a", "b"])
        instance.start_all()
        self.kill(instance.servers[1])
        self.launcher.BACKOFF_START_S = 30
        stops = []
        real_stop = instance.servers[1].stop

        def watching(timeout=self.launcher.STOP_TIMEOUT_S):
            stops.append(instance.stopping.is_set())
            real_stop(timeout)

        instance.servers[1].stop = watching

        def failing(server):
            raise self.launcher.ServerExited("exited again")

        instance.start_server_asleep = failing
        threading.Timer(0.5, instance.stopping.set).start()
        start = time.monotonic()
        with self.assertRaises(self.launcher.Stopping):
            instance.recover_with_backoff(instance.servers[1])
        self.assertLess(time.monotonic() - start, 5, "the 30 s backoff must not be waited out")
        self.assertNotIn(True, stops, "no server is stopped after the stop request")

    def test_the_whole_stop_path_fits_the_pods_grace_period(self):
        L = self.launcher
        self.assertEqual(L.GRACE_PERIOD_S, 300)
        # a stop request is seen within a poll; every group is then stopped at once
        self.assertLess(L.STATE_POLL_S + L.HEALTH_TIMEOUT_S + L.STOP_TIMEOUT_S + L.KILL_WAIT_S, L.GRACE_PERIOD_S)

    def test_a_server_that_ignores_sigterm_is_killed_within_the_budget(self):
        self.install_vllm('#!/usr/bin/env python3\nimport signal, time\nsignal.signal(signal.SIGTERM, signal.SIG_IGN)\ntime.sleep(60)\n')
        instance = self.make(["a"])
        server = instance.servers[0]
        server.start()
        time.sleep(0.5)
        self.launcher.KILL_WAIT_S = 5
        start = time.monotonic()
        server.stop(timeout=1)
        self.assertLess(time.monotonic() - start, 4)
        self.assertFalse(server.running())

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

    # ---- liveness and configuration --------------------------------------------------

    def test_every_request_is_progress(self):
        instance = self.make(["a"])
        instance.start_all()
        os.remove(self.launcher.ALIVE_FILE)
        instance.servers[0].healthy()
        self.assertTrue(os.path.exists(self.launcher.ALIVE_FILE))

    def test_progress_is_recorded_for_the_liveness_probe(self):
        self.assertFalse(os.path.exists(self.launcher.ALIVE_FILE))
        instance = self.make(["a"])
        instance.start_all()
        first = os.stat(self.launcher.ALIVE_FILE).st_mtime
        os.utime(self.launcher.ALIVE_FILE, (first - 100, first - 100))
        self.launcher.beat()
        self.assertGreater(os.stat(self.launcher.ALIVE_FILE).st_mtime, first - 50)

    def test_the_rendered_servers_file_shape_loads(self):
        path = os.path.join(self.tmp, "servers.json")
        Path(path).write_text(json.dumps({"sleepLevel": 2, "servers": [{"name": "a", "port": 1, "args": ["serve"]}]}))
        with open(path, encoding="utf-8") as f:
            instance = self.launcher.Launcher(json.load(f))
        self.assertEqual((instance.servers[0].level, instance.servers[0].port), (2, 1))


if __name__ == "__main__":
    unittest.main()
