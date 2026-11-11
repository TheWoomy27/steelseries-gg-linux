#!/usr/bin/env python3
# Tests the drag fix (dragfix.c): builds it, then, with it preloaded, sends
# wl_data_source.send events over a socket named like a Wayland one and
# checks what reaches each event's pipe.
#
# Run: python3 packaging/dragfix_test.py
import os
import select
import socket
import struct
import subprocess
import sys
import tempfile

LIB = 'libgg-dragfix.so'


def main():
    tmp = tempfile.mkdtemp(prefix='dragfix-')
    if len(tmp) > 80:  # too long for a socket path
        tmp = tempfile.mkdtemp(prefix='dragfix-', dir='/tmp')
    src = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'dragfix.c')
    lib = os.path.join(tmp, LIB)
    subprocess.run(['cc', '-shared', '-fPIC', '-O2', '-Wall', '-Werror', '-pthread', '-o', lib, src, '-ldl'], check=True)
    env = dict(os.environ, LD_PRELOAD=lib, XDG_RUNTIME_DIR=tmp, DRAGFIX_TEST='1')
    code = subprocess.run([sys.executable, '-I', os.path.abspath(__file__)], env=env).returncode
    for f in os.listdir(tmp):
        os.unlink(os.path.join(tmp, f))
    os.rmdir(tmp)
    sys.exit(code)


def event(obj, op, mime):
    s = mime.encode() + b'\0'
    body = struct.pack('<I', len(s)) + s + b'\0' * (-len(s) % 4)
    return struct.pack('<II', obj, ((8 + len(body)) << 16) | op) + body


def read_all(fd):
    out = b''
    while True:
        if not select.select([fd], [], [], 3)[0]:
            return out + b'<timeout>'
        b = os.read(fd, 4096)
        if not b:
            return out
        out += b


def tests():
    run = os.environ['XDG_RUNTIME_DIR']
    path = os.path.join(run, 'wayland-test')
    srv = socket.socket(socket.AF_UNIX)
    srv.bind(path)
    srv.listen()
    cli = socket.socket(socket.AF_UNIX)
    cli.connect(path)
    conn, _ = srv.accept()
    marker = os.path.join(run, 'gg-linux-drag-%d' % os.getpid())

    def case(name, events, chromium, expect, dragging=True):
        if dragging:
            with open(marker, 'w') as f:
                f.write('/home/u/Videos/My clip (7).mp4\n')
        elif os.path.exists(marker):
            os.unlink(marker)
        pipes = [os.pipe() for _ in events]
        msg = b''.join(event(*e) for e in events)
        fds = struct.pack('%di' % len(pipes), *[w for _, w in pipes])
        conn.sendmsg([msg], [(socket.SOL_SOCKET, socket.SCM_RIGHTS, fds)])
        for _, w in pipes:
            os.close(w)
        # As Chromium does: read the events, write what it has, close.
        _, anc, _, _ = cli.recvmsg(4096, socket.CMSG_SPACE(16 * 4))
        got_fds = struct.unpack('%di' % (len(anc[0][2]) // 4), anc[0][2])
        for fd, data in zip(got_fds, chromium):
            if data:
                os.write(fd, data)
            os.close(fd)
        got = [read_all(r) for r, _ in pipes]
        for r, _ in pipes:
            os.close(r)
        print('ok  ' if got == expect else 'FAIL', name, '' if got == expect else got)
        return got == expect

    uri = b'file:///home/u/Videos/My%20clip%20%287%29.mp4\r\n'
    results = [
        case('Chromium sends nothing: the fix sends the file',
             [(7, 1, 'text/uri-list')], [None], [uri]),
        case('Chromium sends the file: the fix stays out',
             [(7, 1, 'text/uri-list')], [b'file:///x\r\n'], [b'file:///x\r\n']),
        case('two requests: each pipe goes with its own',
             [(7, 1, 'text/uri-list'), (7, 1, 'text/x-moz-url')], [None, None], [uri, b'']),
        case('another type', [(7, 1, 'text/plain')], [None], [b'']),
        case('no drag on', [(7, 1, 'text/uri-list')], [None], [b''], dragging=False),
    ]
    if os.path.exists(marker):
        os.unlink(marker)
    sys.exit(0 if all(results) else 1)


if __name__ == '__main__':
    tests() if os.environ.get('DRAGFIX_TEST') else main()
