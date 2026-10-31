import re
from pathlib import Path
import unittest


REPO_ROOT = Path(__file__).resolve().parents[1]
CADDYFILE = REPO_ROOT / "deploy" / "Caddyfile"

EXPECTED_ROUTER_UPSTREAMS = {
    "contracts/gen/im/hertz_gen/im/identity/router.go": "identity:8081",
    "contracts/gen/im/hertz_gen/im/chat/router.go": "chat:8082",
    "contracts/gen/im/hertz_gen/im/realtime/router.go": "realtime:8083",
    "contracts/gen/im/hertz_gen/im/marketplace/router.go": "marketplace:8088",
    "contracts/gen/agent/hertz_gen/agent/control/router.go": "agent-control:8084",
    "contracts/gen/agent/hertz_gen/agent/gateway/router.go": "llm-gateway:8086",
}


def caddy_glob_to_regex(pattern: str) -> re.Pattern[str]:
    """Translate Go path.Match segment glob (* matches non-'/' chars) to regex."""
    parts = ["^"]
    for ch in pattern:
        if ch == "*":
            parts.append("[^/]*")
        elif ch == "?":
            parts.append("[^/]")
        else:
            parts.append(re.escape(ch))
    parts.append("$")
    return re.compile("".join(parts))


def caddy_match_single_path(pattern: str, req_path: str) -> bool:
    """Mirror Caddy v2 modules/caddyhttp/matchers.go MatchPath semantics."""
    pat = pattern.lower()
    target = req_path.lower()
    star_count = pat.count("*")
    if star_count == 0 and "?" not in pat:
        return target == pat
    if star_count == 1 and pat.endswith("*") and "?" not in pat:
        return target.startswith(pat[:-1])
    if star_count == 1 and pat.startswith("*") and "?" not in pat:
        return target.endswith(pat[1:])
    if star_count == 2 and pat.startswith("*") and pat.endswith("*") and "?" not in pat:
        return pat[1:-1] in target
    # Multiple wildcards or middle wildcards delegate to Go's path.Match, where
    # '*' never matches across '/'.
    return bool(caddy_glob_to_regex(pat).match(target))


def parse_caddy_site_routes(caddyfile_text: str) -> list[tuple[list[str] | None, str]]:
    """Parse ordered (path_patterns_or_None, action_target) rules from {$SITE_ADDRESS}."""
    site_match = re.search(
        r"^\{\$SITE_ADDRESS\}\s*\{(.*?)^\}",
        caddyfile_text,
        flags=re.MULTILINE | re.DOTALL,
    )
    if not site_match:
        raise AssertionError("deploy/Caddyfile missing {$SITE_ADDRESS} block")
    body = site_match.group(1)

    named_matchers: dict[str, list[str]] = {}
    for m in re.finditer(r"^\s*(@[A-Za-z0-9_-]+)\s+path\s+(.+)$", body, flags=re.MULTILINE):
        named_matchers[m.group(1)] = m.group(2).split()

    rules: list[tuple[list[str] | None, str]] = []
    for m in re.finditer(
        r"^\s*handle(?:\s+([^\s{]+))?\s*\{(.*?)^\s*\}",
        body,
        flags=re.MULTILINE | re.DOTALL,
    ):
        selector = m.group(1)
        inner = m.group(2).strip()
        rp = re.search(r"reverse_proxy\s+([^\s]+)", inner)
        resp = re.search(r"respond\s+(?:\"[^\"]*\"\s+)?(\d{3})", inner)
        if rp:
            target = rp.group(1)
        elif resp:
            target = f"respond:{resp.group(1)}"
        else:
            raise AssertionError(f"unsupported handle body in Caddyfile: {inner!r}")

        if selector is None:
            rules.append((None, target))
        elif selector.startswith("@"):
            if selector not in named_matchers:
                raise AssertionError(f"unknown Caddy named matcher {selector}")
            rules.append((named_matchers[selector], target))
        else:
            rules.append(([selector], target))
    return rules


def resolve_caddy_target(rules: list[tuple[list[str] | None, str]], req_path: str) -> str:
    for patterns, target in rules:
        if patterns is None or any(caddy_match_single_path(p, req_path) for p in patterns):
            return target
    raise AssertionError(f"no Caddy handle rule matched {req_path}")


def concrete_sample_path(mux_pattern: str) -> str:
    _method, raw_path = mux_pattern.split(" ", 1)
    return re.sub(r"\{([A-Za-z0-9_]+)\}", lambda m: f"sample-{m.group(1).lower()}", raw_path)


class CaddyRouteCoverageTests(unittest.TestCase):
    def test_caddy_multi_wildcard_segment_semantics(self):
        # Single trailing '*' is a prefix match in Caddy v2:
        self.assertTrue(
            caddy_match_single_path("/api/v1/developer/*", "/api/v1/developer/apps/a1/submit")
        )
        # Two '*' wildcards use Go path.Match where '*' never crosses '/':
        self.assertTrue(
            caddy_match_single_path(
                "/api/v1/conversations/*/apps/*", "/api/v1/conversations/c1/apps/a1"
            )
        )
        self.assertFalse(
            caddy_match_single_path(
                "/api/v1/conversations/*/apps/*",
                "/api/v1/conversations/c1/apps/a1/session",
            )
        )
        self.assertTrue(
            caddy_match_single_path(
                "/api/v1/conversations/*/apps/*/session",
                "/api/v1/conversations/c1/apps/a1/session",
            )
        )

    def test_every_generated_contract_route_reaches_expected_caddy_upstream(self):
        rules = parse_caddy_site_routes(CADDYFILE.read_text())
        discovered = sorted(
            str(p.relative_to(REPO_ROOT).as_posix())
            for p in (REPO_ROOT / "contracts" / "gen").rglob("router.go")
        )
        self.assertEqual(discovered, sorted(EXPECTED_ROUTER_UPSTREAMS.keys()))

        pattern_re = re.compile(r'^\s*Pattern[A-Za-z0-9_]+\s*=\s*"([A-Z]+\s+[^"]+)"', re.MULTILINE)
        total_checked = 0
        for rel_router, expected_upstream in EXPECTED_ROUTER_UPSTREAMS.items():
            router_text = (REPO_ROOT / rel_router).read_text()
            mux_patterns = pattern_re.findall(router_text)
            self.assertTrue(mux_patterns, f"no Pattern* constants found in {rel_router}")
            for mux_pat in mux_patterns:
                sample_path = concrete_sample_path(mux_pat)
                actual = resolve_caddy_target(rules, sample_path)
                if sample_path.startswith("/internal/"):
                    self.assertEqual(
                        actual,
                        "respond:404",
                        f"internal route {mux_pat} ({sample_path}) must be blocked at edge",
                    )
                else:
                    self.assertEqual(
                        actual,
                        expected_upstream,
                        f"route {mux_pat} ({sample_path}) routed to {actual}, expected {expected_upstream}",
                    )
                total_checked += 1

        self.assertGreaterEqual(total_checked, 75)
        self.assertEqual(resolve_caddy_target(rules, "/app/admin/marketplace"), "web:80")

    def test_check_ip_tls_route_probe_block_renders_all_upstreams(self):
        import importlib.util

        spec = importlib.util.spec_from_file_location(
            "check_ip_tls", Path(__file__).with_name("check-ip-tls.py")
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        probe_block = mod.build_route_probe_block(CADDYFILE.read_text(), 9081)
        self.assertNotIn("reverse_proxy", probe_block)
        self.assertNotIn("issuer.caddy", probe_block)
        for upstream in EXPECTED_ROUTER_UPSTREAMS.values():
            self.assertIn(f'respond "{upstream}" 200', probe_block)
        self.assertIn("/api/v1/conversations/*/apps/*/session", probe_block)


if __name__ == "__main__":
    unittest.main()
