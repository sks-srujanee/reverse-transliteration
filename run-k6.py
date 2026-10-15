#!/usr/bin/env python3
"""Run k6 against a private local instance; leave existing API processes alone."""
import json
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

directory = Path(__file__).resolve().parent
output = directory / 'k6-results'
output.mkdir(exist_ok=True)
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    port = sock.getsockname()[1]
key = secrets.token_hex(32)
base = f'http://127.0.0.1:{port}'
environment = {**os.environ, 'HOST': '127.0.0.1', 'PORT': str(port), 'API_KEY': key, 'WORKERS': '1', 'RATE_LIMIT_RPS': '100', 'BINARY_PATH': str(directory / 'bin/translit-reverse'), 'K6_NO_USAGE_REPORT': 'true'}
stop = threading.Event()
memory = {'peak_api_rss_mib': 0, 'peak_process_tree_rss_mib': 0, 'samples': 0}
process = subprocess.Popen([str(directory / 'bin/reverse-api-k6')], cwd=directory, env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

def monitor():
    while not stop.is_set():
        rows = subprocess.check_output(['ps', '-axo', 'pid=,ppid=,rss='], text=True)
        entries = [tuple(map(int, line.split())) for line in rows.splitlines() if len(line.split()) == 3]
        descendants = {process.pid}
        while True:
            additions = {pid for pid, parent, _ in entries if parent in descendants} - descendants
            if not additions:
                break
            descendants.update(additions)
        api = sum(rss for pid, _, rss in entries if pid == process.pid) / 1024
        total = sum(rss for pid, _, rss in entries if pid in descendants) / 1024
        memory['peak_api_rss_mib'] = max(memory['peak_api_rss_mib'], round(api, 1))
        memory['peak_process_tree_rss_mib'] = max(memory['peak_process_tree_rss_mib'], round(total, 1))
        memory['samples'] += 1
        stop.wait(0.25)

def conversion():
    request = urllib.request.Request(base + '/v1/reverse', data=json.dumps({'text': 'ગુજરાત मेरा नाम राहुल है'}).encode(), headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
    started = time.monotonic()
    with urllib.request.urlopen(request, timeout=20) as response:
        assert response.status == 200
        assert json.load(response)['result'] == 'gujarat mera naam rahul hai'
    return round((time.monotonic() - started) * 1000, 1)

thread = threading.Thread(target=monitor, daemon=True)
try:
    for _ in range(100):
        if process.poll() is not None:
            raise RuntimeError('Isolated service exited during startup')
        try:
            with urllib.request.urlopen(base + '/health', timeout=1) as response:
                if response.status == 200:
                    break
        except (urllib.error.URLError, TimeoutError):
            time.sleep(0.05)
    else:
        raise RuntimeError('Service did not become ready')
    warmup = conversion()
    thread.start()
    phases = []
    for name, rate, duration in [('baseline', '0', '0s'), ('overload', '10', '30s'), ('burst', '50', '10s')]:
        print(f'Running k6 {name}: rate={rate}, duration={duration}', flush=True)
        env = {**environment, 'BASE_URL': base, 'PHASE': name, 'RATE': rate, 'DURATION': duration, 'SUMMARY_PATH': str(output / f'{name}.json')}
        started = time.monotonic()
        result = subprocess.run([str(directory / 'bin/k6'), 'run', '--quiet', '--no-color', str(directory / 'loadtest.k6.js')], cwd=directory, env=env, capture_output=True, text=True, timeout=150)
        elapsed = time.monotonic() - started
        (output / f'{name}.log').write_text(result.stdout + result.stderr)
        phases.append({'name': name, 'offered_rps': int(rate) if name != 'baseline' else None, 'elapsed_wall_seconds': round(elapsed, 3), 'k6_exit_code': result.returncode})
        print(f'{name}: k6 exit={result.returncode}, elapsed={elapsed:.2f}s', flush=True)
        if result.returncode != 0:
            raise RuntimeError(f'k6 {name} failed; inspect k6-results/{name}.log')
    recovery = conversion()
    stop.set()
    thread.join(timeout=2)
    report = {'timestamp_utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'platform': platform.system(), 'architecture': platform.machine(), 'logical_cpus': os.cpu_count(), 'k6_version': subprocess.check_output([str(directory / 'bin/k6'), 'version'], text=True).strip(), 'workers': 1, 'rate_limit_rps': 100, 'cache': False, 'cross_request_batching': False, 'warmup_ms': warmup, 'recovery_ms': recovery, 'memory_sample_interval_seconds': 0.25, 'memory': memory, 'phases': phases}
    (output / 'run.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report), flush=True)
finally:
    stop.set()
    if thread.is_alive():
        thread.join(timeout=2)
    process.terminate()
    try:
        process.wait(timeout=22)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()
