#!/usr/bin/env python3
"""Exercise the iOS TLS delegate with a real local HTTPS server on macOS."""
import pathlib
import subprocess
import tempfile

repo = pathlib.Path(__file__).resolve().parent.parent
with tempfile.TemporaryDirectory(prefix="blooper-trust-") as temporary:
    folder = pathlib.Path(temporary)
    key, cert, der = (folder / name for name in ("key.pem", "cert.pem", "cert.der"))
    subprocess.run([
        "openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
        "-nodes", "-days", "2", "-subj", "/CN=Blooper local device",
        "-addext", "basicConstraints=critical,CA:FALSE", "-addext", "keyUsage=digitalSignature",
        "-addext", "extendedKeyUsage=serverAuth", "-keyout", str(key), "-out", str(cert),
    ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(["openssl", "x509", "-in", str(cert), "-outform", "DER", "-out", str(der)], check=True)
    program = """
const fs = require('node:fs');
const server = require('node:https').createServer({key: fs.readFileSync(process.argv[1]), cert: fs.readFileSync(process.argv[2])}, (_req, res) => res.end('ok'));
server.listen(0, '127.0.0.1', () => console.log(server.address().port));
"""
    server = subprocess.Popen(["node", "-e", program, str(key), str(cert)], stdout=subprocess.PIPE, text=True)
    try:
        port = server.stdout.readline().strip()
        binary = folder / "check"
        subprocess.run([
            "swiftc", "-module-cache-path", str(folder / "modules"), "-o", str(binary),
            str(repo / "ios/BlooperRemote/Sources/Client.swift"),
            str(repo / "ios/BlooperRemote/Sources/LocalTrust.swift"),
            str(repo / "ios/Tests/LocalTrustCheck.swift"),
        ], check=True)
        subprocess.run([str(binary), f"https://127.0.0.1:{port}", str(der)], check=True, timeout=20)
    finally:
        server.terminate()
        server.wait(timeout=5)
