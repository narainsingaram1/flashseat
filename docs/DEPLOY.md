# Deploying FlashSeat

FlashSeat runs as one Docker Compose stack: PostgreSQL, Redis, Toxiproxy, two
API replicas, the control plane, an nginx gateway, the k6 load generator and the
Next.js web app. Fault injection needs Toxiproxy and Docker's restart policy,
and k6 runs next to the API. So deploy the **whole stack to one Linux server**.
Serverless hosts (Vercel, Netlify) can't run it.

`docker-compose.prod.yml` adds Caddy as the only public entry point:

| Path | Goes to |
|---|---|
| `https://$DOMAIN/` | web app |
| `https://$DOMAIN/api/…` | gateway → API replicas / control plane (incl. the SSE stream) |

PostgreSQL, Redis, the gateway, Prometheus and API `/metrics` are not exposed.
HTTPS certificates come from Let's Encrypt automatically.

## 1. Get a server

Ubuntu 24.04, **4 vCPU / 8 GB RAM** or more. The arena preset (9,000 seats,
thousands of k6 buyers) is CPU-bound, and smaller boxes will show higher
latencies. For example, Hetzner CPX31/CCX23, DigitalOcean 4 vCPU/8 GB, or AWS
c7i.xlarge.

## 2. Point a domain at it

Create an `A` record (and `AAAA` if you have IPv6), e.g. `flashseat.example.com
→ <server IP>`. Ports 80 and 443 must be reachable. Caddy needs them to issue
the certificate.

## 3. Install and start

SSH in as root and run:

```bash
curl -fsSL https://raw.githubusercontent.com/narainsingaram1/flashseat/main/scripts/server-setup.sh | bash
```

It installs Docker, opens only SSH/80/443 in the firewall, clones the repo to
`/opt/flashseat`, asks for your domain, writes `.env` with random secrets and
runs `make deploy`. The first build takes a few minutes.

Manual equivalent:

```bash
git clone https://github.com/narainsingaram1/flashseat.git /opt/flashseat && cd /opt/flashseat
printf 'DOMAIN=%s\nPOSTGRES_PASSWORD=%s\nADMISSION_SECRET=%s\n' flashseat.example.com "$(openssl rand -hex 32)" "$(openssl rand -hex 32)" > .env
make deploy
```

## Operating it

```bash
make deploy        # pull changes first with `git pull`; rebuilds and restarts
make deploy-logs   # follow logs
docker compose -f docker-compose.yml -f docker-compose.prod.yml down      # stop (keeps data)
docker compose -f docker-compose.yml -f docker-compose.prod.yml down -v   # wipe data + recorded runs
```

`DOMAIN` is baked into the web build, so after changing it, run `make deploy`
again. New migrations only run on a fresh database volume (see the README).

## Public-use guardrails

- One sale runs at a time; a second start gets `409 simulation_running`. Every
  visitor watches the same live sale, which is intended.
- Buyers per sale are capped at 20,000 by the control plane.
- Per visitor IP, the gateway allows a burst of 7 control `POST`s (start a sale,
  inject faults), then 12 per minute. Beyond that it returns `429`. It also allows
  8 concurrent live streams per IP.
- There is no login. Anyone with the URL can start sales and inject faults. If
  you want only yourself to drive it, put Caddy `basic_auth` on
  `POST /api/v1/control/*` in `infra/caddy/Caddyfile`.
