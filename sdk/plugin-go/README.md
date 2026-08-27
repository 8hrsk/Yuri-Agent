# Yuri plugin SDK

This package is the dependency-free Go SDK for Yuri's out-of-process plugin
protocol. A plugin is a separate executable that speaks bounded JSON Lines on
stdin/stdout; stdout must contain protocol frames only.

The smallest plugin consists of a validated `Manifest`, a `ToolHandler`, and
`Server.Serve`:

```go
server, err := plugin.NewServer(manifest, plugin.ToolHandlerFunc(handle), plugin.ServerOptions{})
if err != nil {
    log.Fatal(err)
}
log.Fatal(server.Serve(context.Background(), os.Stdin, os.Stdout))
```

The host sends a handshake before health checks or tool invocations. Effective
permissions arrive in that handshake; the permission list in the manifest is
only a request and never grants access. Plugins can publish declared event
sources with `Server.EmitEvent`.

`schemas/plugin-manifest.schema.json` and `schemas/plugin-rpc.schema.json` are
the language-neutral contracts. The reference implementation in
`plugins/reference` demonstrates echo, handshake, health, tool result, error,
and graceful shutdown.
