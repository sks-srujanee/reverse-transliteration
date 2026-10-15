# Reverse transliteration API

Independent Go service using only the standard library, wrapping the supplied `gotrans_reverse_dist.zip` executable. It converts Indic text to Latin for publishing slugs. It returns transliterated text; the caller still applies its existing slug normalization and uniqueness rules. No Srujanee backend integration is included.

## Run locally

From this directory, with Go 1.26+ and Python 3 installed:

```sh
python3 install-binary.py ~/projects/Binaries/gotrans_reverse_dist.zip
export API_KEY="$(openssl rand -hex 32)"
TEST_REAL_BINARY="$PWD/bin/translit-reverse" go test -race ./...
go vet ./...
go build -o bin/reverse-api .
./bin/reverse-api
```

The service binds to `127.0.0.1:4100`. In another terminal, with the same `API_KEY`:

```sh
curl http://127.0.0.1:4100/health
curl --fail-with-body http://127.0.0.1:4100/v1/reverse \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  --data '{"text":"ગુજરાત मेरा नाम राहुल है"}'
```

Response: `{"result":"gujarat mera naam rahul hai"}`.

`API_KEY` is required and must be at least 32 bytes. `HOST`, `PORT`, and `BINARY_PATH` optionally override the bind address, port, and executable location. `WORKERS` defaults to 1 (maximum 8); do not increase it without measuring memory and CPU. `RATE_LIMIT_RPS` defaults to 100 per instance, including failed authentication attempts, with a burst of the same size. The binary is installed locally and ignored by Git. The installer prints its checksum and never extracts archive paths directly.

## Container setup for a later dev deployment

These commands build/start only the machine on which they are run. They do not SSH or deploy remotely:

```sh
python3 install-binary.py ~/projects/Binaries/gotrans_reverse_dist.zip --target linux-amd64
(umask 077; printf 'API_KEY=%s\n' "$(openssl rand -hex 32)" > .env)
docker compose --env-file .env -f compose.yml build
docker compose --env-file .env -f compose.yml up -d
docker compose -f compose.yml logs --tail 100
```

The image targets Linux amd64, runs as a non-root user, has a read-only filesystem, and stores unpacked language resources in the `reverse-cache` volume. The host port is loopback-only. First execution unpacks embedded resources; allow for that cold start. Each container is limited to one CPU and 768 MiB RAM. Stop with `docker compose --env-file .env -f compose.yml down`; omit `-v` to retain the resource cache.

For backend integration later, call from a private network using a server-side key. When the backend is another container, put both services on the same private Docker network and address this service by its service name; the backend container's `localhost` refers to itself. Do not expose this port publicly or put the key in frontend code. Agree the dev configuration and validate it before any production release.

## Contract and limits

- `GET /health`: unauthenticated, 200 when the binary exists and is executable; 503 otherwise. This checks availability, not conversion correctness. Run the real-binary tests as the deployment gate.
- `POST /v1/reverse`: JSON `{ "text": "…" }`, Bearer authentication, returns `{ "result": "…" }`. Whitespace is preserved. English text is accepted. No forward conversion or language parameter.
- Maximum body: 16 KiB; text: 4096 Unicode code points and 128 whitespace-separated words. Blank text, invalid UTF-8, control characters other than tab/newline, and words ending in `.csv` are rejected. The CLI interprets CSV arguments as files, so this text API disallows that mode.
- Errors: 400 invalid input/JSON, 401 missing/incorrect key, 404 unknown endpoint, 405 wrong method, 413 oversized body, 415 wrong content type, 429 rate limit, 502 failed/malformed binary output, 503 unavailable/busy, 504 conversion timeout. Both 429 and 503 include `Retry-After: 1`.
- One conversion process at a time by default; concurrent requests receive 503 rather than accumulating a queue. At most 64 authenticated requests can be parsing/converting simultaneously. A process is forcibly terminated after 15 seconds or client cancellation; stdout and stderr are each capped at 64 KiB. Requests use literal argument arrays, without a shell. The service does not log submitted text or keys.
- HTTP header/body/write/idle deadlines are 5/10/20/30 seconds. Authentication compares fixed-length key hashes in constant time. Responses disable caching and MIME sniffing. Shutdown allows up to 20 seconds for in-flight requests.

## Repeat the local load test

```sh
go build -o bin/reverse-api .
python3 loadtest.py
```

The harness starts its own loopback-only service on an ephemeral port with a temporary key, one worker, and no caching or cross-request batching. It validates every successful multilingual conversion, runs 12 sequential requests followed by 10 requests/second for 15 seconds, checks recovery, samples API plus child-process RSS every 250 ms, writes `load-results.json`, and terminates the service. Configure the offered load with `--baseline-requests`, `--overload-rps`, and `--overload-seconds`.

Successful latency and throughput are reported separately from fast 503/429 rejections. These are short local measurements, not production capacity guarantees. Run sustained representative load in dev on the intended hardware/container limits before promotion. See `LOAD_TEST.md` for the first measured results.

The wrapper starts the CLI for each request. Local measurements show roughly 1–2 seconds per conversion and about 470 MiB of process memory; actual VM performance must be measured in dev. A persistent resource cache saves extraction, not model loading. This is appropriate for publishing requests at modest volume, not per-keystroke typing. A warm persistent engine would require a server/library build of the binary rather than increasing concurrency on a small VM.

Only local implementation/testing is in scope. Backend wiring, dev deployment, and production deployment are separate steps.

## k6 load test

With k6 available at `bin/k6`:

```sh
go build -o bin/reverse-api-k6 .
python3 run-k6.py
```

The runner starts a separate loopback instance on an ephemeral port, with a temporary key and one conversion worker, and leaves existing API processes alone. It runs 15 sequential requests, 10 requests/second for 30 seconds, and 50 requests/second for 10 seconds, followed by a recovery check. It shuts down its own service afterward. No Grafana Cloud account is used; k6 usage reporting is disabled.

The script validates every successful multilingual conversion. Separate custom metrics count conversions, busy responses, rate-limit responses, correctness failures, successful latency, and rejection latency. Overload tests allow 503/429 responses as controlled rejection; a passing stability threshold does **not** mean the service completed all offered requests. Zero unexpected statuses, incorrect conversions, or load-generator dropped iterations are required.

Raw phase summaries and logs are saved under `k6-results/`; memory figures use sampled API/child-process RSS. See [K6_LOAD_TEST.md](K6_LOAD_TEST.md) for the measured results and limitations. Repeating the test overwrites these reports.

The local tool was downloaded from the official [Grafana k6 v2.3.0 release](https://github.com/grafana/k6/releases/tag/v2.3.0), and its archive SHA256 was checked against that release's checksum file. k6 binaries are ignored by Git along with the service executables.
