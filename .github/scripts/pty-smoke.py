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


# Keep the raw equality contract. These helpers explain a failure without
# filtering kernel-managed state or normalizing bytes/integers away.
_TERMIOS_FLAG_NAMES = {
    "iflag": "IGNBRK BRKINT IGNPAR PARMRK INPCK ISTRIP INLCR IGNCR ICRNL IXON IXOFF IXANY IMAXBEL IUTF8 IUCLC",
    "oflag": "OPOST ONLCR OXTABS ONOEOT OCRNL ONOCR ONLRET OFILL OFDEL OLCUC",
    "cflag": "CSTOPB CREAD PARENB PARODD HUPCL CLOCAL CCTS_OFLOW CRTS_IFLOW CDTR_IFLOW CDSR_OFLOW CCAR_OFLOW CRTSCTS MDMBUF CMSPAR",
    "lflag": "ECHOKE ECHOE ECHOK ECHO ECHONL ECHOPRT ECHOCTL ISIG ICANON ALTWERASE IEXTEN EXTPROC TOSTOP FLUSHO NOKERNINFO PENDIN NOFLSH XCASE",
}
_TERMIOS_CC_NAMES = "VINTR VQUIT VERASE VKILL VEOF VTIME VMIN VSWTC VSWTCH VSTART VSTOP VSUSP VDSUSP VEOL VREPRINT VDISCARD VWERASE VLNEXT VEOL2 VSTATUS"


def _termios_cc_value(value):
    if isinstance(value, bytes):
        return {"type": "bytes", "hex": value.hex()}
    return {"type": type(value).__name__, "value": value}


def termios_diagnostics(before, after, constants):
    """Return complete snapshots plus field-specific changes for native CI."""
    field_names = ("iflag", "oflag", "cflag", "lflag", "ispeed", "ospeed")
    changes = {}
    for index, name in enumerate(field_names):
        old, new = before[index], after[index]
        if old == new:
            continue
        entry = {"before": old, "after": new, "before_hex": hex(old), "after_hex": hex(new)}
        if name in _TERMIOS_FLAG_NAMES:
            entry["xor_hex"] = hex(old ^ new)
            for direction, bits in (("set", new & ~old), ("cleared", old & ~new)):
                entry[direction] = [flag for flag in _TERMIOS_FLAG_NAMES[name].split()
                                    if (value := getattr(constants, flag, 0)) and value & (value - 1) == 0 and bits & value]
            if name == "cflag" and hasattr(constants, "CSIZE"):
                entry["character_size_before"] = old & constants.CSIZE
                entry["character_size_after"] = new & constants.CSIZE
        else:
            for label, value in (("before_names", old), ("after_names", new)):
                entry[label] = [key for key in dir(constants) if re.fullmatch(r"B\d+", key) and getattr(constants, key) == value]
        changes[name] = entry
    cc_changes = []
    for index in range(max(len(before[6]), len(after[6]))):
        old = before[6][index] if index < len(before[6]) else None
        new = after[6][index] if index < len(after[6]) else None
        if old != new:
            cc_changes.append({"index": index,
                               "names": [name for name in _TERMIOS_CC_NAMES.split() if getattr(constants, name, None) == index],
                               "before": _termios_cc_value(old), "after": _termios_cc_value(new)})
    if cc_changes:
        changes["cc"] = cc_changes

    def snapshot(values):
        result = dict(zip(field_names, values[:6]))
        result["cc"] = [_termios_cc_value(value) for value in values[6]]
        return result

    return {"changes": changes, "before": snapshot(before), "after": snapshot(after)}


def settled_termios_snapshot(fd, constants):
    """Settle pending line-discipline work without consuming any input.

    Darwin marks raw-to-canonical transitions PENDIN. FIONREAD calls ttnread,
    which runs ttypend before reporting readable bytes. The resulting snapshot
    can still be compared exactly; no flag or control character is ignored.
    https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/tty.c#L1664-L1672
    """
    import fcntl
    before = constants.tcgetattr(fd)
    count = struct.unpack("i", fcntl.ioctl(fd, constants.FIONREAD, struct.pack("i", 0)))[0]
    return before, constants.tcgetattr(fd), count


def raw_restore_baseline_probe(constants):
    """Measure native raw/restore behavior on a separate empty PTY."""
    import pty
    import tty
    master, slave = pty.openpty()
    try:
        _, initial, initial_readable = settled_termios_snapshot(slave, constants)
        tty.setraw(slave, when=constants.TCSANOW)
        constants.tcsetattr(slave, constants.TCSANOW, initial)
        restored = constants.tcgetattr(slave)
        constants.tcsetattr(slave, constants.TCSANOW, initial)
        restored_twice = constants.tcgetattr(slave)
        _, settled, readable = settled_termios_snapshot(slave, constants)
        result = {"raw_restore_changes": termios_diagnostics(initial, restored, constants)["changes"],
                  "repeated_restore_changes": termios_diagnostics(initial, restored_twice, constants)["changes"],
                  "settled_changes": termios_diagnostics(initial, settled, constants)["changes"],
                  "initial_readable_bytes": initial_readable, "settled_readable_bytes": readable,
                  "settled_matches_initial": initial == settled}
        print("Native Python-only termios baseline: " + json.dumps(result, sort_keys=True), flush=True)
        assert settled == initial, ("Python-only raw/restore baseline did not settle exactly",
                                    json.dumps(termios_diagnostics(initial, settled, constants), sort_keys=True))
        return result
    finally:
        os.close(master)
        os.close(slave)


def check(binary):
    if os.name != "posix":
        print("POSIX PTY smoke not applicable; Windows Console/ConPTY rendering remains a separate check")
        return
    import fcntl
    import pty
    import termios

    raw_restore_baseline_probe(termios)
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
                    _, initial, _ = settled_termios_snapshot(slave, termios)
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
                        unsmoothed_final, final, readable = settled_termios_snapshot(slave, termios)
                        if sys.platform == "darwin" and unsmoothed_final != initial:
                            print("Native PTY termios transition: " + json.dumps(
                                {"case": label,
                                 "unsmoothed_changes": termios_diagnostics(initial, unsmoothed_final, termios)["changes"],
                                 "settled_changes": termios_diagnostics(initial, final, termios)["changes"],
                                 "readable_bytes": readable}, sort_keys=True), flush=True)
                        assert final == initial, (label, "terminal modes were not restored",
                                                  json.dumps(termios_diagnostics(initial, final, termios), sort_keys=True),
                                                  {"exit_code": process.returncode, "profile_saved": (state / "profile.json").exists(),
                                                   "unsmoothed_changes": termios_diagnostics(initial, unsmoothed_final, termios)["changes"],
                                                   "readable_bytes": readable})
                        drain(0.03)
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
