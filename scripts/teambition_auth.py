#!/usr/bin/env python3
"""Loopback-only Teambition browser session manager.

The service owns no browser profile. It attaches to a long-running Chromium
through CDP, restores the session when needed, and never returns cookie values.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import quote, urlparse

import requests
import websocket


LOG = logging.getLogger("teambition-auth")
TOKEN_COOKIE = "TB_ACCESS_TOKEN"
PASSWORD_LOGIN_URL = "https://account.teambition.com/login/password"


class AuthenticationError(RuntimeError):
    pass


class CDPPage:
    def __init__(self, endpoint: str, start_url: str = "about:blank") -> None:
        self.endpoint = endpoint.rstrip("/")
        response = requests.put(
            f"{self.endpoint}/json/new?{quote(start_url, safe=':/?&=%')}",
            timeout=10,
        )
        response.raise_for_status()
        target = response.json()
        self.target_id = str(target["id"])
        self.socket = websocket.create_connection(
            target["webSocketDebuggerUrl"],
            timeout=10,
            origin="http://127.0.0.1",
        )
        self._next_id = 0

    def close(self) -> None:
        try:
            self.socket.close()
        finally:
            try:
                requests.get(
                    f"{self.endpoint}/json/close/{self.target_id}", timeout=5
                )
            except requests.RequestException:
                pass

    def call(self, method: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
        self._next_id += 1
        message_id = self._next_id
        self.socket.send(
            json.dumps(
                {"id": message_id, "method": method, "params": params or {}},
                ensure_ascii=True,
            )
        )
        while True:
            payload = json.loads(self.socket.recv())
            if payload.get("id") != message_id:
                continue
            if "error" in payload:
                raise AuthenticationError(
                    f"CDP {method} failed: {payload['error'].get('message', 'unknown error')}"
                )
            return payload.get("result", {})

    def evaluate(self, expression: str) -> Any:
        result = self.call(
            "Runtime.evaluate",
            {
                "expression": expression,
                "returnByValue": True,
                "awaitPromise": True,
                "userGesture": True,
            },
        )
        remote = result.get("result", {})
        if remote.get("subtype") == "error":
            raise AuthenticationError(remote.get("description", "browser expression failed"))
        return remote.get("value")

    def navigate(self, url: str, timeout: float = 30) -> None:
        self.call("Page.enable")
        result = self.call("Page.navigate", {"url": url})
        if result.get("errorText"):
            raise AuthenticationError(f"open login page: {result['errorText']}")
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                if self.evaluate("document.readyState") in {"interactive", "complete"}:
                    return
            except AuthenticationError:
                pass
            time.sleep(0.25)
        raise AuthenticationError("login page did not become ready")

    def cookie_names(self) -> set[str]:
        cookies = self.call("Network.getAllCookies").get("cookies", [])
        return {str(cookie.get("name", "")) for cookie in cookies}

    def focus_input(self, selectors: list[str]) -> bool:
        encoded = json.dumps(selectors, ensure_ascii=True)
        return bool(
            self.evaluate(
                f"""(() => {{
  const selectors = {encoded};
  const element = selectors.map(selector => document.querySelector(selector))
    .find(item => item && !item.disabled && item.getClientRects().length);
  if (!element) return false;
  element.focus();
  element.select();
  return true;
}})()"""
            )
        )

    def replace_focused_text(self, value: str) -> None:
        self.call("Input.dispatchKeyEvent", {"type": "keyDown", "key": "Backspace"})
        self.call("Input.dispatchKeyEvent", {"type": "keyUp", "key": "Backspace"})
        self.call("Input.insertText", {"text": value})

    def click_agreement(self) -> bool:
        return bool(
            self.evaluate(
                r"""(() => {
  const visible = element => element && element.getClientRects().length;
  const controls = Array.from(document.querySelectorAll(
    "input[type='checkbox'], input[type='radio'], [role='checkbox'], [role='radio']"
  )).filter(visible);
  let control = controls.find(element => {
    const parent = element.closest('label') || element.parentElement;
    const text = (parent?.textContent || '').replace(/\s+/g, '');
    return text.includes('同意') || text.includes('服务条款') || /agree/i.test(text);
  }) || controls[0];
  if (!control) {
    const textNode = Array.from(document.querySelectorAll('label, span, div'))
      .filter(visible)
      .find(element => {
        const text = (element.textContent || '').replace(/\s+/g, '');
        return text.includes('同意') && (text.includes('服务条款') || text.includes('隐私'));
      });
    control = textNode?.closest('label, [role="checkbox"], [role="radio"]') || textNode;
  }
  if (!control) return false;
  const checked = control.checked === true || control.getAttribute('aria-checked') === 'true';
  if (!checked) control.click();
  return true;
})()"""
            )
        )

    def button_center(self) -> dict[str, float] | None:
        result = self.evaluate(
            r"""(() => {
  const visible = element => element && element.getClientRects().length;
  const buttons = Array.from(document.querySelectorAll("button, input[type='submit'], [role='button']"))
    .filter(visible);
  const button = buttons.find(element => {
    const text = (element.textContent || element.value || '').replace(/\s+/g, '');
    return text === '登录' || text === 'LogIn' || text === 'Login' || text === 'Startnow';
  }) || document.querySelector("button.account-btn, button[type='submit'], input[type='submit']");
  if (!visible(button)) return null;
  const rect = button.getBoundingClientRect();
  return {x: rect.left + rect.width / 2, y: rect.top + rect.height / 2, enabled: !button.disabled && button.getAttribute('aria-disabled') !== 'true'};
})()"""
        )
        return result if isinstance(result, dict) else None

    def click_at(self, point: dict[str, float]) -> None:
        params = {
            "x": float(point["x"]),
            "y": float(point["y"]),
            "button": "left",
            "clickCount": 1,
        }
        self.call("Input.dispatchMouseEvent", {**params, "type": "mousePressed"})
        self.call("Input.dispatchMouseEvent", {**params, "type": "mouseReleased"})

    def safe_page_state(self) -> tuple[str, str]:
        state = self.evaluate(
            r"""(() => ({
  url: location.href,
  text: (document.body?.innerText || '').replace(/\s+/g, ' ').slice(0, 600)
}))()"""
        )
        if not isinstance(state, dict):
            return "", ""
        return str(state.get("url", "")), str(state.get("text", ""))


class SessionManager:
    def __init__(self, cdp_url: str, username: str, password: str) -> None:
        parsed = urlparse(cdp_url)
        if parsed.hostname not in {"127.0.0.1", "localhost", "::1"}:
            raise ValueError("CDP endpoint must be loopback-only")
        self.cdp_url = cdp_url.rstrip("/")
        self.username = username.strip()
        self.password = password
        self.lock = threading.Lock()

    def browser_ready(self) -> bool:
        try:
            response = requests.get(f"{self.cdp_url}/json/version", timeout=3)
            return response.ok and bool(response.json().get("webSocketDebuggerUrl"))
        except (requests.RequestException, ValueError):
            return False

    def status(self) -> dict[str, Any]:
        if not self.browser_ready():
            return {"ok": False, "authenticated": False, "browser_ready": False}
        page = CDPPage(self.cdp_url)
        try:
            authenticated = TOKEN_COOKIE in page.cookie_names()
            return {
                "ok": True,
                "authenticated": authenticated,
                "browser_ready": True,
                "credentials_configured": bool(self.username and self.password),
            }
        finally:
            page.close()

    def ensure(self) -> dict[str, Any]:
        with self.lock:
            page = CDPPage(self.cdp_url)
            try:
                if TOKEN_COOKIE in page.cookie_names():
                    LOG.info("existing Teambition session is available")
                    return {"ok": True, "authenticated": True, "reused": True}
                if not self.username or not self.password:
                    raise AuthenticationError("Teambition credentials are not configured")
                LOG.info("Teambition session is absent; starting password login")
                page.navigate(PASSWORD_LOGIN_URL)
                username_ready = page.focus_input(
                    [
                        "input[autocomplete='username']",
                        "input[type='email']",
                        "input[type='tel']",
                        "input:not([type='password'])",
                    ]
                )
                if not username_ready:
                    raise AuthenticationError("username input was not found")
                page.replace_focused_text(self.username)
                if not page.focus_input(
                    ["input[autocomplete='current-password']", "input[type='password']"]
                ):
                    raise AuthenticationError("password input was not found")
                page.replace_focused_text(self.password)
                if not page.click_agreement():
                    raise AuthenticationError("agreement control was not found")
                point = None
                enabled = False
                for _ in range(40):
                    time.sleep(0.25)
                    point = page.button_center()
                    enabled = bool(point and point.get("enabled"))
                    if enabled:
                        break
                if point is None:
                    raise AuthenticationError("login button was not found")
                if not enabled:
                    raise AuthenticationError("login button stayed disabled after form input")
                page.click_at(point)

                deadline = time.monotonic() + 45
                while time.monotonic() < deadline:
                    if TOKEN_COOKIE in page.cookie_names():
                        LOG.info("Teambition password login completed")
                        return {"ok": True, "authenticated": True, "reused": False}
                    time.sleep(1)
                current_url, page_text = page.safe_page_state()
                manual_markers = ("验证码", "扫码", "安全验证", "滑块", "captcha")
                manual_required = any(
                    marker.lower() in page_text.lower() for marker in manual_markers
                )
                LOG.warning(
                    "Teambition login did not complete url=%s manual_required=%s",
                    current_url,
                    manual_required,
                )
                message = "login requires manual verification" if manual_required else "login did not produce a valid session"
                raise AuthenticationError(message)
            finally:
                page.close()


class AuthHTTPServer(ThreadingHTTPServer):
    manager: SessionManager


class Handler(BaseHTTPRequestHandler):
    server: AuthHTTPServer

    def log_message(self, format: str, *args: Any) -> None:
        LOG.info("http %s", format % args)

    def send_json(self, status: int, payload: dict[str, Any]) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:
        if self.path == "/health":
            status = self.server.manager.status()
            self.send_json(200 if status.get("browser_ready") else 503, status)
            return
        if self.path == "/session/status":
            status = self.server.manager.status()
            self.send_json(200 if status.get("ok") else 503, status)
            return
        self.send_json(404, {"ok": False, "error": "not found"})

    def do_POST(self) -> None:
        if self.path != "/session/ensure":
            self.send_json(404, {"ok": False, "error": "not found"})
            return
        try:
            length = min(int(self.headers.get("Content-Length", "0")), 64 * 1024)
            if length:
                json.loads(self.rfile.read(length).decode("utf-8"))
            self.send_json(200, self.server.manager.ensure())
        except (AuthenticationError, requests.RequestException, websocket.WebSocketException) as error:
            LOG.warning("session ensure failed: %s", error)
            self.send_json(
                409,
                {"ok": False, "authenticated": False, "error": str(error)},
            )
        except (ValueError, json.JSONDecodeError):
            self.send_json(400, {"ok": False, "error": "invalid request"})


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--listen", default="127.0.0.1:9881")
    parser.add_argument(
        "--cdp-url", default=os.getenv("TEAMBITION_CDP_URL", "http://127.0.0.1:9222")
    )
    parser.add_argument("--once", action="store_true")
    return parser.parse_args()


def main() -> int:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    args = parse_args()
    manager = SessionManager(
        args.cdp_url,
        os.getenv("TEAMBITION_LOGIN_USERNAME", ""),
        os.getenv("TEAMBITION_LOGIN_PASSWORD", ""),
    )
    if args.once:
        print(json.dumps(manager.ensure(), ensure_ascii=False))
        return 0
    host, port_text = args.listen.rsplit(":", 1)
    if host not in {"127.0.0.1", "localhost", "::1"}:
        raise SystemExit("authentication service must listen on loopback")
    server = AuthHTTPServer((host, int(port_text)), Handler)
    server.manager = manager
    LOG.info("authentication service listening on %s", args.listen)
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
