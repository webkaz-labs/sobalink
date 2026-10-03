"""Socket-free checks of the real HTTP serializer/parser used by offline smoke."""
import importlib.util
import io
import json
import os
import pathlib
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("offline_smoke", pathlib.Path(__file__).with_name("offline-smoke.py"))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)
ORIGIN = "http://127.0.0.1:43210"


def response(status, body, headers=()):
    return (f"HTTP/1.1 {status} Fixture\r\nContent-Length: {len(body)}\r\n"
            + "".join(f"{name}: {value}\r\n" for name, value in headers)
            + "\r\n").encode("ascii") + body


class ResponseBytes(io.BytesIO):
    def close(self):
        if not self.closed:
            if self.tell() != len(self.getvalue()):
                raise AssertionError("response closed before its complete body was read")
        super().close()


class ScriptedSocket:
    """Supply HTTP responses in memory; HTTPConnection still handles the wire."""

    def __init__(self, responses):
        self.responses = list(responses)
        self.writes = []
        self.readers = []
        self.closed = False

    def setsockopt(self, *args):
        pass

    def sendall(self, data):
        if self.closed or any(not reader.closed for reader in self.readers):
            raise AssertionError("request sent before the previous response finished")
        self.writes.append(bytes(data))

    def makefile(self, mode):
        if mode != "rb":
            raise AssertionError("unexpected response stream mode")
        reader = ResponseBytes(self.responses.pop(0))
        self.readers.append(reader)
        return reader

    def close(self):
        if any(not reader.closed for reader in self.readers):
            raise AssertionError("socket closed before response was consumed")
        self.closed = True


class LocalWebTransportTests(unittest.TestCase):
    def test_rejected_post_preserves_cookie_and_persistent_connection(self):
        forbidden = b'{"code":"csrf","error":"fixture rejection"}'
        sock = ScriptedSocket([
            response(200, b'{}', [("Set-Cookie", "soba_session=fixture-session; Path=/; HttpOnly; SameSite=Strict")]),
            response(403, forbidden),
            response(200, b'{"settings":{"network":"none"}}'),
        ])
        payload = {"requestId": "smoke", "name": "network.login", "payload": {}}
        with patch("socket.create_connection", return_value=sock) as connect:
            with smoke.LocalWebClient(ORIGIN) as client:
                client.request("/api/session", {"code": "fixture-code"}, {"Origin": ORIGIN})
                self.assertEqual(client.request("/api/command", payload, {"Origin": ORIGIN}, status=403), forbidden)
                self.assertEqual(json.loads(client.request("/api/state"))["settings"]["network"], "none")
                self.assertFalse(sock.closed)
            connect.assert_called_once()
            self.assertEqual(connect.call_args.args[:2], (("127.0.0.1", 43210), 5))
        wire = b"".join(sock.writes)
        self.assertEqual(wire.count(b" HTTP/1.1\r\n"), 3)
        self.assertNotIn(b"connection: close", wire.lower())
        self.assertIn(b"Content-type: application/json\r\n", wire)
        self.assertIn(f"Content-Length: {len(json.dumps(payload).encode('utf-8'))}\r\n".encode("ascii"), wire)
        self.assertEqual(wire.count(b"Cookie: soba_session=fixture-session\r\n"), 2)
        self.assertNotIn(b"x-csrf-token:", wire.lower())
        self.assertIn(json.dumps(payload).encode("utf-8"), wire)
        self.assertTrue(sock.closed)
        self.assertFalse(sock.responses)

    def test_proxy_environment_and_host_override_do_not_change_tcp_target(self):
        sock = ScriptedSocket([response(403, b'{"code":"local_only"}')])
        proxy = "http://proxy.example:8080"
        with patch.dict(os.environ, {"HTTP_PROXY": proxy, "HTTPS_PROXY": proxy, "ALL_PROXY": proxy, "http_proxy": proxy, "NO_PROXY": ""}):
            with patch("socket.create_connection", return_value=sock) as connect:
                with smoke.LocalWebClient(ORIGIN) as client:
                    client.request("/api/state", headers={"Host": "attacker.example"}, status=403)
                self.assertEqual(connect.call_args.args[0], ("127.0.0.1", 43210))
        wire = b"".join(sock.writes)
        self.assertIn(b"GET /api/state HTTP/1.1\r\n", wire)
        self.assertIn(b"Host: attacker.example\r\n", wire)

    def test_unexpected_status_fails_without_redirect_retry_or_body_disclosure(self):
        for status in (200, 302, 401):
            with self.subTest(status=status):
                sock = ScriptedSocket([response(status, b"private-fixture-body", [("Location", "http://example.com/")])])
                with patch("socket.create_connection", return_value=sock) as connect:
                    with self.assertRaises(AssertionError) as failure:
                        with smoke.LocalWebClient(ORIGIN) as client:
                            client.request("/api/command", {}, status=403)
                    connect.assert_called_once()
                self.assertNotIn("private-fixture-body", str(failure.exception))
                self.assertEqual(b"".join(sock.writes).count(b"POST /api/command HTTP/1.1\r\n"), 1)
                self.assertTrue(sock.closed)

    def test_connection_reset_is_not_retried_or_treated_as_rejection(self):
        sock = ScriptedSocket([])
        with patch("socket.create_connection", return_value=sock) as connect:
            with patch.object(sock, "makefile", side_effect=ConnectionResetError("fixture reset")):
                with self.assertRaises(ConnectionResetError):
                    with smoke.LocalWebClient(ORIGIN) as client:
                        client.request("/api/command", {}, status=403)
            connect.assert_called_once()
        self.assertEqual(b"".join(sock.writes).count(b"POST /api/command HTTP/1.1\r\n"), 1)
        self.assertTrue(sock.closed)

    def test_only_loopback_origins_and_local_paths_are_accepted(self):
        with patch("socket.create_connection", side_effect=AssertionError("must not connect")):
            for base in ("http://example.com:43210", "http://127.0.0.1", "https://127.0.0.1:43210",
                         "http://user@127.0.0.1:43210", ORIGIN + "/other", ORIGIN + "?query", ORIGIN + "#fragment"):
                with self.subTest(base=base), self.assertRaises(ValueError):
                    smoke.LocalWebClient(base)
            with smoke.LocalWebClient(ORIGIN) as client:
                for path in ("http://example.com/", "//example.com/", "api/state"):
                    with self.subTest(path=path), self.assertRaises(ValueError):
                        client.request(path)


if __name__ == "__main__":
    unittest.main()
