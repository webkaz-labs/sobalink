#!/usr/bin/env python3
"""Copy a verified pinned module and insert an experimental test-only guard.
Never edit GOMODCACHE or the application's dependency selection.
"""
import argparse
import hashlib
from pathlib import Path
import shutil
import prepare_remaining

PIN = "33fa42f7385928c729bf02f093a13537f03f8548e8d07268e47450c4af72acee"

def prepare(source: Path, destination: Path):
    original = (source / "wgengine/magicsock/rebinding_conn.go").read_bytes()
    if hashlib.sha256(original).hexdigest() != PIN:
        raise SystemExit("pinned rebinding source hash mismatch")
    prepare_remaining.verify(source)
    if destination.exists():
        raise SystemExit("destination must not exist")
    shutil.copytree(source, destination)
    # Go module-cache directories are read-only. Only this fresh private copy changes.
    for directory in [destination, *[p for p in destination.rglob("*") if p.is_dir()]]:
        directory.chmod(0o755)
    target = destination / "wgengine/magicsock/rebinding_conn.go"
    text = original.decode()
    text = text.replace("type RebindingUDPConn struct {", """type RebindingUDPConn struct {
    // Experiment only: no production constructor wires this policy.
    // Nil denies writes; test policy must be installed before publication.
    lanDestinationCheck func(netip.AddrPort) error
""", 1)
    for method, expression, result in [
        ("WriteWireGuardBatchTo", "addr.ap", "return err"),
        ("writeToUDPAddrPortWithInitPconn", "addr", "return 0, err"),
    ]:
        start = text.index("func (c *RebindingUDPConn) " + method + "(")
        end = text.index("\n}", start)
        body = text[start:end]
        old = "\n\tfor {"
        new = old + "\n\t\tif err := c.checkLANDestination(" + expression + "); err != nil { " + result + " }"
        if body.count(old) != 1:
            raise SystemExit("unexpected pinned write-loop structure")
        text = text[:start] + body.replace(old, new, 1) + text[end:]
    text += """
// checkLANDestination is an experiment hook, not an OS egress sandbox.
func (c *RebindingUDPConn) checkLANDestination(addr netip.AddrPort) error {
    if c.lanDestinationCheck == nil { return errors.New("LAN experiment: missing policy") }
    return c.lanDestinationCheck(addr)
}
"""
    target.chmod(0o644)
    target.write_text(text)
    # Build all production package code, but execute only this bounded fixture.
    # The upstream regression suite has unrelated network-bearing tests.
    for test in (destination / "wgengine/magicsock").glob("*_test.go"):
        test.unlink()
    fixture = Path(__file__).with_name("engine_test.go.txt")
    shutil.copyfile(fixture, destination / "wgengine/magicsock/lan_guard_experiment_test.go")
    prepare_remaining.prepare(source, destination)
    print("Pinned engine fixture prepared; application dependencies unchanged.")

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--destination", type=Path, required=True)
    args = parser.parse_args()
    prepare(args.source, args.destination)
