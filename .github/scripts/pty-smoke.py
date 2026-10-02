#!/usr/bin/env python3
"""Exercise a packaged CLI's real POSIX terminal, entirely offline.

Uses temporary legacy fixture profiles only. No node, login or enrollment is
started. Windows Console/ConPTY is a separate acceptance gate, not a POSIX PTY.
"""
import base64
import json
import os
import pathlib
import re
import select
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import time


def check(binary):
    if os.name != "posix":
        print("POSIX PTY smoke not applicable; Windows Console/ConPTY rendering remains a separate check")
        return
    import fcntl
    import pty
    import termios

    binary = str(pathlib.Path(binary).resolve())
    public_key = base64.b64encode(bytes(32)).decode("ascii")
    cases = [
        ("left-right", "per.example.test\x1b[H\x1b[C\x1b[Ce\x1b[F", "peer.example.test"),
        ("home-end-delete", "xpeer.example.test\x1b[H\x1b[3~\x1b[F", "peer.example.test"),
        ("japanese-backspace", "pee日本\x7f\x7fr.example.test", "peer.example.test"),
        ("combining-backspace", "peee\u0301\x7fr.example.test", "peer.example.test"),
        ("emoji-backspace", "pee👨‍👩‍👧‍👦\x7fr.example.test", "peer.example.test"),
        ("ctrl-d-delete", "xpeer.example.test\x1b[H\x04\x1b[F", "peer.example.test"),
        ("paste-multiline", "peer\x1b[200~\ny\nq\n\x1b[201~.example.test", "peer.example.test"),
        ("paste-oversized", "peer\x1b[200~" + "x" * (17 * 1024) + "\x1b[201~.example.test", "peer.example.test"),
        ("paste-no-next-answer", "\x1b[200~peer.example.test\n" + public_key + "\n\x1b[201~", None),
        ("escape", "\x1b", None),
        ("ctrl-c", "\x03", None),
        ("ctrl-d-empty", "\x04", None),
        ("interrupt", None, None),
    ]
    count = 0
    for language in ("en", "ja"):
        for columns in (30, 40, 80):
            for name, keys, expected_host in cases:
                with tempfile.TemporaryDirectory(prefix="tsnet-bridge-pty-") as tmp:
                    state = pathlib.Path(tmp) / "fixture"
                    master, slave = pty.openpty()
                    initial = termios.tcgetattr(slave)
                    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, columns, 0, 0))
                    # Reserve an unused loopback port only for the offline preflight.
                    # setup closes its preflight listener and does not start a node.
                    with socket.socket() as probe:
                        probe.bind(("127.0.0.1", 0))
                        port = probe.getsockname()[1]
                    command = [binary, "--lang", language, "--state-dir", str(state),
                               "setup", "--mode", "socks", "--socks-port", str(port),
                               "--key", public_key]
                    if name == "paste-no-next-answer":
                        command = command[:-2]
                    process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave,
                                               env=dict(os.environ, TERM="xterm-256color"),
                                               start_new_session=True)
                    output = bytearray()
                    label = f"{language}/{columns}/{name}"

                    def drain(wait=0.03):
                        end = time.monotonic() + wait
                        while time.monotonic() < end:
                            if select.select([master], [], [], min(0.01, max(0, end-time.monotonic())))[0]:
                                try:
                                    chunk = os.read(master, 65536)
                                except OSError:
                                    break
                                if not chunk:
                                    break
                                output.extend(chunk)

                    try:
                        deadline = time.monotonic() + 15
                        while time.monotonic() < deadline:
                            drain()
                            if not (termios.tcgetattr(slave)[3] & termios.ICANON) and b"Tailnet" in output:
                                break
                            assert process.poll() is None, (label, process.returncode, bytes(output))
                        else:
                            raise AssertionError((label, "raw terminal prompt did not appear", bytes(output)))
                        if keys is None:
                            process.send_signal(signal.SIGINT)
                        else:
                            data = keys.encode("utf-8") + (b"\r" if expected_host is not None else b"")
                            while data:
                                written = os.write(master, data)
                                data = data[written:]
                            if name == "paste-no-next-answer":
                                drain(0.15)
                                assert process.poll() is None and b"RustDesk" not in output, (label, "multiline paste advanced a prompt", bytes(output))
                                os.write(master, b"peer.example.test\r")
                                deadline = time.monotonic() + 10
                                while b"RustDesk" not in output and time.monotonic() < deadline:
                                    drain()
                                assert b"RustDesk" in output and process.poll() is None, (label, "separate prompt did not wait", bytes(output))
                                assert not (state / "profile.json").exists(), (label, "paste answered next prompt")
                                os.write(master, b"q\r")
                        deadline = time.monotonic() + 15
                        while process.poll() is None and time.monotonic() < deadline:
                            drain()
                        assert process.poll() is not None, (label, "terminal input did not complete", bytes(output))
                        drain(0.1)
                        assert termios.tcgetattr(slave) == initial, (label, "terminal modes were not restored")
                        assert re.search(rb"(?<!\r)\n", output) is None, (label, "bare LF staircases raw terminal display", bytes(output))
                        assert not any(sequence in output for sequence in (b"^[[A", b"^[[B", b"^[[C", b"^[[D")), (label, bytes(output))
                        if expected_host is not None:
                            assert process.returncode == 0, (label, process.returncode, bytes(output))
                            saved = json.loads((state / "profile.json").read_text(encoding="utf-8"))
                            assert saved["id_host"] == expected_host and saved["relay_host"] == expected_host, (label, saved["id_host"])
                        else:
                            assert process.returncode != 0, (label, "cancellation succeeded unexpectedly")
                            assert not (state / "profile.json").exists(), (label, "cancellation saved a profile")
                        assert not (state / "identity").exists(), (label, "offline prompt started a node")
                        count += 1
                    finally:
                        if process.poll() is None:
                            process.kill()
                            process.wait(timeout=5)
                        os.close(master)
                        os.close(slave)
    print(f"Packaged native POSIX PTY: {count} EN/JA edit, grapheme, bounded-paste and cancel checks passed at 30/40/80 columns; terminal restored")


if __name__ == "__main__":
    check(sys.argv[1])
