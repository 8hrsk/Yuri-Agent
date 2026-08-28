# Performance baseline

Stage 6 фиксирует воспроизводимый microbenchmark для двух горячих путей: cross-session memory ranking и сборки bounded model context. Это baseline для обнаружения регрессий, а не обещание end-to-end latency внешнего LLM.

```text
make bench-baseline
```

Reference run от 2026-08-28: macOS, Apple M1, darwin/arm64, Go 1.25, `GOMAXPROCS=8`.

| Benchmark | Median time | Bytes/op | Allocs/op |
| --- | ---: | ---: | ---: |
| HybridRanker1000 | 547 µs | 1,163,745 | 1,009 |
| LexicalScoreUnicode | 5.64 µs | 1,712 | 44 |
| AssemblerBoundedContext | 216 µs | 118,853 | 474 |

Перед сравнением необходимо использовать одинаковые CPU, Go version и power mode. Существенной регрессией для review считается рост median time или allocations более чем на 20% без объяснённого изменения качества retrieval/context assembly.

Desktop cold start, voice latency и provider latency измеряются отдельно после появления стабильного release harness: microbenchmarks не включают Wails/WebKit, сеть, STT/TTS и модель.
