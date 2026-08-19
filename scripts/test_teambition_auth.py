from __future__ import annotations

import importlib.util
import pathlib
import unittest
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).with_name("teambition_auth.py")
SPEC = importlib.util.spec_from_file_location("teambition_auth", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
AUTH = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(AUTH)


class SessionManagerTests(unittest.TestCase):
    def test_rejects_non_loopback_cdp(self) -> None:
        with self.assertRaisesRegex(ValueError, "loopback"):
            AUTH.SessionManager("https://example.com:9222", "user", "password")

    @mock.patch.object(AUTH.requests, "get")
    def test_browser_ready_requires_websocket_url(self, request_get: mock.Mock) -> None:
        response = mock.Mock(ok=True)
        response.json.return_value = {"Browser": "Chrome"}
        request_get.return_value = response
        manager = AUTH.SessionManager("http://127.0.0.1:9222", "", "")
        self.assertFalse(manager.browser_ready())

        response.json.return_value = {
            "webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/browser/test"
        }
        self.assertTrue(manager.browser_ready())


if __name__ == "__main__":
    unittest.main()
