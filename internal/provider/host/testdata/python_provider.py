#!/usr/bin/env python3

import json
import os
import sys
import threading
import time


MAX_HEADER = 8 * 1024
MAX_PAYLOAD = 4 * 1024 * 1024
WRITE_LOCK = threading.Lock()
CANCEL_EVENTS = {}


def read_frame():
    header = bytearray()
    while not header.endswith(b"\r\n\r\n"):
        value = sys.stdin.buffer.read(1)
        if not value:
            return None
        header.extend(value)
        if len(header) > MAX_HEADER:
            raise RuntimeError("header too large")
    lengths = []
    for line in bytes(header[:-4]).split(b"\r\n"):
        name, separator, value = line.partition(b":")
        if not separator:
            raise RuntimeError("malformed header")
        if name.strip().lower() == b"content-length":
            lengths.append(int(value.strip()))
    if len(lengths) != 1 or lengths[0] > MAX_PAYLOAD:
        raise RuntimeError("invalid content length")
    payload = sys.stdin.buffer.read(lengths[0])
    if len(payload) != lengths[0]:
        raise RuntimeError("truncated payload")
    return json.loads(payload.decode("utf-8"))


def write_frame(message):
    payload = json.dumps(message, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    frame = f"Content-Length: {len(payload)}\r\n\r\n".encode("ascii") + payload
    with WRITE_LOCK:
        sys.stdout.buffer.write(frame)
        sys.stdout.buffer.flush()


def result(request_id, value):
    write_frame({"jsonrpc": "2.0", "id": request_id, "result": value})


def failure(request_id, code, message):
    write_frame({
        "jsonrpc": "2.0",
        "id": request_id,
        "error": {"code": -32000, "message": message, "data": {"code": code}},
    })


def capabilities(replay_safe=False):
    return {
        "health": {"version": 1, "max_in_flight": 2},
        "capture_turn": {
            "version": 1,
            "roles": ["user", "assistant"],
            "max_request_bytes": 3800 * 1024,
            "max_in_flight": 2,
            "replay_safe": replay_safe,
            "ordering": "turn",
        },
        "recall": {
            "version": 1,
            "scopes": ["user"],
            "max_request_bytes": 3800 * 1024,
            "max_result_items": 5,
            "max_in_flight": 2,
        },
    }


def handle_business(message, mode):
    request_id = message["id"]
    method = message["method"]
    params = message["params"]
    if method == "health":
        result(request_id, {"process": "ready", "config": "valid", "backend": "available"})
        return
    if method == "capture_turn":
        result(request_id, {
            "receipt_id": request_id,
            "state": "accepted",
            "provider_refs": ["python-ref"],
            "replay_safe": False,
        })
        return
    if method == "recall":
        if mode == "slow-recall":
            sys.stderr.write("PYTHON_RECALL_STARTED\n")
            sys.stderr.flush()
            event = CANCEL_EVENTS.setdefault(request_id, threading.Event())
            deadline_ms = params["meta"]["deadline_unix_ms"]
            timeout = max(0.0, deadline_ms / 1000.0 - time.time())
            event.wait(timeout)
            failure(request_id, "deadline_exceeded", "provider request deadline exceeded")
            return
        result(request_id, {
            "items": [{
                "id": "python-memory",
                "kind": "instruction",
                "scope": "user",
                "text": "Python Provider memory",
                "source": "python:l1/python-memory",
            }],
            "partial": False,
        })
        return
    failure(request_id, "unsupported_capability", "unsupported method")


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "normal"
    if mode == "garbage":
        sys.stdout.buffer.write(b"python provider starting\n")
        sys.stdout.buffer.flush()
        time.sleep(60)
        return
    if mode == "oversized":
        sys.stdout.buffer.write(b"Content-Length: 4194305\r\n\r\n")
        sys.stdout.buffer.flush()
        time.sleep(60)
        return

    initialized = False
    while True:
        message = read_frame()
        if message is None:
            return
        method = message.get("method")
        if method == "$/cancelRequest":
            canceled_id = message["params"]["id"]
            CANCEL_EVENTS.setdefault(canceled_id, threading.Event()).set()
            continue
        request_id = message.get("id")
        if not initialized:
            if method != "initialize":
                failure(request_id, "protocol_error", "initialize required")
                return
            secret = message["params"]["secrets"]["token"]
            provider_id = "dev.mlink.fixture"
            if mode == "wrong-provider":
                provider_id = "dev.mlink.wrong"
            elif mode == "echo-initialize-secret":
                provider_id = secret
            result(request_id, {
                "provider_id": provider_id,
                "provider_version": "0.1.0",
                "protocol_version": "1.0",
                "capabilities": capabilities(mode == "capability-escalation"),
            })
            initialized = True
            if mode == "split-stderr-secret":
                midpoint = len(secret) // 2
                sys.stderr.write(secret[:midpoint])
                sys.stderr.flush()
                time.sleep(0.01)
                sys.stderr.write(secret[midpoint:] + "\n")
                sys.stderr.flush()
            continue
        if method == "shutdown":
            result(request_id, {})
            return
        worker = threading.Thread(target=handle_business, args=(message, mode), daemon=True)
        worker.start()


if __name__ == "__main__":
    main()
