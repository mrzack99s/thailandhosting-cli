# th — ThailandHosting CLI

Use ThailandHosting from your terminal or CI: deploy Containers and static
sites, copy files to Object Storage, connect to databases and caches, and
send or receive Queue Service messages.

```
th auth login
th container deploy api --source .
th static-site deploy docs ./dist
th object-storage cp -r ./backups th://my-bucket/backups
th database connect app-db
th queue tail jobs
```

## Install

Linux or macOS:

```
curl -fsSL https://raw.githubusercontent.com/mrzack99s/thailandhosting-cli/main/install.sh | sh
```

Windows (PowerShell):

```
irm https://raw.githubusercontent.com/mrzack99s/thailandhosting-cli/main/install.ps1 | iex
```

Or download an archive for your system from
[Releases](https://github.com/mrzack99s/thailandhosting-cli/releases), or
with Go: `go install github.com/mrzack99s/thailandhosting-cli/cmd/th@latest`.

## Get started

```
th auth login                  # opens the Console; approve the code shown
th projects list
th config set project acme     # ID or slug
th config set region bkk       # th regions list
```

`th help` lists every command; add `--help` to any command for its flags.

## Containers

```
th container deploy api --source .                        # build this folder and deploy it
th container deploy api --image ghcr.io/acme/api:1.4 --port 3000
th container deploy api --cpu 1 --memory 512Mi --min-instances 0 --max-instances 5 \
  --set-env-vars NODE_ENV=production --secret-env API_KEY=xxxx
th container services list
th container services logs api
th container revisions list --service api
th container services update-traffic api --to-revision 3  # roll back
```

`--source` needs no Docker on your computer. With a `Dockerfile` it is used;
without one, Node.js, Python and Go projects get one written for them (shown
in the build log). Files in `.thignore` (or `.dockerignore`, then
`.gitignore`) are not uploaded. Your app must listen on `$PORT`.

Images are kept in your Project's registry (20 GB per Project): the newest 10
revisions of each Container and any tagged image.

```
th container images list api
th container images delete api:old-tag
th auth configure-docker       # to docker push your own images
```

## Static Web

```
th static-site deploy docs ./dist        # creates the site on the first deploy
th static-site releases docs
th static-site rollback docs previous
th static-site domains add docs www.example.com
```

Build the site first (for example `npm run build`) and deploy the output
folder.

## Object Storage

```
th object-storage buckets create my-bucket
th object-storage cp ./report.pdf th://my-bucket/reports/
th object-storage cp -r ./backups th://my-bucket/backups
th object-storage sync ./public th://my-bucket/site --delete
th object-storage ls th://my-bucket/reports/
th object-storage sign th://my-bucket/reports/report.pdf --expires 1h
```

## Database and Cache

```
th database create app-db --engine postgres
th database public app-db --enable --allow 203.0.113.7/32   # your IP
th database connect app-db             # opens psql / mysql with the credentials
th database credentials app-db
th database backups create app-db
th cache create sessions --engine valkey
th cache connect sessions              # opens redis-cli
```

A database's private address works only inside your Project's network; to
connect from your computer, allow your own address first.

## Queue Service

```
th queue create jobs --visibility-timeout 60s
th queue send jobs '{"order": 1042}'
th queue receive jobs --max 10 --ack
th queue tail jobs                     # receive and acknowledge until Ctrl+C
```

## CI

Create a key in the Console and store it as a secret in your CI:

| To | Key | Variables |
| --- | --- | --- |
| Deploy Containers | API key with **Containers: deploy** (`run:write`) | `TH_API_KEY` |
| Deploy Static Web | API key with **Static Web: deploy** (`static:write`) | `TH_API_KEY` |
| Object Storage | Object Storage access key | `TH_OBJECT_STORAGE_KEY_ID`, `TH_OBJECT_STORAGE_SECRET` |
| Queue Service | Queue key | `TH_QUEUE_KEY_ID`, `TH_QUEUE_SECRET` |

GitHub Actions:

```yaml
on:
  push:
    branches: [main]
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: |
          curl -fsSL https://raw.githubusercontent.com/mrzack99s/thailandhosting-cli/main/install.sh | TH_INSTALL_DIR="$HOME/.local/bin" sh
          echo "$HOME/.local/bin" >> "$GITHUB_PATH"
      - run: th container deploy api --source . --quiet
        env:
          TH_API_KEY: ${{ secrets.TH_API_KEY }}
```

## Settings

`th config set|get|unset|list` stores `project`, `region` and `endpoint`.
Environment variables override them: `TH_PROJECT`, `TH_REGION`,
`TH_ENDPOINT`. Every command also takes `--project`, `--region`,
`--format json` and `--quiet` (no prompts).

## License

[Apache License 2.0](LICENSE)
