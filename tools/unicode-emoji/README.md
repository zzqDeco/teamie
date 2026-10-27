# Pinned reaction emoji data

Chat accepts exactly the 3944 `fully-qualified` sequences in Unicode Emoji 17.0.
`minimally-qualified`, `unqualified` and `component` entries are excluded.

- Source: https://www.unicode.org/Public/17.0.0/emoji/emoji-test.txt
- SHA-256: `1d8a944f88d7952f7ef7c5167fef3c67995bcae24543949710231b03a201acda`
- Specification: https://www.unicode.org/reports/tr51/tr51-29.html
- License: `data/LICENSE.txt`; the source data also retains its copyright header.

From the repository root:

```sh
go run tools/unicode-emoji/generate.go
go run tools/unicode-emoji/generate.go -check
```

Both commands, tests and the production validator work offline. Updating the
Unicode version requires explicitly reviewing and replacing the vendored data,
its checksum, the generator's version/count checks and the generated allowlist.
