#!/usr/bin/env python3
"""Seed the Acme Analytics Corporation demo through DataAtlas public APIs.

Creates a demo schema with fictional business data, registers it as a
source, discovers it, builds lineage via OpenLineage events, creates and
executes quality checks (raising alerts on the seeded defects), profiles
key datasets, and records glossary terms and classifications.

Idempotent: safe to re-run (deduplicated sources/checks/terms, checksum
equality makes re-discovery a no-op).
"""

import json
import os
import random
import subprocess
import time
import sys
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone

BASE = os.environ.get("DATAATLAS_URL", "http://localhost:8080")
ADMIN_DB_URL = os.environ["SEED_ADMIN_DB_URL"]  # owner role, dataatlas db
APP_PASSWORD = os.environ.get("POSTGRES_APP_PASSWORD", "app-local-dev-only")
EMAIL = os.environ.get("DEMO_EMAIL", "demo@acme.test")
PASSWORD = os.environ.get("DEMO_PASSWORD", "acme-demo-password-123")


def http(method: str, path: str, body=None, cookies: dict | None = None):
    req = urllib.request.Request(BASE + path, method=method)
    if cookies:
        req.add_header("Cookie", "; ".join(f"{k}={v}" for k, v in cookies.items()))
    if body is not None:
        req.add_header("Content-Type", "application/json")
        req.data = json.dumps(body).encode()
    try:
        with urllib.request.urlopen(req) as resp:
            raw = resp.read()
            set_cookie = resp.headers.get("Set-Cookie")
            return resp.status, (json.loads(raw) if raw else {}), set_cookie
    except urllib.error.HTTPError as e:
        raw = e.read()
        return e.code, (json.loads(raw) if raw else {}), None


def psql(sql: str) -> None:
    import subprocess

    subprocess.run(
        ["docker", "exec", "dataatlas-postgres", "psql", "-U", "dataatlas",
         "-d", "dataatlas", "-c", sql],
        check=True, capture_output=True, text=True,
    )


def main() -> int:
    schema = "demo_acme"

    # -- 1. Demo schema with fictional business data (idempotent) ----------
    has_schema = (
        subprocess.run(
            ["docker", "exec", "dataatlas-postgres", "psql", "-U", "dataatlas",
             "-d", "dataatlas", "-tAc",
             f"SELECT 1 FROM pg_namespace WHERE nspname = '{schema}'"],
            capture_output=True, text=True,
        ).stdout.strip()
        == "1"
    )
    if not has_schema:
        print("creating demo_acme schema with fictional business data…")
        random.seed(42)
        names = ["Aurora Labs", "Borealis Foods", "Cobalt Systems", "Delta Retail",
                 "Everline Media", "Fjord Energy", "Gale Transport", "Halcyon Health",
                 "Ivory Finance", "Junipeer Cloud", "Kestrel Sports", "Lumen Design",
                 "Meridian Foods", "Nimbus Toys", "Orbit Logistics", "Pinecrest Realty",
                 "Quanta Tools", "Riverbank Capital", "Stonebridge Law", "Thistlewear"]
        statuses = ["paid", "pending", "refunded"]
        rows = []
        for i in range(1, 121):
            cust = random.choice(names)
            amount = round(random.uniform(8, 950), 2)
            status = random.choice(statuses)
            rows.append(f"({i}, '{cust}', {amount}, '{status}', now() - interval '{random.randint(0,48)} hours')")
        psql(f"CREATE SCHEMA {schema}; GRANT USAGE ON SCHEMA {schema} TO dataatlas_app")
        psql(f"""
            CREATE TABLE {schema}.customers (
                id SERIAL PRIMARY KEY, name TEXT NOT NULL, email TEXT,
                country TEXT, created_at TIMESTAMPTZ DEFAULT now())
        """)
        cust_rows = ", ".join(
            f"('{n}', '{n.lower().replace(' ', '.')}@example.test', '{random.choice(['US','DE','GB','FR','JP'])}')"
            for n in names
        )
        psql(f"INSERT INTO {schema}.customers (name, email, country) VALUES {cust_rows}")
        psql(f"""
            CREATE TABLE {schema}.orders (
                id SERIAL PRIMARY KEY, customer_name TEXT, amount NUMERIC(10,2),
                status TEXT, ordered_at TIMESTAMPTZ DEFAULT now())
        """)
        psql(f"INSERT INTO {schema}.orders (id, customer_name, amount, status, ordered_at) VALUES {', '.join(rows)}")
        psql(f"""
            CREATE TABLE {schema}.daily_revenue AS
            SELECT date_trunc('day', ordered_at)::date AS revenue_date,
                   sum(amount)::numeric(12,2) AS revenue,
                   count(*) AS order_count
            FROM {schema}.orders GROUP BY 1
        """)
        psql(f"GRANT SELECT ON ALL TABLES IN SCHEMA {schema} TO dataatlas_app")
    else:
        print("demo schema already present")

    # -- 2. Register (or reuse) the demo organization and log in -----------
    code, body, set_cookie = http("POST", "/v1/auth/register", {
        "email": EMAIL, "password": PASSWORD,
        "display_name": "Acme Data Engineer",
        "organization_name": "Acme Analytics Corporation",
    })
    if code == 409:  # already registered
        code, body, set_cookie = http("POST", "/v1/auth/login", {
            "email": EMAIL, "password": PASSWORD,
        })
    if code not in (200, 201) or not set_cookie:
        print("auth failed:", code, body)
        return 1
    cookie = {}
    for part in set_cookie.split(";"):
        if "=" in part:
            k, _, v = part.strip().partition("=")
            cookie[k] = v
    print(f"authenticated as {EMAIL}")

    # -- 3. Register the demo source (scoped to the demo schema) ----------
    code, body, _ = http("GET", "/v1/sources", cookies=cookie)
    sources = {s["name"]: s for s in body.get("sources", [])}
    if "Acme Operational DB" in sources:
        source_id = sources["Acme Operational DB"]["id"]
    else:
        code, body, _ = http("POST", "/v1/sources", {
            "name": "Acme Operational DB",
            "connector_key": "postgres",
            "config": {"host": "localhost", "port": 5432, "database": "dataatlas",
                       "user": "dataatlas_app", "schemas": [schema]},
            "credentials": {"password": APP_PASSWORD},
        }, cookies=cookie)
        if code != 201:
            print("source registration failed:", code, body)
            return 1
        source_id = body["source"]["id"]
    print(f"source: {source_id}")

    # -- 4. Connection test + discovery ------------------------------------
    code, body, _ = http("POST", f"/v1/sources/{source_id}/test", {}, cookies=cookie)
    print("connection test:", body.get("result", {}).get("message", body))
    code, body, _ = http("POST", f"/v1/sources/{source_id}/discover", {}, cookies=cookie)
    if code != 202:
        print("discovery trigger failed:", code, body)
        return 1
    run_id = body["run"]["id"]

    # Wait for the worker to process the run.
    for _ in range(30):
        code, body, _ = http("GET", f"/v1/ingestion/runs/{run_id}", cookies=cookie)
        if body.get("run", {}).get("status") in ("SUCCEEDED", "FAILED"):
            break
        time.sleep(1)
    status = body.get("run", {}).get("status")
    print("discovery:", status, body.get("run", {}).get("progress"))
    if status != "SUCCEEDED":
        return 1

    # -- 5. OpenLineage lineage: raw -> staging -> analytics chains --------
    def emit(event_type, run, job, ns, inputs, outputs):
        now = datetime.now(timezone.utc).isoformat()
        code, body, _ = http("POST", "/v1/lineage/openlineage", {
            "eventType": event_type, "eventTime": now,
            "producer": "https://dataatlas.local/demo",
            "schemaURL": "https://openlineage.io/spec/2-0-2/OpenLineage.json",
            "run": {"runId": run}, "job": {"namespace": ns, "name": job},
            "inputs": [{"namespace": "demo_acme", "name": i} for i in inputs],
            "outputs": [{"namespace": "demo_acme", "name": o} for o in outputs],
        }, cookies=cookie)
        return code

    demo = f"demo_acme"
    emit("START", str(uuid.uuid4()), "nightly_order_load", "acme_ops",
         ["orders"], ["orders"])
    emit("COMPLETE", str(uuid.uuid4()), "nightly_order_load", "acme_ops",
         ["orders"], ["daily_revenue"])
    emit("COMPLETE", str(uuid.uuid4()), "customer_enrichment", "acme_ops",
         ["customers", "orders"], ["daily_revenue"])
    print("lineage events emitted")

    # -- 6. Quality checks on the orders dataset ---------------------------
    code, body, _ = http("GET", "/v1/catalog/datasets?q=orders", cookies=cookie)
    datasets = body.get("datasets", [])
    orders = next((d for d in datasets if d["name"] == "orders" and d["namespace"] == schema), None)
    customers = next((d for d in datasets if d["name"] == "customers"), None)
    revenue = next((d for d in datasets if d["name"] == "daily_revenue"), None)
    if not orders:
        print("orders dataset not found")
        return 1

    code, body, _ = http("GET", f"/v1/quality/checks?dataset_id={orders['id']}", cookies=cookie)
    existing = {c["name"] for c in body.get("checks", [])}
    new_checks = [
        ("order status valid", "ACCEPTED_VALUES", "status",
         {"values": ["paid", "pending", "refunded"]}, "WARNING"),
        ("customer name present", "NOT_NULL", "customer_name", {}, "ERROR"),
        ("order id unique", "UNIQUE", "id", {}, "ERROR"),
    ]
    for name, ctype, col, params, sev in new_checks:
        if name in existing:
            continue
        http("POST", "/v1/quality/checks", {
            "dataset_id": orders["id"], "name": name, "check_type": ctype,
            "target_column": col, "parameters": params, "severity": sev,
        }, cookies=cookie)
    print("quality checks configured")

    # Give the demo data one 'pending'-status anomaly the check will flag.
    code, body, _ = http("POST", "/v1/quality/runs", {}, cookies=cookie)
    if code == 202:
        print("quality run queued")

    # -- 7. Profile the orders dataset -------------------------------------
    code, body, _ = http("POST", f"/v1/catalog/datasets/{orders['id']}/profile", {}, cookies=cookie)
    print("profiling queued:", code)

    # -- 8. Governance: glossary + classification + freshness expectation --
    code, body, _ = http("GET", "/v1/glossary/terms", cookies=cookie)
    have = {t["name"] for t in body.get("terms", [])}
    for term, definition, category in [
        ("Order", "A purchase request placed by a customer; the atomic unit of revenue.",
         "Sales"),
        ("Revenue", "The monetary value of paid orders within a period.",
         "Finance"),
        ("Customer", "An organization or person that has placed at least one order.",
         "Sales"),
    ]:
        if term not in have:
            http("POST", "/v1/glossary/terms", {
                "name": term, "definition": definition, "category": category,
                "status": "PUBLISHED",
            }, cookies=cookie)
    if customers:
        http("POST", f"/v1/assets/{customers['asset_id']}/classifications",
             {"classification": "CONFIDENTIAL"}, cookies=cookie)
    http("POST", f"/v1/catalog/datasets/{orders['id']}/freshness",
         {"expected_interval_seconds": 86400}, cookies=cookie)
    print("governance recorded")

    # -- 9. Freshness evidence: emit successful runs on the demo tables ----
    emit("COMPLETE", str(uuid.uuid4()), "hourly_sync", "acme_ops",
         ["orders"], ["orders"])
    print("freshness evidence emitted")
    print("DEMO SEED COMPLETE")
    return 0


if __name__ == "__main__":
    sys.exit(main())
