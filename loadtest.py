#!/usr/bin/env python3
"""Local real-binary baseline; launches and terminates its own API process."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import math
import os
from pathlib import Path
import platform
import secrets
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output', type=Path, default=Path('load-results.json'))
parser.add_argument('--baseline-requests', type=int, default=12)
parser.add_argument('--overload-rps', type=int, default=10)
parser.add_argument('--overload-seconds', type=int, default=15)
args = parser.parse_args()
if not (1 <= args.baseline_requests <= 1000 and 1 <= args.overload_rps <= 100 and 1 <= args.overload_seconds <= 300):
    parser.error('Use baseline 1–1000, overload rate 1–100 and duration 1–300 seconds')
directory = Path(__file__).resolve().parent
key = secrets.token_hex(32)
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    port = sock.getsockname()[1]
environment = {**os.environ, 'HOST': '127.0.0.1', 'PORT': str(port), 'API_KEY': key, 'WORKERS': '1', 'RATE_LIMIT_RPS': '100', 'BINARY_PATH': str(directory / 'bin/translit-reverse')}
process = subprocess.Popen([str(directory / 'bin/reverse-api')], cwd=directory, env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
base = f'http://127.0.0.1:{port}'
payload = json.dumps({'text': 'ગુજરાત मेरा नाम राहुल है தமிழ் తెలుగు বাংলা ଓଡ଼ିଆ'}).encode()
expected = 'gujarat mera naam rahul hai tamil telugu bangla oriya'
stop_monitor = threading.Event()
memory = {'peak_api_rss_mib': 0, 'peak_process_tree_rss_mib': 0, 'samples': 0}

def monitor():
    while not stop_monitor.is_set():
        try:
            rows = subprocess.check_output(['ps', '-axo', 'pid=,ppid=,rss='], text=True)
            entries = [tuple(map(int, line.split())) for line in rows.splitlines() if len(line.split()) == 3]
            descendants = {process.pid}
            while True:
                additions = {pid for pid, parent, _ in entries if parent in descendants} - descendants
                if not additions:
                    break
                descendants.update(additions)
            tree = sum(rss for pid, _, rss in entries if pid in descendants) / 1024
            api = sum(rss for pid, _, rss in entries if pid == process.pid) / 1024
            memory['peak_api_rss_mib'] = max(memory['peak_api_rss_mib'], round(api, 1))
            memory['peak_process_tree_rss_mib'] = max(memory['peak_process_tree_rss_mib'], round(tree, 1))
            memory['samples'] += 1
        except (subprocess.SubprocessError, ValueError):
            pass
        stop_monitor.wait(0.25)

def call():
    started = time.monotonic()
    request = urllib.request.Request(base + '/v1/reverse', data=payload, headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            status = response.status
            valid = json.load(response).get('result') == expected
    except urllib.error.HTTPError as error:
        status, valid = error.code, False
        error.close()
    except Exception:
        status, valid = 'transport_error', False
    return {'status': str(status), 'valid': valid, 'latency_ms': (time.monotonic() - started) * 1000}

def summarize(name, results, elapsed):
    statuses = {}
    for result in results:
        statuses[result['status']] = statuses.get(result['status'], 0) + 1
    successful = [r['latency_ms'] for r in results if r['status'] == '200' and r['valid']]
    rejected = [r['latency_ms'] for r in results if r['status'] != '200']
    def latencies(values):
        if not values:
            return None
        ordered = sorted(values)
        return {f'p{p}_ms': round(ordered[max(0, math.ceil(len(ordered)*p/100)-1)], 1) for p in (50, 95, 99)}
    return {'phase': name, 'elapsed_seconds': round(elapsed, 3), 'requests': len(results), 'statuses': statuses, 'valid_successes': len(successful), 'incorrect_200_responses': sum(r['status'] == '200' and not r['valid'] for r in results), 'successful_rps': round(len(successful)/elapsed, 3), 'success_latency': latencies(successful), 'rejection_latency': latencies(rejected)}

thread = threading.Thread(target=monitor, daemon=True)
try:
    for _ in range(100):
        if process.poll() is not None:
            raise RuntimeError('API exited before startup; check binary and configuration')
        try:
            with urllib.request.urlopen(base + '/health', timeout=1) as response:
                if response.status == 200:
                    break
        except (urllib.error.URLError, TimeoutError):
            time.sleep(0.05)
    else:
        raise RuntimeError('Service not ready')
    warmup = call()
    if warmup['status'] != '200' or not warmup['valid']:
        raise RuntimeError(f'Warmup conversion failed: {warmup}')
    thread.start()
    started = time.monotonic()
    baseline = [call() for _ in range(args.baseline_requests)]
    phases = [summarize('sequential_baseline', baseline, time.monotonic() - started)]
    print(json.dumps(phases[-1]), flush=True)
    with ThreadPoolExecutor(max_workers=32) as executor:
        started = time.monotonic()
        futures = []
        for index in range(args.overload_rps * args.overload_seconds):
            delay = started + index / args.overload_rps - time.monotonic()
            if delay > 0:
                time.sleep(delay)
            futures.append(executor.submit(call))
        overloaded = [future.result() for future in futures]
        phases.append(summarize('open_loop_overload', overloaded, time.monotonic() - started))
    print(json.dumps(phases[-1]), flush=True)
    started = time.monotonic()
    recovery = call()
    phases.append(summarize('recovery', [recovery], time.monotonic() - started))
    report = {'timestamp_utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'platform': platform.system(), 'architecture': platform.machine(), 'logical_cpus': os.cpu_count(), 'workers': 1, 'cache': False, 'cross_request_batching': False, 'warmup': warmup, 'offered_overload_rps': args.overload_rps, 'offered_overload_seconds': args.overload_seconds, 'memory_sampling_interval_seconds': 0.25, 'memory': memory, 'phases': phases, 'limitations': ['Local native machine; not production hardware or container limits.', 'Short test with one representative multilingual title; not a sustained capacity guarantee.', 'RSS sampled at intervals, so brief memory peaks may be missed.']}
    args.output.write_text(json.dumps(report, indent=2) + '\n')
    print(f'Report: {args.output.resolve()}', flush=True)
    if any(p['incorrect_200_responses'] for p in phases) or phases[-1]['valid_successes'] != 1:
        raise RuntimeError('Conversion correctness or recovery check failed')
finally:
    stop_monitor.set()
    if thread.is_alive():
        thread.join(timeout=2)
    process.terminate()
    try:
        process.wait(timeout=22)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()
