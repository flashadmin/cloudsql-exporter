# cloudsql-exporter

Fork of [trufflesecurity/cloudsql-exporter](https://github.com/trufflesecurity/cloudsql-exporter).

Automatically exports CloudSQL databases in a given project to a GCS bucket.
Supports automatic enumeration of CloudSQL instances and their databases,
and can ensure the correct IAM role bindings are in place for a successful export.

## Why

~~CloudSQL backups are tied to the instance — if the instance or GCP project
is deleted, the backups go with it.~~ CloudSQL now supports
[retaining backups after instance deletion](https://docs.cloud.google.com/sql/docs/postgres/backup-recovery/manage-backups-deleted-instance),
but backups are still tied to the GCP project. Exporting to a GCS bucket
in a separate project provides additional data retention assurance and
better control over retention policies.

## Usage

```
cloudsql-exporter --help
usage: cloudsql-backup --bucket=BUCKET --project=PROJECT [<flags>]

Export Cloud SQL databases to Google Cloud Storage

Flags:
  --help                 Show context-sensitive help
  --bucket=BUCKET        Google Cloud Storage bucket name
  --project=PROJECT      GCP project ID
  --instance=INSTANCE    Cloud SQL instance name, if not specified all
                         within the project will be enumerated
  --compression          Enable compression for exported SQL files
  --ensure-iam-bindings  Ensure that the Cloud SQL service account has
                         the required IAM role binding to export
  --version              Show application version
```

## Build

```bash
go build -o cloudsql-exporter .
```

## Docker

Example multi-stage Dockerfile that builds from source:

```dockerfile
FROM golang:bookworm AS builder
WORKDIR /build
ENV CGO_ENABLED=0
RUN git clone --branch <tag> --single-branch https://github.com/flashadmin/cloudsql-exporter.git \
    && cd cloudsql-exporter \
    && go build -o cloudsql-exporter .

FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:alpine
COPY --from=builder /build/cloudsql-exporter/cloudsql-exporter /usr/bin/cloudsql-exporter
COPY entrypoint.sh /opt/entrypoint.sh
CMD ["/bin/bash", "-c", "/opt/entrypoint.sh"]
```

Example entrypoint script for exporting multiple instances:

```bash
#!/usr/bin/env bash
set -euo pipefail
IFS=',' read -ra INSTANCES <<< "${INSTANCES}"
for i in "${INSTANCES[@]}"; do
  cloudsql-exporter --project="${PROJECT}" --bucket="${BUCKET}" --instance="${i}" --compression
done
```

## Cloud Run Job

The tool runs once and exits, making it a good fit for
[Cloud Run Jobs](https://cloud.google.com/run/docs/create-jobs)
triggered by [Cloud Scheduler](https://cloud.google.com/scheduler/docs).

```bash
# Create the job
gcloud run jobs create cloudsql-exporter-example \
  --image=<registry>/cloudsql-exporter:<tag> \
  --region=europe-west4 \
  --project=<operations-project> \
  --service-account=<sa>@<project>.iam.gserviceaccount.com \
  --set-env-vars="PROJECT=<target-project>,BUCKET=<backup-bucket>,INSTANCES=<instance-1>,<instance-2>" \
  --cpu=1 \
  --memory=512Mi \
  --max-retries=0 \
  --task-timeout=43200s

# Schedule it (e.g. daily at 04:00 UTC)
gcloud scheduler jobs create http cloudsql-exporter-example-scheduler \
  --location=europe-west1 \
  --schedule="0 4 * * *" \
  --time-zone="UTC" \
  --uri="https://europe-west4-run.googleapis.com/apis/run.googleapis.com/v1/namespaces/<operations-project>/jobs/cloudsql-exporter-example:run" \
  --http-method=POST \
  --attempt-deadline=180s \
  --oauth-service-account-email=<sa>@<project>.iam.gserviceaccount.com
```

## IAM

The service account running the job needs:

- `roles/cloudsql.viewer` on each target project — to list instances, databases, and trigger exports
- `roles/run.invoker` on the operations project — to allow Cloud Scheduler to invoke the job

The CloudSQL instance service accounts (format: `p<project-number>-<id>@gcp-sa-cloud-sql.iam.gserviceaccount.com`)
need access to the GCS bucket:

- `roles/storage.objectCreator` — to write export files
- `roles/storage.objectViewer` — to validate the export

Use `--ensure-iam-bindings` to grant these automatically, or pre-grant them via IAM.
