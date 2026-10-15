# k6 load-test results

Run completed 15 October 2026 at 04:27 UTC (09:57 IST), using k6 v2.3.0 on the local Apple M4 / macOS arm64 machine (10 logical CPUs, 16 GiB RAM). The native Go API used one binary worker, a 100 requests/second global rate limit, no response caching, and no cross-request batching. No service implementation changes were made for this test.

The runner started an isolated loopback service on an ephemeral port using a temporary API key. The existing user-started API process was left running and untouched. Only the test instance and its descendants are included in memory sampling. The runner stopped its own service after testing.

## Measured results

| Phase | Offered load | Actual requests | Successful conversions | HTTP 503 | Success rate | Successful conversions/sec | Successful p95 |
|---|---|---:|---:|---:|---:|---:|---:|
| Sequential | 1 virtual user, 15 iterations | 15 | 15 | 0 | 100% | 1.44 | 702.6 ms |
| Overload | 10 requests/sec for 30 seconds | 301 | 43 | 258 | 14.29% | 1.41 | 705.8 ms |
| Burst | 50 requests/sec for 10 seconds | 501 | 15 | 486 | 2.99% | 1.43 | 704.0 ms |

Successful p50 was 692.5 / 690.7 / 692.1 ms respectively. Rejection p95 was 0.53 ms at 10 requests/sec and 0.45 ms at 50 requests/sec. Fast rejections are reported separately and **do not count as successful conversion throughput**.

The arrival-rate executor scheduled 301 and 501 requests at the duration boundary; the table uses actual k6 counts rather than rounding to 300 and 500. Throughput is the k6 conversion counter rate over the active test run, including completion of the last request. The runner's wall clock also includes k6 process startup/teardown, so it is slightly longer.

- **817 measured requests:** 73 completed conversions, 744 controlled busy rejections.
- All 73 successful results matched the expected transliteration exactly.
- No incorrect results, transport errors, unexpected statuses, HTTP 500/502/504 responses, or k6 dropped iterations.
- No HTTP 429 responses: offered rates were below the configured 100 requests/sec rate limit. This test did not exercise the rate-limit boundary.
- Peak sampled API RSS: **17.7 MiB**.
- Peak sampled API plus binary process-tree RSS: **497.6 MiB**.
- Post-load recovery request completed correctly in **693.3 ms**; the warmup completed in 690.5 ms. These two requests are excluded from the table.

RSS was sampled every 250 ms using `ps`. It can miss brief peaks, and summed process RSS can double-count shared pages. The load generator's memory and the existing unrelated API process are excluded.

## What this proves

The one-worker service remained stable and preserved correctness while rejecting excess work. It did **not** successfully process 10 or 50 conversions per second. Successful throughput remained approximately 1.4 conversions/sec, consistent with the per-request binary startup/model-loading constraint seen in the earlier test.

All script thresholds passed: baseline conversion success was 100%; overload runs had successful conversions, no incorrect results or unexpected statuses, and no load-generator dropped iterations. These are stability/correctness gates, **not** an assertion that all offered load was served. k6's built-in `http_req_failed` metric counts the intentional 503 responses as failures; they are shown explicitly in the table.

No speed optimizations were introduced for this run. Bounded batching or a persistent engine should be evaluated next and compared using unique inputs as well as repeated inputs.

## Reproduce and inspect

```sh
cd ~/projects/reverse-transliteration
go build -o bin/reverse-api-k6 .
./bin/k6 version
python3 run-k6.py
```

The local k6 binary came from the official Grafana v2.3.0 release. The macOS arm64 archive SHA256 was verified against its release checksum file: `b2417a3038edc5fe81dc178a889237724b595c5c9cfed875822008e46e862c7d`.

- Test script: [loadtest.k6.js](loadtest.k6.js).
- Isolated runner: [run-k6.py](run-k6.py).
- Configuration, memory, recovery, and version: [k6-results/run.json](k6-results/run.json).
- Raw k6 summaries: [baseline](k6-results/baseline.json), [overload](k6-results/overload.json), [burst](k6-results/burst.json). Matching `.log` files are also retained; successful quiet runs may have empty logs.
- Methodology: Grafana's [constant-arrival-rate executor](https://grafana.com/docs/k6/latest/using-k6/scenarios/executors/constant-arrival-rate/) and [custom summary](https://grafana.com/docs/k6/latest/results-output/end-of-test/custom-summary/) documentation.

Limits: this is a short native local test using one representative multilingual title, not a soak test, security audit, or production capacity guarantee. The extracted binary-resource cache already existed. No Docker resource limits, VM environment, network/TLS overhead, diverse-title workload, or multi-worker scaling were tested. No dev/production deployment or Srujanee code changes were made.
