"""Runs the vLLM servers of one kubemoot-vllm release in one container.

The chart renders the server list into servers.json and mounts this script
beside it. All servers share one accelerator, and a vLLM server measures the free
accelerator memory when it starts, so they start one at a time: start a server,
wait for /health, put it to sleep, start the next, and wake the first at the end.
A server whose process dies, or that is running but no longer answers /health, is
killed and restarted the same way, with every other server asleep first. Each
server runs as its own process group and is stopped as a group (the server and its
engine workers), and its log lines are prefixed with its name. Standard library only.

The launcher keeps two files for the pod's probes: READY_FILE exists while every
server is up (it is removed while a server is being recovered), and ALIVE_FILE is
touched whenever the launcher makes progress, so a launcher that is stuck stops
touching it and the liveness probe restarts the pod.

    python3 launcher.py /launcher/servers.json
"""

import http.client
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
ALIVE_FILE = "/tmp/launcher-alive"
POLL_S = 1.0
HEALTH_POLL_S = 2.0
# A running server whose /health fails this many checks in a row, each given
# HEALTH_TIMEOUT_S, is hung: it is killed and restarted like a dead one.
HEALTH_TIMEOUT_S = 10
HEALTH_FAILURES = 3
# The pod's termination grace period is 300 s. A stop request can arrive while a
# request is in flight (up to REQUEST_TIMEOUT_S), after which every server's group is
# stopped at once: SIGTERM and up to STOP_TIMEOUT_S, then SIGKILL and up to
# KILL_WAIT_S. The three must fit the period (checked by the tests).
GRACE_PERIOD_S = 300
REQUEST_TIMEOUT_S = 120
STOP_TIMEOUT_S = 120
KILL_WAIT_S = 10
HUNG_STOP_TIMEOUT_S = 10
BACKOFF_START_S = 5
BACKOFF_MAX_S = 60


class Stopping(Exception):
    """The launcher was asked to stop."""


class ServerExited(Exception):
    """A server's process ended before it answered /health."""


# What a failed attempt to recover a server can raise: the server exiting while it
# starts, or a server that answers badly or not at all.
RECOVERABLE = (ServerExited, OSError, RuntimeError, ValueError, http.client.HTTPException)


def beat():
    """Records progress for the liveness probe."""
    try:
        with open(ALIVE_FILE, "a", encoding="utf-8"):
            os.utime(ALIVE_FILE, None)
    except OSError:
        pass


def set_ready(ready):
    """Creates or removes the file the readiness probe looks for."""
    if ready:
        open(READY_FILE, "w", encoding="utf-8").close()
        return
    try:
        os.remove(READY_FILE)
    except FileNotFoundError:
        pass


def http_request(method, url, timeout=REQUEST_TIMEOUT_S, body=None):
    """The status of one request; an HTTP error answer returns its status, no answer raises.
    Each request is progress for the liveness probe, before it and after it."""
    beat()
    try:
        return _send(method, url, timeout, body)
    finally:
        beat()


def _send(method, url, timeout, body):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"} if data else {}
    request = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as reply:
            return reply.status, reply.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def post(url, body=None):
    status, _ = http_request("POST", url, body=body)
    if status >= 300:
        raise RuntimeError("POST %s returned %d" % (url, status))


class Server:
    """One `vllm serve` process group for one model."""

    def __init__(self, spec, level, stopping):
        self.name, self.port, self.args = spec["name"], spec["port"], spec["args"]
        self.level, self.stopping = level, stopping
        self.proc = None
        self.failed_checks = 0

    @property
    def url(self):
        return "http://127.0.0.1:%d" % self.port

    def running(self):
        return self.proc is not None and self.proc.poll() is None

    def start(self):
        self.failed_checks = 0
        self.proc = subprocess.Popen(
            ["vllm"] + self.args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, bufsize=1, start_new_session=True)
        threading.Thread(target=self._copy_log, args=(self.proc.stdout,), daemon=True).start()

    def _copy_log(self, stream):
        with stream:
            for line in stream:
                sys.stdout.write("[%s] %s" % (self.name, line))
                sys.stdout.flush()

    def healthy(self):
        """True when /health answers 200 within HEALTH_TIMEOUT_S."""
        try:
            return http_request("GET", self.url + "/health", timeout=HEALTH_TIMEOUT_S)[0] == 200
        except (OSError, http.client.HTTPException):
            return False

    def hung(self):
        """True once a running server has failed HEALTH_FAILURES health checks in a row."""
        if self.healthy():
            self.failed_checks = 0
            return False
        self.failed_checks += 1
        return self.failed_checks >= HEALTH_FAILURES

    def wait_ready(self):
        """Waits until /health answers 200. The process ending is the failure signal, not a clock."""
        while True:
            beat()
            if self.stopping.is_set():
                raise Stopping()
            if self.proc.poll() is not None:
                raise ServerExited("%s exited with %s before it answered /health" % (self.name, self.proc.returncode))
            if self.healthy():
                return
            time.sleep(HEALTH_POLL_S)

    def is_sleeping(self):
        status, body = http_request("GET", self.url + "/is_sleeping", timeout=HEALTH_TIMEOUT_S)
        if status != 200:
            raise RuntimeError("%s: /is_sleeping returned %d" % (self.name, status))
        return bool(json.loads(body).get("is_sleeping"))

    def sleep(self):
        post("%s/sleep?level=%d" % (self.url, self.level))

    def wake(self):
        """Wakes the server. After a level 2 sleep the weights are gone: restore the
        weight buffers, reload the weights from disk, then restore the key-value
        cache, in that order. After level 1 one call restores everything. The
        operator's vLLM adapter wakes a server the same way (operator/internal/engine/
        vllm/adapter.go, wake); keep the two in step."""
        if self.level == 2:
            post(self.url + "/wake_up?tags=weights")
            post(self.url + "/collective_rpc", {"method": "reload_weights"})
            post(self.url + "/wake_up?tags=kv_cache")
            return
        post(self.url + "/wake_up")

    def _signal(self, sig):
        """Signals the process group; False when the group is already gone."""
        try:
            os.killpg(self.proc.pid, sig)
            return True
        except ProcessLookupError:
            return False

    def _group_gone(self, timeout):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            beat()
            if self.proc.poll() is not None and not self._signal(0):
                return True
            time.sleep(0.2)
        return False

    def stop(self, timeout=STOP_TIMEOUT_S):
        """Stops the whole group and waits until every process of it is gone.

        The next server measures free accelerator memory when it starts, so every
        process holding memory must be gone first. A stopped process (SIGSTOP) holds a
        SIGTERM until it continues, so the group is continued first.
        """
        if self.proc is None:
            return
        self._signal(signal.SIGCONT)
        if self._signal(signal.SIGTERM) and not self._group_gone(timeout):
            self._signal(signal.SIGKILL)
            self._group_gone(KILL_WAIT_S)
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
        set_ready(True)

    def is_awake(self, server):
        """True when the server is awake. A server whose /health is down holds nothing
        that can be put to sleep and counts as not awake (it is restarted in its turn,
        as the operator's adapter also skips it); one that is healthy but cannot say is
        an error."""
        try:
            return not server.is_sleeping()
        except (OSError, RuntimeError, ValueError, http.client.HTTPException):
            if server.healthy():
                raise
            return False

    def awake_servers(self, besides):
        return [s for s in self.servers if s is not besides and s.running() and self.is_awake(s)]

    def recover(self, dead, awake, stop_timeout=STOP_TIMEOUT_S):
        """Restarts a server, with every other server asleep first.

        `awake` are the servers that were awake before the attempt; the first of them
        that is still running is woken again afterwards. When none was (the dead server
        was the awake one) the first running server is.
        """
        for server in awake:
            if server.running() and self.is_awake(server):
                server.sleep()
        dead.stop(stop_timeout)
        self.start_server_asleep(dead)
        # Only a healthy server can be woken: another hung one is restarted next.
        healthy = [s for s in self.servers if s.running() and s.healthy()]
        target = next((s for s in awake if s in healthy), None) or (healthy[0] if healthy else None)
        if target is None:
            raise RuntimeError("no healthy server to wake")
        target.wake()

    def recover_with_backoff(self, dead, stop_timeout=STOP_TIMEOUT_S):
        """Retries recover until it works. A server that exits during the restart, or
        one that does not answer, fails the attempt, not the launcher."""
        set_ready(False)
        delay = BACKOFF_START_S
        awake = None
        while not self.stopping.is_set():
            beat()
            try:
                if awake is None:
                    awake = self.awake_servers(dead)
                self.recover(dead, awake, stop_timeout)
                set_ready(True)
                return
            except RECOVERABLE as e:
                print("launcher: %s; retrying in %d s" % (e, delay), flush=True)
                dead.stop(stop_timeout)
                self.stopping.wait(delay)
                delay = min(delay * 2, BACKOFF_MAX_S)
        raise Stopping()

    def check_once(self):
        """Restarts the first server found dead or hung; True when one was."""
        for server in self.servers:
            if not server.running():
                print("launcher: %s is not running; restarting it" % server.name, flush=True)
                self.recover_with_backoff(server)
                return True
            if server.hung():
                print("launcher: %s is running but does not answer /health; killing and restarting it" % server.name, flush=True)
                self.recover_with_backoff(server, HUNG_STOP_TIMEOUT_S)
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
                beat()
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
