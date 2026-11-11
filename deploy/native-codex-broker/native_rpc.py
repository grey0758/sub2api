#!/usr/bin/env python3
"""Official native RPC reader. Never reads or returns a refresh token.

Each lease comes from a fresh official app-server getAuthStatus RPC with
refreshToken=false. The existing native Codex lifecycle owns refresh. A near-
expiry token fails closed; this trial never triggers a second refresh owner.
"""
import base64
import json
import os
import select
import subprocess
import time


class BrokerError(Exception):
    pass


def native_access(binary):
    proc = subprocess.Popen(
        [binary, "app-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL, bufsize=0,
    )
    pending = bytearray()

    def rpc(mid, method, params):
        proc.stdin.write(json.dumps({"id": mid, "method": method, "params": params}).encode() + b"\n")
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if b"\n" not in pending:
                if not select.select([proc.stdout], [], [], .1)[0]:
                    continue
                chunk = os.read(proc.stdout.fileno(), 65536)
                if not chunk:
                    raise BrokerError("native_rpc_exit")
                pending.extend(chunk)
                if len(pending) > 1048576:
                    raise BrokerError("native_rpc_limit")
            if b"\n" in pending:
                line, _, tail = pending.partition(b"\n")
                pending[:] = tail
                data = json.loads(line)
                if data.get("id") == mid:
                    if "error" in data:
                        raise BrokerError("native_rpc_error")
                    return data["result"]
        raise BrokerError("native_rpc_timeout")

    try:
        version = subprocess.check_output([binary, "--version"], text=True, timeout=5).strip().split()[-1]
        if not version or len(version) > 64:
            raise BrokerError("native_version_invalid")
        initialized = rpc(1, "initialize", {"clientInfo": {"name": "codex_cli_rs", "version": version}})
        user_agent = initialized.get("userAgent", "")
        if not user_agent.startswith("codex_cli_rs/" + version + " ") or len(user_agent) > 512 or any(ord(c) < 32 or ord(c) > 126 for c in user_agent):
            raise BrokerError("native_identity_invalid")
        proc.stdin.write(b'{"method":"initialized"}\n')
        result = rpc(2, "getAuthStatus", {"includeToken": True, "refreshToken": False})
        if result.get("authMethod") != "chatgpt":
            raise BrokerError("native_chatgpt_required")
        token = result.get("authToken", "")
        parts = token.split(".")
        if len(parts) != 3:
            raise BrokerError("native_token_invalid")
        claims = json.loads(base64.urlsafe_b64decode(parts[1] + "==="))
        auth = claims.get("https://api.openai.com/auth", {})
        account = auth.get("chatgpt_account_id", "")
        user = auth.get("chatgpt_user_id", "")
        expiry = claims.get("exp", 0)
        if not account or not isinstance(expiry, int) or expiry <= time.time() + 180:
            raise BrokerError("native_token_near_expiry")
        return {"source": "codex-app-server", "access_token": token,
                "account_id": account, "user_id": user, "expires_at": expiry, "user_agent": user_agent}
    except (ValueError, KeyError, TypeError) as exc:
        raise BrokerError("native_response_invalid") from exc
    finally:
        proc.stdin.close()
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.terminate()
            try:
                proc.wait(timeout=2)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
        proc.stdout.close()
