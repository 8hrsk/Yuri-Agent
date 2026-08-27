# Yuri reference plugin

`yuri-reference` is a deliberately small out-of-process plugin. It implements
the `echo` tool and demonstrates the Stage 3 stdio JSON-lines handshake,
health, tool invocation, result, error, and shutdown messages.

Build the development package (the output path matches `plugin.json`):

```bash
./plugins/reference/build.sh
```

Then run it directly from the repository root if desired:

```bash
go run ./plugins/reference
```

The process keeps stdout exclusively for protocol frames. Diagnostic output,
if added later, belongs on stderr. `plugin.json` is intentionally unsigned and
is suitable only for explicit development mode until a release asset is built,
checksummed, and signed. The build script prints the SHA-256 digest; release
packaging must record that digest and the signature in the manifest without
changing the package-relative executable path.
