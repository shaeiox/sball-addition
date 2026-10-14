#!/usr/bin/env python3
"""Fake PVM/sballd XML-RPC socket used by the sball-addition integration tests.

The wire protocol is the one implemented by pvm/pvm-wrapper and sballd
(a 15 byte zero padded length prefix followed by the XML body), so the tests
exercise the real client code instead of a mock.

State control
-------------
* --master N            master node id reported by command 15
* --node id:name:node-state:sball-state:ip:port   (repeatable)
* --quorate-after SECS  the local node reports "not-quorate" for SECS seconds
* --control FILE        when FILE exists and is not empty, its content
                        ("quorate" or "not-quorate") decides the local state,
                        which lets a test flip the state deterministically.

The server prints "LISTENING <port>" on stdout as soon as it is ready.
"""

import argparse
import os
import signal
import socket
import sys
import threading
import time

REQUEST_LENGTH = 15


def parse_args():
    p = argparse.ArgumentParser(description="fake PVM socket")
    p.add_argument("--port", type=int, default=0, help="0 = pick a free port")
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--hostname", default=socket.gethostname().split(".")[0])
    p.add_argument("--master", default="1")
    p.add_argument("--node", action="append", default=[],
                   help="id:name:node-state:sball-state:ip:port")
    p.add_argument("--quorate-after", type=float, default=0.0)
    p.add_argument("--control", default="")
    p.add_argument("--quiet", action="store_true")
    return p.parse_args()


class Node:
    def __init__(self, spec):
        parts = spec.split(":")
        if len(parts) != 6:
            raise SystemExit("--node needs id:name:node-state:sball-state:ip:port")
        self.id, self.name, self.node_state, self.pvm_state, self.ip, self.port = parts


class FakePVM:
    def __init__(self, args):
        self.args = args
        self.started = time.time()
        self.lock = threading.Lock()
        self.servers = []
        self.nodes = [Node(spec) for spec in args.node] or [
            Node("1:%s:up:quorate:127.0.0.1:22" % args.hostname)
        ]
        if not any(n.name == args.hostname for n in self.nodes):
            self.nodes.insert(0, Node("1:%s:up:quorate:127.0.0.1:22" % args.hostname))

    # ---------------------------------------------------------------- state --
    def local_state(self):
        control = self.args.control
        if control and os.path.exists(control):
            try:
                with open(control) as fh:
                    value = fh.read().strip()
            except OSError:
                value = ""
            if value:
                return value
        if self.args.quorate_after > 0 and time.time() - self.started < self.args.quorate_after:
            return "not-quorate"
        return "quorate"

    # ------------------------------------------------------------ protocol ---
    def response(self, request):
        if "<module_id>3</module_id>" in request:
            return "<status>success</status><description>token-1</description>"
        if "<command_id>15</command_id>" in request:
            return ("<status>success</status><description><master_node>%s</master_node>"
                    "</description>" % self.args.master)

        state = self.local_state()
        with self.lock:
            nodes = list(self.nodes)
        body = ["<status>success</status><description><nodes>"]
        for node in nodes:
            pvm_state = node.pvm_state
            if node.name == self.args.hostname and pvm_state == "quorate":
                pvm_state = state
            body.append(
                "<node><id>%s</id><name>%s</name><node-state>%s</node-state>"
                "<sball-state>%s</sball-state><ip>%s</ip><port>%s</port></node>"
                % (node.id, node.name, node.node_state, pvm_state, node.ip, node.port)
            )
        body.append("</nodes></description>")
        return "".join(body)

    def handle(self, conn):
        try:
            conn.settimeout(10)
            head = self.recv_exact(conn, REQUEST_LENGTH)
            if not head:
                return
            try:
                length = int(head.decode().strip())
            except ValueError:
                return
            body = self.recv_exact(conn, length)
            if body is None:
                return
            payload = self.response(body.decode("utf-8", "replace"))
            framed = ("%0*d" % (REQUEST_LENGTH, len(payload))).encode() + payload.encode()
            conn.sendall(framed)
        except OSError:
            pass
        finally:
            try:
                conn.close()
            except OSError:
                pass

    @staticmethod
    def recv_exact(conn, n):
        buf = b""
        while len(buf) < n:
            chunk = conn.recv(n - len(buf))
            if not chunk:
                return None if buf else None
            buf += chunk
        return buf

    def serve(self):
        srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        srv.bind((self.args.host, self.args.port))
        srv.listen(16)
        self.servers.append(srv)
        print("LISTENING %d" % srv.getsockname()[1], flush=True)
        while True:
            try:
                conn, _ = srv.accept()
            except OSError:
                return
            threading.Thread(target=self.handle, args=(conn,), daemon=True).start()


def main():
    args = parse_args()
    server = FakePVM(args)

    def stop(*_):
        for srv in server.servers:
            try:
                srv.close()
            except OSError:
                pass
        sys.exit(0)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    if not args.quiet:
        print("fake pvm: hostname=%s master=%s quorate-after=%s"
              % (args.hostname, args.master, args.quorate_after), file=sys.stderr)
    server.serve()


if __name__ == "__main__":
    main()
