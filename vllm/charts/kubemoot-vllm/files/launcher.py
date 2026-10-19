"""Runs the vLLM servers of one kubemoot-vllm release in one container.

The chart renders the server list into servers.json and mounts this script
beside it. All servers share one accelerator, and a vLLM server measures the free
accelerator memory when it starts, so they start one at a time: start a server,
wait for /health, put it to sleep, start the next, and wake the first at the end.
A server whose process dies is restarted the same way, with every other server
asleep first. Each server runs as its own process group and is stopped as a group
(the server and its engine workers), and its log lines are prefixed with its
name. Standard library only.

    python3 launcher.py /launcher/servers.json
"""

import json
import os
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

READY_FILE = "/tmp/launcher-ready"
POLL_S = 1.0
HEALTH_POLL_S = 2.0
REQUEST_TIMEOUT_S = 600
STOP_TIMEOUT_S = 120
BACKOFF_START_S = 5
BACKOFF_MAX_S = 60


class Stopping(Exception):
    """The launcher was asked to stop."""


class ServerExited(Exception):
    """A server's process ended before it answered /health."""


def http(method, url, timeout=REQUEST_TIMEOUT_S, body=None):
    """The status of one request; an HTTP error answer returns its status, no answer raises."""
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"} if data else {}
    request = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as reply:
            return reply.status, reply.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def post(url, body=None):
    status, _ = http("POST", url, body=body)
    if status >= 300:
        raise RuntimeError("POST %s returned %d" % (url, status))


class Server:
    """One `vllm serve` process group for one model."""

    def __init__(self, spec, level, stopping):
        self.name, self.port, self.args = spec["name"], spec["port"], spec["args"]
        self.level, self.stopping = level, stopping
        self.proc = None

    @property
    def url(self):
        return "http://127.0.0.1:%d" % self.port

    def running(self):
        return self.proc is not None and self.proc.poll() is None

    def start(self):
        self.proc = subprocess.Popen(
            ["vllm"] + self.args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, bufsize=1, start_new_session=True)
        threading.Thread(target=self._copy_log, args=(self.proc.stdout,), daemon=True).start()

    def _copy_log(self, stream):
        with stream:
            for line in stream:
                sys.stdout.write("[%s] %s" % (self.name, line))
                sys.stdout.flush()

    def wait_ready(self):
        """Waits until /health answers 200. The process ending is the failure signal, not a clock."""
        while True:
            if self.stopping.is_set():
                raise Stopping()
            if self.proc.poll() is not None:
                raise ServerExited("%s exited with %s before it answered /health" % (self.name, self.proc.returncode))
            try:
                if http("GET", self.url + "/health", timeout=5)[0] == 200:
                    return
            except OSError:
                pass
            time.sleep(HEALTH_POLL_S)

    def is_sleeping(self):
        status, body = http("GET", self.url + "/is_sleeping", timeout=30)
        if status != 200:
            raise RuntimeError("%s: /is_sleeping returned %d" % (self.name, status))
        return bool(json.loads(body).get("is_sleeping"))

    def sleep(self):
        post("%s/sleep?level=%d" % (self.url, self.level))

    def wake(self):
        """From level 2 the weights are reloaded before the cache returns."""
        if self.level == 1:
            post(self.url + "/wake_up")
            return
        post(self.url + "/wake_up?tags=weights")
        post(self.url + "/collective_rpc", {"method": "reload_weights"})
        post(self.url + "/wake_up?tags=kv_cache")

    def _signal(self, sig):
        """Signals the process group; False when the group is already gone."""
        try:
            os.killpg(self.proc.pid, sig)
            return True
        except ProcessLookupError:
            return False

    def _group_gone(self):
        deadline = time.monotonic() + STOP_TIMEOUT_S
        while time.monotonic() < deadline:
            if self.proc.poll() is not None and not self._signal(0):
                return True
            time.sleep(0.2)
        return False

    def stop(self):
        """Stops the whole group and waits until every process of it is gone.

        The next server measures free accelerator memory when it starts, so every
        process holding memory must be gone first.
        """
        if self.proc is None:
            return
        if self._signal(signal.SIGTERM) and not self._group_gone():
            self._signal(signal.SIGKILL)
            self._group_gone()
        self.proc = None


class Launcher:
    def __init__(self, config, stopping=None):
        self.stopping = stopping or threading.Event()
        level = config.get("sleepLevel", 1)
        self.servers = [Server(s, level, self.stopping) for s in config["servers"]]

    def start_server_asleep(self, server):
        server.start()
        server.wait_ready()
        server.sleep()

    def start_all(self):
        """Starts every server one at a time, each asleep before the next starts, then wakes the first."""
        for server in self.servers:
            self.start_server_asleep(server)
        self.servers[0].wake()
        open(READY_FILE, "w").close()

    def awake_servers(self, besides):
        return [s for s in self.servers if s is not besides and s.running() and not s.is_sleeping()]

    def recover(self, dead):
        """Restarts a server whose process died, with every other server asleep first.

        The one that was awake is woken again afterwards; when the dead server was
        the awake one, the first server is.
        """
        awake = self.awake_servers(dead)
        for server in awake:
            server.sleep()
        dead.stop()
        self.start_server_asleep(dead)
        # Only a running server can be woken; another dead one is restarted next.
        (awake or [s for s in self.servers if s.running()])[0].wake()

    def recover_with_backoff(self, dead):
        """Retries recover until it works. A server that exits during the restart, or
        one that does not answer (a wedged one), fails the attempt, not the launcher."""
        delay = BACKOFF_START_S
        while not self.stopping.is_set():
            try:
                self.recover(dead)
                return
            except (ServerExited, OSError, RuntimeError) as e:
                print("launcher: %s; retrying in %d s" % (e, delay), flush=True)
                dead.stop()
                self.stopping.wait(delay)
                delay = min(delay * 2, BACKOFF_MAX_S)
        raise Stopping()

    def check_once(self):
        """Restarts the first server found dead; True when one was."""
        for server in self.servers:
            if not server.running():
                print("launcher: %s is not running; restarting it" % server.name, flush=True)
                self.recover_with_backoff(server)
                return True
        return False

    def stop_all(self):
        """Stops every server at once, so the time to stop does not grow with the
        number of servers (the pod's grace period is fixed)."""
        threads = [threading.Thread(target=s.stop) for s in self.servers]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

    def run(self):
        signal.signal(signal.SIGTERM, lambda *_: self.stopping.set())
        signal.signal(signal.SIGINT, lambda *_: self.stopping.set())
        try:
            self.start_all()
            while not self.stopping.is_set():
                self.check_once()
                self.stopping.wait(POLL_S)
        except Stopping:
            pass
        finally:
            self.stop_all()
        return 0


def main(argv):
    with open(argv[1], encoding="utf-8") as f:
        config = json.load(f)
    return Launcher(config).run()


if __name__ == "__main__":
    sys.exit(main(sys.argv))
