#!/usr/bin/env python3
"""Exercise installed soba with a fresh offline profile and loopback-only IPC/UI."""
import http.client
import http.cookiejar
import json
import os
import platform
import re
import pathlib
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request


class LocalWebClient:
    """Direct, persistent HTTP/1.1 for the loopback smoke server."""

    def __init__(self, base):
        target = urllib.parse.urlsplit(base)
        if (target.scheme != "http" or target.hostname != "127.0.0.1"
                or target.port is None or target.username is not None
                or target.password is not None or target.path not in ("", "/")
                or target.query or target.fragment):
            raise ValueError("expected a loopback HTTP origin with an explicit port")
        self.base = base.rstrip("/")
        self.jar = http.cookiejar.CookieJar()
        self.connection = http.client.HTTPConnection(target.hostname, target.port, timeout=5)

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.connection.close()

    def request(self, path, data=None, headers=None, status=200):
        if not path.startswith("/") or path.startswith("//"):
            raise ValueError("expected a local absolute path")
        headers = {} if headers is None else dict(headers)
        raw = None if data is None else json.dumps(data).encode("utf-8")
        if raw is not None:
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.base + path, data=raw, headers=headers)
        self.jar.add_cookie_header(request)
        # urllib's HTTPHandler forces Connection: close. An early rejection can
        # then close a Go server connection with an unread POST body, resetting
        # TCP on Windows. Keep HTTP/1.1 persistent as browsers do, and consume
        # each response before the next request or closing the connection.
        # HTTPConnection connects directly: no environment proxy or redirects.
        self.connection.request(request.get_method(), request.selector, body=raw,
                                headers=dict(request.header_items()))
        with self.connection.getresponse() as response:
            body = response.read()
            self.jar.extract_cookies(response, request)
            assert response.status == status, (path, response.status, "expected", status)
            return body


def check_locales(binary):
    # Exercise the packaged/installed executable, including the native read-only
    # fallback. Never modify OS preferences or start a node to test presentation.
    base = dict(os.environ)
    for key in ("LC_ALL", "LC_MESSAGES", "LANG"):
        base.pop(key, None)

    def run(args, env=None, success=True):
        result = subprocess.run([binary, *args], env=base if env is None else env,
                                capture_output=True, text=True, encoding="utf-8", timeout=30)
        assert (result.returncode == 0) == success, (args, result.returncode, result.stdout, result.stderr)
        return result

    english = run(["--locale", "en", "help"]).stdout
    japanese = run(["--locale", "ja", "help"]).stdout
    assert "local" in english and "ローカル" in japanese
    assert run(["help"], dict(base, LANG="ja_JP.UTF-8")).stdout == japanese
    assert run(["help"], dict(base, LANG="ja_JP.UTF-8", LC_MESSAGES="en_US.UTF-8")).stdout == english
    assert run(["help"], dict(base, LANG="ja_JP.UTF-8", LC_ALL="C")).stdout == english
    assert run(["help"], dict(base, LANG="xx_XX")).stdout == english
    native_ja = False
    if platform.system() == "Windows":
        import ctypes
        native_ja = (ctypes.windll.kernel32.GetUserDefaultUILanguage() & 0x3ff) == 0x11
    elif platform.system() == "Darwin":
        result = subprocess.run(["/usr/bin/defaults", "read", "-g", "AppleLanguages"],
                                capture_output=True, text=True, encoding="utf-8", timeout=5)
        if result.returncode == 0:
            first = result.stdout.strip().lstrip("(").strip().split(",")[0].splitlines()[0].strip().strip('"').rstrip(")").strip()
            native_ja = re.match(r"^ja(?:$|[-_.@])", first.lower()) is not None
    assert run(["help"]).stdout == (japanese if native_ja else english), "native locale fallback mismatch"
    for lang in ("ja", "en"):
        for topic in ("login", "connect", "share", "status", "message", "send", "retry", "stop"):
            text = run(["--locale", lang, "help", topic]).stdout
            assert "soba" in text
        error = run(["--locale", lang, "help", "unknown"], success=False).stderr
        assert ("不明" in error or "見つか" in error) if lang == "ja" else "Unknown command" in error
    print("Native packaged/installed Japanese/English, locale fallback and overrides passed")


def check(binary):
    binary = str(pathlib.Path(binary).resolve())
    check_locales(binary)
    with tempfile.TemporaryDirectory(prefix="soba-") as tmp:
        root = pathlib.Path(tmp)
        state = root / "state space"

        def run(*args, success=True, locale="en"):
            result = subprocess.run(
                [binary, "--state-dir", str(state), "--locale", locale, *args],
                capture_output=True, text=True, encoding="utf-8", timeout=30,
            )
            assert (result.returncode == 0) == success, (args, result.returncode, result.stdout, result.stderr)
            return result.stdout

        for args in (("--help",), ("version",), ("help", "share")):
            run(*args)
        assert not state.exists(), "help/version changed state"
        process = subprocess.Popen([binary, "--state-dir", str(state), "--locale", "en", "start"],
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding="utf-8")
        try:
            import time
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                assert process.poll() is None, process.communicate()
                probe = subprocess.run([binary, "--state-dir", str(state), "ui"],
                                       capture_output=True, text=True, encoding="utf-8", timeout=5)
                if probe.returncode == 0:
                    ui = json.loads(probe.stdout)
                    break
                time.sleep(0.05)
            else:
                raise AssertionError("private IPC and local UI did not become ready")
            snapshot = json.loads(run("status", "--json"))
            assert snapshot["settings"]["network"] == "none"
            assert snapshot["self"]["status"] == "idle"
            assert not snapshot["peers"] and not snapshot["services"]
            assert run("status", "--json", locale="ja") == run("status", "--json", locale="en"), "machine JSON changed with locale"
            run("start", success=False)
            assert process.poll() is None, "duplicate startup stopped the first agent"
            assert ui["url"].startswith("http://127.0.0.1:") and ui["code"]
            check_web(ui, binary)
            for args in (("login", "extra"), ("status", "extra"), ("message", "missing"),
                         ("share", "--name", "example", "--ports", "80"),
                         ("connect", "--name", "example", "--ports", "80"),
                         ("command", "unknown.action", "{}")):
                run(*args, success=False)
            run("setup", "--network", "none", "--name", "example-device")
            assert json.loads(run("status", "--json"))["self"]["name"] == "example-device"
            assert json.loads(run("stop", "--json"))["state"] == "stopping"
            stdout, stderr = process.communicate(timeout=15)
            assert process.returncode == 0, (stdout, stderr)
            assert "One-time code" not in stdout and ui["code"] not in stdout, "redirected startup exposed login code"
            assert not (state / "identity").exists(), "offline smoke unexpectedly enrolled a network node"
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate(timeout=5)
    print("Installed soba offline startup, private IPC, locale, Web authentication and clean shutdown passed")


def check_web(ui, binary):
    base = ui["url"]
    with LocalWebClient(base) as client:
        request = client.request
        assert b"<html" in request("/").lower(), "embedded frontend absent"
        metadata = pathlib.Path(binary).parent.parent / "share" / "sobalink" / "build.json"
        if metadata.is_file():
            import hashlib
            for asset in json.loads(metadata.read_text(encoding="utf-8"))["frontend"]["assets"]:
                route = "/" if asset["path"] == "index.html" else "/" + asset["path"]
                assert hashlib.sha256(request(route)).hexdigest() == asset["sha256"], "embedded frontend differs from build inventory"
        assert json.loads(request("/api/state", status=401))["code"] == "unauthenticated"
        assert json.loads(request("/api/state", headers={"Host": "attacker.example"}, status=403))["code"] == "local_only"
        # Every rejection must return its complete expected response. These are
        # repeated native transport checks, never retries of failed requests.
        for _ in range(10):
            assert json.loads(request("/api/session", {"code": ui["code"]}, status=403))["code"] == "origin"
        request("/api/session", {"code": ui["code"]}, {"Origin": base})
        assert json.loads(request("/api/state"))["settings"]["network"] == "none"
        for _ in range(10):
            body = request("/api/command", {"requestId": "smoke", "name": "network.login", "payload": {}}, {"Origin": base}, status=403)
            assert json.loads(body)["code"] == "csrf"
            assert json.loads(request("/api/state"))["settings"]["network"] == "none"
        assert json.loads(request("/api/session", {"code": ui["code"]}, {"Origin": base}, status=401))["code"] == "invalid_code"


if __name__ == "__main__":
    check(sys.argv[1])
