#!/usr/bin/env python3
"""Verify the candidate Nginx route on an isolated loopback instance.

No live configuration, TLS key, process, manuscript or account is used.
"""
import base64
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import threading
import time


class Fixture(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):
        pass

    def do_GET(self):
        upgrade = self.headers.get("Upgrade", "").lower()
        if upgrade == "websocket":
            assert self.headers.get("Connection", "").lower() == "upgrade"
            assert self.headers.get("Authorization") == "Bearer fixture"
            assert self.headers.get("Sec-WebSocket-Protocol") == "atoman"
            key = self.headers["Sec-WebSocket-Key"]
            accept = base64.b64encode(hashlib.sha1(
                (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()
            ).digest()).decode()
            self.send_response(101)
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
            self.send_header("Sec-WebSocket-Accept", accept)
            self.send_header("Sec-WebSocket-Protocol", "atoman")
            self.end_headers()
            # A real server WebSocket text frame across the upgraded proxy.
            self.wfile.write(b"\x81\x02ok")
            self.wfile.flush()
            self.close_connection = True
        else:
            payload = json.dumps({"path": self.path, "upgrade": upgrade}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)


def available_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    upstream.daemon_threads = True
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="papers-proxy-test-") as directory:
            root = Path(directory)
            root.chmod(0o755)
            port = available_port()
            source = (Path(__file__).resolve().parents[1] / "nginx/api.atoman.org.conf").read_text()
            parts = source.split("server {", 2)
            assert len(parts) == 3
            candidate = parts[0] + "server {" + parts[2]
            candidate = candidate.replace("listen 443 ssl;", f"listen 127.0.0.1:{port};")
            candidate = re.sub(r"^\s*ssl_(certificate_key|certificate|protocols)\s+[^;]+;\s*$", "", candidate, flags=re.M)
            candidate = candidate.replace("127.0.0.1:8080", f"127.0.0.1:{upstream.server_port}")
            for name in ["client", "proxy", "fastcgi", "uwsgi", "scgi"]:
                (root / name).mkdir()
            config = root / "nginx.conf"
            config.write_text(
                f"worker_processes 1; pid {root}/nginx.pid; error_log {root}/error.log;\n"
                "events { worker_connections 64; }\nhttp { access_log off;\n"
                + "".join(f"{name}_temp_path {root}/{folder};\n" for name, folder in [
                    ("client_body", "client"), ("proxy", "proxy"), ("fastcgi", "fastcgi"),
                    ("uwsgi", "uwsgi"), ("scgi", "scgi")])
                + candidate + "\n}\n"
            )
            cmd = ["nginx", "-p", str(root), "-c", str(config)]
            subprocess.run(cmd + ["-t"], check=True)
            proc = subprocess.Popen(cmd + ["-g", "daemon off;"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                for _ in range(50):
                    assert proc.poll() is None
                    try:
                        with socket.create_connection(("127.0.0.1", port), timeout=.2):
                            break
                    except OSError:
                        time.sleep(.1)
                else:
                    raise AssertionError("fixture Nginx not listening")
                project = "550e8400-e29b-41d4-a716-446655440001"
                file = "550e8400-e29b-41d4-a716-446655440002"
                base = f"/api/v1/papers/projects/{project}/files/{file}/collaboration"
                headers = {
                    "Host": "api.atoman.org", "Connection": "Upgrade", "Upgrade": "websocket",
                    "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": base64.b64encode(os.urandom(16)).decode(),
                    "Sec-WebSocket-Protocol": "atoman", "Authorization": "Bearer fixture",
                }
                for path in (base + "/ws?presence=1", "/api/v1/collab/ws/fixture"):
                    with socket.create_connection(("127.0.0.1", port), timeout=4) as sock:
                        sock.sendall((f"GET {path} HTTP/1.1\r\n" + "".join(f"{k}: {v}\r\n" for k, v in headers.items()) + "\r\n").encode())
                        response = b""
                        while b"\r\n\r\n" not in response:
                            chunk = sock.recv(4096)
                            assert chunk
                            response += chunk
                        head, data = response.split(b"\r\n\r\n", 1)
                        assert head.startswith(b"HTTP/1.1 101 "), head
                        assert b"sec-websocket-protocol: atoman" in head.lower()
                        while len(data) < 4:
                            chunk = sock.recv(4 - len(data))
                            assert chunk
                            data += chunk
                        assert data[:4] == b"\x81\x02ok"
                # Ordinary API routes (including the collaboration capability)
                # must not forward a WebSocket upgrade to the backend.
                for path in (base, base + "/other", "/api/v1/papers/projects"):
                    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=4)
                    try:
                        connection.request("GET", path, headers=headers)
                        response = connection.getresponse()
                        assert response.status == 200
                        value = json.loads(response.read())
                        assert value == {"path": path, "upgrade": ""}
                    finally:
                        connection.close()
                print("PAPERS_PROXY_UPGRADE_AND_ORDINARY_API_ISOLATION_VERIFIED")
            finally:
                if proc.poll() is None:
                    proc.terminate()
                    try:
                        proc.wait(timeout=8)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait()
    finally:
        upstream.shutdown()
        upstream.server_close()
        thread.join(timeout=2)


if __name__ == "__main__":
    main()
