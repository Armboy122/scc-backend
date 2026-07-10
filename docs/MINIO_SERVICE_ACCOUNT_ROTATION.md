# MinIO application service-account rotation

The MinIO root pair bootstraps and administers the `scc-minio` server. It must
not be the credential used by the API or the scheduled object backup. This
runbook rotates `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` to a dedicated MinIO
service account while retaining `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` only
for the MinIO container and reviewed administration.

## Permission boundary

`scripts/rotate-minio-service-account.sh` creates an inline service-account
policy for the configured `MINIO_BUCKET` with only:

| Resource | Allowed actions | Why |
|---|---|---|
| `arn:aws:s3:::<bucket>` | `s3:GetBucketLocation`, `s3:GetBucketPolicy`, `s3:ListBucket` | API bucket/readiness checks and authenticated backup inventory |
| `arn:aws:s3:::<bucket>/evidence/v1/*` | `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` | signed evidence reads/writes, metadata/magic-byte reads, backup, and explicit cleanup |

The validation gate proves the account can list the private bucket, read its
policy state, write/read/stat/delete a probe below `evidence/v1/`, and that it
cannot:

- write outside `evidence/v1/`;
- create a bucket;
- change anonymous/bucket policy;
- call MinIO admin server-info APIs.

Production API startup is read-only for object-storage administration: it calls
the same `Ready` check as `/api/v1/readyz`. Bucket creation and
`mc anonymous set none` remain owned by `deploy-vps.sh`, which receives root
credentials through a short-lived MinIO-only env file. Do not rotate an older
API build that still calls `CreateBucketIfNotExists` in production; deploy the
image built from this source tree as the candidate in the activation step.

The scheduled `backup-vps.sh` intentionally reads the application service
account from the protected env. Its `mc mirror` therefore remains within the
same read/list boundary and receives no MinIO admin credential.

## Safety model

- Run the tool as root. It refuses non-root operation by default. The examples
  use `sudo -n` so automation fails instead of waiting for a password prompt.
  `ROTATION_ALLOW_NON_ROOT=true` exists only for isolated local tests and must
  not be used for production rotation.
- `/opt/scc-backend/.env` must be an absolute, regular non-symlink file with no
  group/other permissions (normally mode `600`). It is parsed without `source`
  or `eval`.
- The tool requires the configured immutable `MINIO_MC_IMAGE` digest. It sends
  credentials to the ephemeral `mc` container through stdin, never Docker argv
  or Docker environment variables. Normal `mc` output is suppressed.
- A non-blocking `flock` prevents overlapping rotations.
- Every rotation has a mode-`700` directory below
  `/opt/scc-backend/credential-rotations/`. `before.env` and `candidate.env` are
  mode `600`; they are protected recovery material and must never be printed,
  copied to chat/tickets, or committed.
- `.env` replacement uses a temporary file in the same directory followed by
  an atomic rename and preserves its protected ownership/mode.
- `prepare` validates permissions before changing `.env`. The running backend
  remains on its existing credential until the normal candidate deploy.
- A former service account is not revoked until `finalize` has proven both the
  active container's exact credential pair and `/api/v1/readyz`.
- Rollback restores `.env` first but deliberately keeps the candidate account
  valid until the old credential has been redeployed and verified. Only
  `finalize-rollback` revokes it.
- The script never rotates the MinIO root pair. Root credential rotation is a
  separate maintenance operation because it recreates the MinIO container.

## One-time installation and preflight

Install the reviewed scripts without changing the application env:

```bash
sudo -n install -d -m 0755 -o root -g root /opt/scc-backend/bin
sudo -n install -m 0755 scripts/rotate-minio-service-account.sh \
  /opt/scc-backend/bin/rotate-minio-service-account.sh
sudo -n install -m 0755 scripts/deploy-vps.sh \
  /opt/scc-backend/bin/deploy-vps.sh
sudo -n APP_ENV_PATH=/opt/scc-backend/.env \
  /opt/scc-backend/bin/rotate-minio-service-account.sh status
```

Before `prepare`, confirm that:

1. the immutable backend digest was built from the source containing the
   production `Ready`-only startup path;
2. no deployment or backup is running;
3. the configured bucket exists and is private;
4. `MINIO_MC_IMAGE` is the reviewed 2025 client digest already present or
   pullable by Docker;
5. the current backend and latest verified backup are healthy.

## Normal rotation

Use the exact reviewed backend image digest from the release pipeline. These
are the operational commands; none of them prints a credential:

```bash
sudo -n APP_ENV_PATH=/opt/scc-backend/.env \
  /opt/scc-backend/bin/rotate-minio-service-account.sh prepare

sudo -n env \
  APP_ENV_PATH=/opt/scc-backend/.env \
  GHCR_IMAGE='ghcr.io/<owner>/scc-backend@sha256:<reviewed-64-hex-digest>' \
  /opt/scc-backend/bin/deploy-vps.sh

sudo -n APP_ENV_PATH=/opt/scc-backend/.env \
  /opt/scc-backend/bin/rotate-minio-service-account.sh finalize

sudo -n APP_ENV_PATH=/opt/scc-backend/.env \
  /opt/scc-backend/bin/rotate-minio-service-account.sh status
```

Expected final status: `No MinIO credential rotation is pending.` Run the VPS
doctor afterward. Its env result must change from the root-pair warning to:
`application MinIO credentials are separated from root credentials`.

`deploy-vps.sh` may also require the normal read-only GHCR username/token in a
manual deployment. Provide those exactly as documented in `DEPLOYMENT.md`; do
not add them to the application env.

## Rollback

If the activation deploy or `finalize` fails, do not delete the service account
or protected rotation directory. Restore and redeploy the prior pair:

```bash
sudo -n APP_ENV_PATH=/opt/scc-backend/.env \
  /opt/scc-backend/bin/rotate-minio-service-account.sh rollback

sudo -n env \
  APP_ENV_PATH=/opt/scc-backend/.env \
  GHCR_IMAGE='ghcr.io/<owner>/scc-backend@sha256:<reviewed-64-hex-digest>' \
  /opt/scc-backend/bin/deploy-vps.sh

sudo -n APP_ENV_PATH=/opt/scc-backend/.env \
  /opt/scc-backend/bin/rotate-minio-service-account.sh finalize-rollback
```

The same immutable image can be used for credential rollback because schema is
not rolled back. The ordinary deploy retains/restores the current backend on a
candidate failure; the credential tool independently restores the protected
env, so both layers must complete.

## Interrupted prepare and recovery-required

`prepare` traps ordinary errors and signals. If it already created the service
account, it attempts to remove it; if it changed `.env`, it restores
`before.env`. A fully cleaned failure removes the pending marker and records
phase `aborted`.

If either automatic cleanup cannot be proved, the phase becomes
`recovery-required` and the pending marker is retained. In that case:

1. do not delete the state directory or either account;
2. run `status` (never `cat` the protected env files);
3. inspect which credential pair the active `scc-backend` container uses with a
   redacting tool, not raw `docker inspect` output;
4. choose the normal finalize or rollback sequence only after the active pair
   is known;
5. keep both service accounts valid until the matching backend passes
   `/api/v1/readyz`.

Escalate a persistent `recovery-required` state for reviewed recovery. Do not
manually run `mc admin user svcacct rm` based only on `.env`; the active
container may still be using that account.

## Verification tests

The deterministic local test covers atomic prepare/finalize/rollback,
permission-gate failure cleanup, duplicate env rejection, active-container
matching, and credential redaction:

```bash
bash scripts/test-minio-service-account-rotation.sh
```

The opt-in integration test uses a local MinIO 2025 container. It creates and
removes a scratch Docker network, private bucket, and service account:

```bash
SCC_TEST_MINIO_CONTAINER=scc-test-minio \
SCC_TEST_MINIO_ROOT_USER='<test-root-user>' \
SCC_TEST_MINIO_ROOT_PASSWORD='<test-root-password>' \
SCC_TEST_MINIO_MC_IMAGE='minio/mc@sha256:<reviewed-digest>' \
bash scripts/test-minio-service-account-rotation-integration.sh
```

Never point the integration test at the production container.
