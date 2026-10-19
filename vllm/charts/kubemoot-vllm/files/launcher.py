"""Runs the vLLM servers of one kubemoot-vllm release in one container.

The chart renders the server list into servers.json and mounts this script
beside it. All servers share one accelerator, and a vLLM server measures the free
accelerator memory when it starts, so they start one at a time: start a server,
wait for /health, put it to sleep, start the next. Every server is asleep when the
launcher is done; the launcher never wakes one. Waking the server a call needs (and
putting the awake one to sleep first) is the Kubemoot operator's vLLM adapter, which
agents ask over NATS, so there is one place that wakes and sleeps.

A server is dead when its process has exited, and only then does the launcher
restart it, the same way, with every other server asleep first. The launcher never
kills a running server, however slowly it answers: the operator's vLLM adapter may be
waking or sleeping it, and a wake or sleep is judged by the state the server reports,
not by a clock. A running server that does not answer is skipped when the others are
put to sleep and takes the pod out of ready until it answers again.

A sleep is waited on without a deadline while anything is changing: a step answered,
or, once the steps are answered, a different state reported. Only a wait in which nothing at
all changes for NO_PROGRESS_LIMIT_S (a safety net) is given up (the wait for a server's first
/health is not bounded, because a first start downloads and compiles for a long time): the launcher
stops recording progress and exits, so the pod is restarted and the accelerator freed. Until
then a wait on a server that has not answered holds up restarting other servers that exit.
A running server that does not report its sleep state is not put to sleep before a restart,
so its memory use can make the next server's start fail, which is retried.

Each server runs as its own process group and is stopped as a group (the server and its
engine workers) only when the launcher is stopped or has to restart it after it exited.
Its log lines are prefixed with its name. Standard library only.

The launcher keeps two files for the pod's probes: READY_FILE exists while every server
is running and answering /health, and ALIVE_FILE is touched whenever the launcher makes
progress, so a launcher that is stuck stops touching it and the liveness probe restarts
the pod.

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
STATE_POLL_S = 1.0
# How long a read of /health or /is_sleeping may take. These reads only report; no
# decision to kill or restart is made from how long they take.
HEALTH_TIMEOUT_S = 10
# The pod's termination grace period is 300 s. A stop request is seen within one state
# poll or one health read (the reads of the servers run side by side), then every
# server's group is stopped at once: SIGTERM and up to STOP_TIMEOUT_S, then SIGKILL and
# up to KILL_WAIT_S. They must fit the period (checked by the tests).
GRACE_PERIOD_S = 300
STOP_TIMEOUT_S = 120
KILL_WAIT_S = 10
BACKOFF_START_S = 5
BACKOFF_MAX_S = 60
# A safety net, not flow control. A wait for a server's state ends on that state; it
# never ends on a clock while anything is changing. It is given up only when nothing
# at all has changed for this long: no step answered and the server's reported state
# the same. The launcher then stops recording progress and raises NoProgress, so the
# container exits, the pod restarts, and the accelerator is freed. The normal path (any
# state change, a slow level 2 reload whose steps are answering) never reaches it.
NO_PROGRESS_LIMIT_S = 1800


class Stopping(Exception):
    """The launcher was asked to stop."""


class NoProgress(Exception):
    """A wait for a server's state saw no change at all for NO_PROGRESS_LIMIT_S. Not
    recoverable: the launcher exits so the pod is restarted."""


class ServerExited(Exception):
    """A server's process has exited."""


# What a failed attempt to recover a server can raise: the server exiting while it
# starts or is put to sleep or woken, or a server that answers with an error.
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


def http_request(method, url, timeout=None, body=None):
    """The status of one request; an HTTP error answer returns its status, no answer
    raises. With no timeout it waits for the answer. Each request is progress for the
    liveness probe, before it and after it."""
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
    """A POST that waits for its answer however long the server takes."""
    status, _ = http_request("POST", url, body=body)
    if status >= 300:
        raise RuntimeError("POST %s returned %d" % (url, status))


class Server:
    """One `vllm serve` process group for one model."""

    def __init__(self, spec, level, stopping):
        self.name, self.port, self.args = spec["name"], spec["port"], spec["args"]
        self.level, self.stopping = level, stopping
        self.proc = None
        self.last_error = None
        self.reading = None  # what the last state read said, for the no-progress check

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

    def healthy(self):
        """True when /health answers 200 within HEALTH_TIMEOUT_S. A report, not a verdict."""
        try:
            return http_request("GET", self.url + "/health", timeout=HEALTH_TIMEOUT_S)[0] == 200
        except (OSError, http.client.HTTPException):
            return False

    def wait_ready(self):
        """Waits until /health answers 200. The process ending is the failure signal, not a clock."""
        while True:
            beat()
            self.check_alive()
            if self.healthy():
                return
            time.sleep(HEALTH_POLL_S)

    def check_alive(self):
        """Raises when the launcher is stopping or this server's process has exited."""
        if self.stopping.is_set():
            raise Stopping()
        if self.proc.poll() is not None:
            raise ServerExited("%s exited with %s" % (self.name, self.proc.returncode))

    def is_sleeping(self):
        status, body = http_request("GET", self.url + "/is_sleeping", timeout=HEALTH_TIMEOUT_S)
        if status != 200:
            raise RuntimeError("%s: /is_sleeping returned %d" % (self.name, status))
        sleeping = json.loads(body).get("is_sleeping")
        if not isinstance(sleeping, bool):
            raise ValueError("%s: /is_sleeping did not say whether it sleeps" % self.name)
        return sleeping

    def reached(self, asleep):
        """True when the server reports the wanted state (and, awake, answers /health). A
        read that fails is not an answer yet; it is logged once per distinct error, so a
        server that never answers (for example one started without VLLM_SERVER_DEV_MODE)
        is not waited on silently."""
        try:
            sleeping = self.is_sleeping()
            self.reading = "asleep" if sleeping else "awake"
            if sleeping != asleep or asleep:
                return sleeping == asleep
            if not self.healthy():
                self.reading = "awake, /health not answering"
                return False
            return True
        except (OSError, RuntimeError, ValueError, http.client.HTTPException) as e:
            self.reading = "error: %s" % e
            if str(e) != self.last_error:
                self.last_error = str(e)
                print("launcher: waiting for %s to report its state: %s" % (self.name, e), flush=True)
            return False

    def change(self, steps, asleep):
        """Sends the steps in a worker thread with no deadline, and waits until the server
        reports the wanted state. The wait ends on that state, on an error answer, on the
        server's process exiting, or on a stop request; never on elapsed time. A sleep that
        outlasts any single request therefore succeeds."""
        failures = []
        answered = []

        def send():
            try:
                for path, body in steps:
                    post(self.url + path, body)
                    answered.append(path)
            except (OSError, RuntimeError, ValueError, http.client.HTTPException) as e:
                failures.append(e)

        worker = threading.Thread(target=send, daemon=True)
        worker.start()
        seen, changed_at = None, time.monotonic()
        while True:
            if failures:
                raise failures[0]
            self.check_alive()
            if not worker.is_alive() and self.reached(asleep):
                return
            now = (len(answered), worker.is_alive(), self.reading)
            if now != seen:
                seen, changed_at = now, time.monotonic()
            elif time.monotonic() - changed_at > NO_PROGRESS_LIMIT_S:
                raise NoProgress("%s: no change in %d s while waiting to be %s"
                                 % (self.name, NO_PROGRESS_LIMIT_S, "asleep" if asleep else "awake"))
            beat()
            time.sleep(STATE_POLL_S)

    def sleep(self):
        self.change([("/sleep?level=%d" % self.level, None)], asleep=True)

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
        process holding memory must be gone first. The launcher stops a server only
        when it is shutting down or after the server's main process has exited and
        left the rest of its group behind.
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
        """Starts every server one at a time, each asleep before the next starts. All stay
        asleep: the operator's vLLM adapter wakes the one a call needs."""
        for server in self.servers:
            self.start_server_asleep(server)
        set_ready(True)

    def sleep_state(self, server):
        """"awake", "asleep", or None when the server does not report it. A server that
        does not report is skipped when the others are put to sleep: the launcher does
        not kill it, and it is restarted only if its process exits."""
        try:
            return "asleep" if server.is_sleeping() else "awake"
        except (OSError, RuntimeError, ValueError, http.client.HTTPException) as e:
            print("launcher: %s does not report its sleep state (%s); skipping it" % (server.name, e), flush=True)
            return None

    def is_awake(self, server):
        return self.sleep_state(server) == "awake"

    def possibly_awake(self, besides):
        """Every other running server not known to be asleep: awake ones, and ones that do
        not report, which are looked at again on each attempt."""
        return [s for s in self.servers if s is not besides and s.running() and self.sleep_state(s) != "asleep"]

    def recover(self, dead, awake):
        """Restarts a server whose process exited, with every other server asleep first,
        or brings an already restarted one to sleep after a failed attempt.

        `awake` are the servers that were possibly awake before the first attempt; each
        that reports it is awake now is put to sleep. Nothing is woken afterwards: the
        operator's vLLM adapter wakes a server when a call needs it.
        """
        for server in awake:
            if server.running() and self.sleep_state(server) == "awake":
                server.sleep()
        if not dead.running():
            dead.stop()
            self.start_server_asleep(dead)
        else:
            dead.wait_ready()
            if not dead.reached(True):
                dead.sleep()

    def recover_with_backoff(self, dead):
        """Retries recover until it works or the launcher is stopped. A server that exits
        during the restart, or one that answers with an error, fails the attempt, not the
        launcher. A server that is running is never stopped here."""
        set_ready(False)
        delay = BACKOFF_START_S
        awake = None
        while not self.stopping.is_set():
            beat()
            try:
                if awake is None:
                    awake = self.possibly_awake(dead)
                self.recover(dead, awake)
                set_ready(True)
                return
            except Stopping:
                break
            except RECOVERABLE as e:
                if self.stopping.is_set():
                    break
                print("launcher: %s; retrying in %d s" % (e, delay), flush=True)
                if not dead.running():
                    dead.stop()
                self.stopping.wait(delay)
                delay = min(delay * 2, BACKOFF_MAX_S)
        raise Stopping()

    def check_once(self):
        """Restarts the first server whose process has exited; True when one was."""
        for server in self.servers:
            if not server.running():
                print("launcher: %s has exited; restarting it" % server.name, flush=True)
                self.recover_with_backoff(server)
                return True
        return False

    def report_ready(self):
        """The pod is ready while every server is running and answering /health. The
        servers are asked side by side, so a stalled one delays a cycle by one read."""
        results = {}

        def ask(server):
            results[server.name] = server.running() and server.healthy()

        threads = [threading.Thread(target=ask, args=(s,)) for s in self.servers]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        set_ready(all(results.get(s.name) for s in self.servers))

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
                if not self.check_once():
                    self.report_ready()
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
