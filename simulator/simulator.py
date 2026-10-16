
import argparse
import json
import random
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone


def make_log(mode, index):
    now = datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace(
        "+00:00", "Z"
    )

    endpoints = [
        "/api/users",
        "/api/products",
        "/api/orders",
        "/api/search",
        "/api/health",
    ]

    status = random.choices(
        [200, 201, 400, 404, 500, 502],
        weights=[75, 8, 5, 4, 6, 2],
    )[0]

    latency = random.uniform(10, 150)

    if mode == "spike":
        latency *= random.uniform(1.5, 4)

    if mode == "errors" and random.random() < 0.7:
        status = random.choice([500, 502, 503])
        latency = random.uniform(100, 1200)

    if mode == "flood":
        status = random.choice([200, 200, 200, 429, 503])
        latency = random.uniform(5, 80)

    # Use valid documentation-range IPv4 addresses.
    ip = f"192.0.2.{random.randint(1, 254)}"

    if mode == "flood":
        # Simulate many requests from a smaller pool of clients.
        ip = f"198.51.100.{random.randint(1, 5)}"

    return {
        "ts": now,
        "endpoint": random.choice(endpoints),
        "method": random.choice(["GET", "GET", "GET", "POST"]),
        "status": status,
        "latency_ms": round(latency, 2),
        "ip": ip,
        "payload_bytes": random.randint(100, 5000),
    }


def send_batch(url, api_key, logs):
    payload = json.dumps({"logs": logs}).encode("utf-8")

    request = urllib.request.Request(
        url,
        data=payload,
        headers={
            "Content-Type": "application/json",
            "X-API-Key": api_key,
        },
        method="POST",
    )

    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            result = json.loads(response.read().decode("utf-8"))
            return result.get("accepted", 0)

    except urllib.error.HTTPError as exc:
        message = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"Server returned HTTP {exc.code}: {message}") from exc

    except urllib.error.URLError as exc:
        raise RuntimeError(f"Cannot reach the Go server: {exc.reason}") from exc


def main():
    parser = argparse.ArgumentParser(
        description="Generate traffic for AI Traffic Command Center"
    )
    parser.add_argument("--url", default="http://localhost:8080")
    parser.add_argument("--project-id", required=True)
    parser.add_argument("--api-key", required=True)
    parser.add_argument(
        "--spike", action="store_true", help="Generate high-latency traffic"
    )
    parser.add_argument(
        "--errors", action="store_true", help="Generate frequent server errors"
    )
    parser.add_argument(
        "--flood", action="store_true", help="Generate high-volume traffic"
    )
    parser.add_argument("--duration", type=int, default=60)
    parser.add_argument("--rps", type=int, default=5)
    parser.add_argument("--batch-size", type=int, default=100)
    args = parser.parse_args()

    selected_modes = sum([args.spike, args.errors, args.flood])
    if selected_modes > 1:
        parser.error("Choose only one of --spike, --errors, or --flood.")

    if args.duration < 1 or args.rps < 1 or args.batch_size < 1:
        parser.error("duration, rps, and batch-size must be positive.")

    mode = (
        "spike" if args.spike
        else "errors" if args.errors
        else "flood" if args.flood
        else "normal"
    )

    ingest_url = (
        f"{args.url.rstrip('/')}/ingest/{args.project_id}"
    )

    print(f"Mode: {mode}")
    print(f"Ingestion URL: {ingest_url}")
    print(f"Target rate: {args.rps} records/second")
    print(f"Duration: {args.duration} seconds")
    print("Press Ctrl+C to stop.")

    total_accepted = 0
    started = time.monotonic()
    next_tick = started
    index = 0

    try:
        while time.monotonic() - started < args.duration:
            elapsed = time.monotonic() - started
            current_mode = mode

            # Make the spike temporary: first 10 seconds of the run.
            if mode == "spike" and elapsed >= 10:
                current_mode = "normal"

            batch = []
            batch_deadline = time.monotonic() + 1

            while (
                len(batch) < min(args.rps, args.batch_size, 1000)
                and time.monotonic() < batch_deadline
            ):
                batch.append(make_log(current_mode, index))
                index += 1

            accepted = send_batch(ingest_url, args.api_key, batch)
            total_accepted += accepted

            print(
                f"Accepted: {accepted} | Total: {total_accepted} "
                f"| Elapsed: {int(elapsed)}s"
            )

            next_tick += 1
            delay = next_tick - time.monotonic()
            if delay > 0:
                time.sleep(delay)
            else:
                next_tick = time.monotonic()

    except KeyboardInterrupt:
        print("\nStopped by user.")

    except RuntimeError as exc:
        print(f"\nERROR: {exc}")
        raise SystemExit(1)

    print(f"Finished. Total accepted records: {total_accepted}")


if __name__ == "__main__":
    main()
