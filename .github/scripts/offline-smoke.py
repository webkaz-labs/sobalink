#!/usr/bin/env python3
"""Exercise installed soba with a fresh offline profile and loopback-only IPC/UI."""
import json
import os
import platform
import re
import pathlib
import subprocess
import sys
import tempfile


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
            snapshot = json.loads(run("status"))
            assert snapshot["settings"]["network"] == "none"
            assert snapshot["self"]["status"] == "idle"
            assert not snapshot["peers"] and not snapshot["services"]
            assert run("status", locale="ja") == run("status", locale="en"), "machine JSON changed with locale"
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
            assert json.loads(run("status"))["self"]["name"] == "example-device"
            assert json.loads(run("stop"))["state"] == "stopping"
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
    import http.cookiejar
    import urllib.error
    import urllib.request
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
    base = ui["url"]

    def request(path, data=None, headers=None, status=200):
        headers = {} if headers is None else headers
        raw = None if data is None else json.dumps(data).encode("utf-8")
        if raw is not None:
            headers = dict(headers, **{"Content-Type": "application/json"})
        try:
            response = client.open(urllib.request.Request(base + path, data=raw, headers=headers), timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read()
            assert response.status == status, (path, response.status, body)
            return body

    assert b"<html" in request("/").lower(), "embedded frontend absent"
    metadata = pathlib.Path(binary).parent.parent / "share" / "sobalink" / "build.json"
    if metadata.is_file():
        import hashlib
        for asset in json.loads(metadata.read_text(encoding="utf-8"))["frontend"]["assets"]:
            route = "/" if asset["path"] == "index.html" else "/" + asset["path"]
            assert hashlib.sha256(request(route)).hexdigest() == asset["sha256"], "embedded frontend differs from build inventory"
    request("/api/state", status=401)
    request("/api/state", headers={"Host": "attacker.example"}, status=403)
    request("/api/session", {"code": ui["code"]}, status=403)
    request("/api/session", {"code": ui["code"]}, {"Origin": base})
    assert json.loads(request("/api/state"))["settings"]["network"] == "none"
    request("/api/command", {"requestId": "smoke", "name": "network.login", "payload": {}}, {"Origin": base}, status=403)
    request("/api/session", {"code": ui["code"]}, {"Origin": base}, status=401)


if __name__ == "__main__":
    check(sys.argv[1])
