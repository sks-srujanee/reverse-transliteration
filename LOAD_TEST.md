# First Go load-test baseline

Measured locally on 15 October 2026 IST (14 October UTC), using an Apple M4, 10 logical CPUs, 16 GiB RAM, macOS arm64, and Go 1.26.5. This was a native run without Docker CPU/memory limits. A preliminary run overlapped a cross-compilation; it was discarded. The figures below are from the subsequent run after compilation finished.

## Setup

- One binary worker; each accepted request starts a new reverse-transliteration process.
- No response caching or batching across requests. The binary's extracted resource cache already existed; this does not keep models in memory across processes.
- One representative multilingual title: `ગુજરાત मेरा नाम राहुल है தமிழ் తెలుగు বাংলা ଓଡ଼ିଆ`.
- Every HTTP 200 response was checked against `gujarat mera naam rahul hai tamil telugu bangla oriya`.
- One warmup request, then 12 sequential requests, then 150 requests offered at 10/second over 15 seconds, then a recovery request. The warmup is excluded from phase throughput.
- Memory sampled with `ps` every 250 ms. Process-tree RSS is the sum of the API and its descendant RSS; shared pages may be counted more than once, and short peaks can be missed.

## Results

| Phase | Requests | HTTP 200 | HTTP 503 | Successful requests/sec | Successful p50 | Successful p95 |
|---|---:|---:|---:|---:|---:|---:|
| Sequential baseline | 12 | 12 | 0 | 1.303 | 754.3 ms | 839.7 ms |
| Offered 10 requests/sec | 150 | 18 | 132 | 1.200 | 726.2 ms | 1331.6 ms |
| Recovery | 1 | 1 | 0 | — | 681.8 ms | — |

Overload rejection p95: **1.8 ms**. These fast rejections are not successful conversion throughput. The overload success rate was **12%**; 88% of offered requests were deliberately rejected because the worker was occupied. There were no transport errors, timeouts, HTTP 500 responses, or incorrect successful results in this run.

Peak sampled API RSS: **13.4 MiB**. Peak sampled API plus child-process RSS: **498.1 MiB**. The service was terminated by the harness after testing; no API/binary processes were left running.

## Interpretation

The Go wrapper is small relative to the conversion process. With this CLI integration, one worker completes roughly one conversion per second on this machine. The bounded worker protects memory under overload; it does not satisfy a requirement to successfully process 10 or more unique titles per second.

The next optimization to evaluate is batching several pending titles into one invocation with a strictly bounded queue and per-request deadlines, followed by the same correctness and load tests. A persistent model engine would remove repeated model loading more directly, but the supplied distribution has no documented server mode or source. Caching repeated inputs can help some workloads; measure unique-input throughput separately so cache hits do not hide this limitation. Increasing worker count costs roughly another model-sized process per concurrent conversion and needs its own CPU/memory measurements.

This short, single-input, local run is a baseline, not a sustained production capacity claim or a full security audit. Next gates are representative input/soak testing on dev hardware, container validation, and agreement on the actual peak successful request rate and latency target. No dev/production deployment or Srujanee backend changes were made.

## Reproduce

```sh
python3 install-binary.py ~/projects/Binaries/gotrans_reverse_dist.zip
TEST_REAL_BINARY="$PWD/bin/translit-reverse" go test -race ./...
go vet ./...
go build -o bin/reverse-api .
python3 loadtest.py
```

Raw results: [load-results.json](load-results.json). Repeat measurements will overwrite that file. The harness uses a temporary key, binds only to loopback, and starts/stops its own API instance. Load tests should target an isolated local/dev instance.
