#!/usr/bin/env python3
"""Exercise Caddy's production global TLS options with local-only IP HTTPS."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def build_route_probe_block(caddyfile_text: str, port: int = 9081) -> str:
    site_match = re.search(
        r"^\{\$SITE_ADDRESS\}\s*\{(.*?)^\}",
        caddyfile_text,
        flags=re.MULTILINE | re.DOTALL,
    )
    assert site_match, "production Caddyfile must define {$SITE_ADDRESS} block"
    body = re.sub(r"^\s*import\s+/etc/caddy/issuer\.caddy\s*$", "", site_match.group(1), flags=re.MULTILINE)
    body = re.sub(r"\breverse_proxy\s+([^\s]+)", r'respond "\1" 200', body)
    return f"http://127.0.0.1:{port} {{\n{body}\n}}\n"


def verify_live_caddy_routes(port: int = 9081) -> int:
    spec = importlib.util.spec_from_file_location(
        "test_caddy_routes", Path(__file__).with_name("test_caddy_routes.py")
    )
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    pattern_re = re.compile(r'^\s*Pattern[A-Za-z0-9_]+\s*=\s*"([A-Z]+\s+[^"]+)"', re.MULTILINE)
    checked = 0
    for rel_router, expected_upstream in mod.EXPECTED_ROUTER_UPSTREAMS.items():
        router_text = (mod.REPO_ROOT / rel_router).read_text()
        for mux_pat in pattern_re.findall(router_text):
            method, _ = mux_pat.split(" ", 1)
            sample_path = mod.concrete_sample_path(mux_pat)
            req = urllib.request.Request(f"http://127.0.0.1:{port}{sample_path}", method=method)
            if sample_path.startswith("/internal/"):
                try:
                    opener.open(req, timeout=3)
                except urllib.error.HTTPError as err:
                    assert err.code == 404, f"{mux_pat} returned {err.code}, expected 404"
                else:
                    raise AssertionError(f"internal route {mux_pat} was not blocked with 404")
            else:
                with opener.open(req, timeout=3) as resp:
                    actual = resp.read().decode("utf-8")
                    assert resp.status == 200 and actual == expected_upstream, (
                        f"live Caddy routed {mux_pat} ({sample_path}) to {actual!r} ({resp.status}), "
                        f"expected {expected_upstream!r}"
                    )
            checked += 1
    assert checked >= 75, f"expected >=75 contract routes, checked {checked}"
    return checked


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--caddy", required=True)
    args = parser.parse_args()
    binary = str(Path(args.caddy).resolve())
    public_ip = "192.0.2.1"  # Documentation-only address; never contacted.
    source = Path(__file__).resolve().parent.parent / "deploy" / "Caddyfile"
    source_text = source.read_text()
    global_block = re.match(r"\A(\{.*?^\})", source_text, re.MULTILINE | re.DOTALL)
    assert global_block, "production Caddyfile must start with a global block"
    with tempfile.TemporaryDirectory(prefix="teamie-ip-tls-") as directory:
        root = Path(directory)
        cert, key = root / "ip.crt", root / "ip.key"
        subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
                        "-noenc", "-days", "1", "-subj", "/CN=Teamie-local-preflight",
                        "-addext", "subjectAltName=IP:" + public_ip, "-keyout", str(key), "-out", str(cert)],
                       check=True, capture_output=True)
        config = root / "Caddyfile"
        config.write_text(global_block.group(1)[:-1] + "\n debug\n http_port 9080\n https_port 9443\n"
                          " default_bind 127.0.0.1\n skip_install_trust\n}\nhttp:// {\n}\n"
                          "https://" + public_ip + ":9443 {\n tls " + str(cert) + " " + str(key) +
                          '\n respond "ip-tls-preflight-ok" 200\n}\n' +
                          build_route_probe_block(source_text, 9081))
        env = {**os.environ, "TLS_SERVER_NAME": public_ip, "XDG_DATA_HOME": directory + "/data",
               "XDG_CONFIG_HOME": directory + "/config"}
        adapted = json.loads(subprocess.check_output([binary, "adapt", "--config", str(config), "--adapter", "caddyfile"],
                                                    env=env, stderr=subprocess.DEVNULL))
        assert adapted.get("admin", {}).get("disabled") is True, "Caddy admin API must stay disabled"
        # Load the certificate without manual-cert selection tags, approximating managed-cache lookup.
        for item in adapted["apps"]["tls"]["certificates"]["load_files"]:
            item.pop("tags", None)
        for server in adapted["apps"]["http"]["servers"].values():
            if server.get("tls_connection_policies"):
                policy = server["tls_connection_policies"][-1]
                policy.pop("match", None)
                policy.pop("certificate_selection", None)
                server["tls_connection_policies"] = [policy]
        json_config = root / "caddy.json"
        json_config.write_text(json.dumps(adapted))
        log_path = root / "caddy.log"
        with log_path.open("w") as log:
            process = subprocess.Popen([binary, "run", "--config", str(json_config)], env=env, stdout=log, stderr=log)
            try:
                for _ in range(100):
                    if process.poll() is not None:
                        raise RuntimeError(log_path.read_text())
                    try:
                        with socket.create_connection(("127.0.0.1", 9443), timeout=0.1):
                            break
                    except OSError:
                        time.sleep(0.03)
                result = subprocess.run(["curl", "--noproxy", "*", "--cacert", str(cert), "--connect-to",
                                         public_ip + ":9443:127.0.0.1:9443", "--connect-timeout", "3", "--max-time", "5",
                                         "--silent", "--show-error", "--write-out", "\n%{http_code} %{ssl_verify_result}",
                                         "https://" + public_ip + ":9443/"], check=True, capture_output=True, text=True)
                assert result.stdout == "ip-tls-preflight-ok\n200 0", result.stdout
                redirect = subprocess.check_output(["curl", "--noproxy", "*", "--connect-to", public_ip + ":9080:127.0.0.1:9080",
                                                     "--max-time", "3", "--silent", "--show-error", "--output", os.devnull,
                                                     "--write-out", "%{http_code}", "http://" + public_ip + ":9080/"], text=True)
                assert redirect == "308", "automatic HTTP redirect must remain enabled"
                routed_count = verify_live_caddy_routes(9081)
            finally:
                process.terminate()
                process.wait(timeout=5)
        hellos = [json.loads(line)["data"]["client_hello"] for line in log_path.read_text().splitlines()
                  if json.loads(line).get("name") == "tls_get_certificate"]
        assert hellos and all(item["ServerName"] == "" and item["LocalAddr"]["IP"] == "127.0.0.1" for item in hellos), \
            "the IP handshake must omit SNI and use the different loopback listener address"
    print(f"IP HTTPS without SNI: trusted certificate verified, HTTP 200; redirect 308; admin disabled; {routed_count} routes verified.")


if __name__ == "__main__":
    main()
