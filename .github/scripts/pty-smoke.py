#!/usr/bin/env python3
"""Exercise a packaged CLI's real POSIX terminal, entirely offline.

The Web UI owns human input; the CLI never enters raw terminal mode.
Help/version must leave native terminal state untouched without starting a node. Windows Console/ConPTY is a separate acceptance gate, not a POSIX PTY.
"""
import json
import os
import pathlib
import re
import select
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
    count = 0
    for language in ("en", "ja"):
        for columns in (30, 40, 80):
            for args in (("--help",), ("share", "--help"), ("connect", "--help"), ("version",)):
                with tempfile.TemporaryDirectory(prefix="sobalink-pty-") as tmp:
                    master, slave = pty.openpty()
                    process = None
                    try:
                        _, initial, _ = settled_termios_snapshot(slave, termios)
                        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, columns, 0, 0))
                        command = [binary, "--locale", language, "--state-dir", str(pathlib.Path(tmp) / "absent"), *args]
                        process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave)
                        output = bytearray()
                        deadline = time.monotonic() + 10
                        while time.monotonic() < deadline:
                            if select.select([master], [], [], 0.05)[0]:
                                output.extend(os.read(master, 65536))
                            if process.poll() is not None:
                                while select.select([master], [], [], 0)[0]:
                                    output.extend(os.read(master, 65536))
                                break
                        assert process.poll() is not None, "CLI help waited for interactive input"
                        assert process.returncode == 0, output.decode("utf-8")
                        _, final, _ = settled_termios_snapshot(slave, termios)
                        assert final == initial, json.dumps(termios_diagnostics(initial, final, termios), sort_keys=True)
                        assert not (pathlib.Path(tmp) / "absent").exists(), "terminal help wrote state"
                        assert re.search(rb"(?<!\r)\n", output) is None, "bare LF staircases terminal output"
                        count += 1
                    finally:
                        if process is not None and process.poll() is None:
                            process.kill()
                            process.wait(timeout=5)
                        os.close(master)
                        os.close(slave)
    print(f"Packaged soba POSIX PTY: {count} EN/JA help/version checks at 30/40/80 columns; terminal state preserved")


if __name__ == "__main__":
    check(sys.argv[1])
