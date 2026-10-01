#!/usr/bin/env python3
"""Exercise the installed v2 CLI without node startup or external networking."""
import json
import pathlib
import subprocess
import sys
import tempfile


def check(binary):
    binary = str(pathlib.Path(binary).resolve())
    with tempfile.TemporaryDirectory(prefix="tsnet-bridge-v2-offline-") as tmp:
        root = pathlib.Path(tmp)
        state = root / "state"

        def run(*args, success=True):
            result = subprocess.run(
                [binary, "--state-dir", str(state), *args],
                capture_output=True, text=True, encoding="utf-8", timeout=30,
            )
            assert (result.returncode == 0) == success, (args, result.returncode, result.stdout, result.stderr)
            return result.stdout

        run("init")
        original = json.loads((state / "profile.json").read_text(encoding="utf-8"))
        assert original["version"] == 2 and original["mode"] == "rules"
        assert not original.get("rules")
        run("init", success=False)
        fixture = {
            "version": 2, "mode": "rules", "hostname": "example-bridge",
            "rules": [
                {"name": "ssh-demo", "purpose": "ssh", "direction": "forward", "network": "tcp", "listen_port": 22222, "target_host": "peer.example.invalid", "target_port": 22, "peer_id": "example-peer", "enabled": True},
                {"name": "udp-demo", "purpose": "custom", "direction": "forward", "network": "udp", "listen_port": 29000, "target_host": "peer.example.invalid", "target_port": 9000, "peer_id": "example-peer", "enabled": True},
                {"name": "api-demo", "purpose": "web", "direction": "share", "network": "tcp", "listen_port": 8080, "target_host": "127.0.0.1", "target_port": 8080, "allowed_peers": [{"id": "example-peer", "host": "peer.example.invalid"}], "enabled": True},
            ],
            "groups": [{"name": "demo", "rules": ["ssh-demo", "udp-demo"]}],
        }
        incoming = root / "incoming.json"
        incoming.write_text(json.dumps(fixture), encoding="utf-8")
        run("import", "--replace", str(incoming))
        assert json.loads((state / "profile.json").read_text(encoding="utf-8")) == original
        run("import", "--replace", "--confirm", str(incoming))
        saved = json.loads(run("rules", "--json"))
        assert len(saved["rules"]) == 3 and not any(r["enabled"] for r in saved["rules"])
        assert saved["hostname"] == original["hostname"], "import must not replace node name"
        settings = run("settings")
        assert "HostKeyAlias=peer.example.invalid" in settings
        assert "StrictHostKeyChecking=no" not in settings and "insecure" not in settings.lower()
        assert "demo:" in run("group", "list")
        export = root / "export.json"
        run("export", "--output", str(export))
        assert not export.exists(), "preview wrote a file"
        run("export", "--output", str(export), "--confirm")
        exported = json.loads(export.read_text(encoding="utf-8"))
        assert not any(r["enabled"] for r in exported["rules"])
        assert not any((state / name).exists() for name in ("identity", "credentials.json", "startup.log")), "offline setup unexpectedly started a node"
        plan = json.loads(run("autostart", "enable", "--json"))
        assert plan["action"] == "enable" and "idle" in plan["note"]
        assert not pathlib.Path(plan["path"]).exists(), "autostart preview changed registration"
        fixture["rules"][2]["target_host"] = "192.168.1.10"
        incoming.write_text(json.dumps(fixture), encoding="utf-8")
        run("import", "--replace", "--confirm", str(incoming), success=False)
        assert json.loads((state / "profile.json").read_text(encoding="utf-8")) == saved, "rejected import changed profile"
    print("Installed v2 offline init/import/export/settings/groups/autostart-preview checks passed")


if __name__ == "__main__":
    check(sys.argv[1])
