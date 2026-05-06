# Amazon Lightsail CLI

`lightsailctl` is a first-class CLI and TUI for Amazon Lightsail. It also
serves as an [AWS CLI extension][lscli] (`aws lightsail push-container-image`
shells out to it).

## Two faces

**1. First-class CLI** — deploy Docker Compose applications to a Lightsail
instance with one command:

```sh
lightsailctl deploy                 # deploy current dir to an app/env
lightsailctl                        # launch the TUI dashboard
lightsailctl app list               # list applications
lightsailctl app status --name foo  # show health + endpoints
lightsailctl app logs --name foo    # tail docker compose logs over SSH
lightsailctl app delete --name foo  # tear down (buckets, tags, firewall)
lightsailctl app --help             # everything else
```

Every command missing a required flag launches an inline wizard (suppress
with `-y` for CI).

**2. AWS-CLI plugin** — invoked automatically by the AWS CLI:

```sh
$ lightsailctl --plugin -h
Usage of `lightsailctl --plugin`:
  --input payload
        plugin payload
  --input-stdin
        receive plugin payload on stdin
```

## Applications

A Lightsail Application is a client-side aggregate over Lightsail buckets and
instances. The model lives entirely in this CLI:

- **Buckets.** One app-config bucket `ls--<acct>--<app>` plus one env bucket
  per environment `ls--<acct>--<app>--<env>`. Deploy tarballs land in
  `s3://<env-bucket>/deploy/<unix>-<sha>.tar.gz`.
- **Instance tags.** Target instances are marked with `ls:app:<app>:<env> =
  true`.
- **Status files.** The on-instance watcher writes
  `<instance>_status.json` to the env bucket on every deploy or every minute,
  whichever comes first.
- **On-instance layout.** `/opt/lightsail/<app>/<env>/current` holds the
  deployed source; the watcher runs under a systemd unit
  `lightsail-watch-<app>-<env>.service`.

The watcher binary is `lightsailctl` itself, invoked on the instance as
`lightsailctl app local watch`. No separate daemon to ship.

## Creating an app

```sh
lightsailctl app create \
  --name my-web-app \
  --env dev \
  --region us-east-2 \
  --instance my-lightsail-instance \
  --agent-path ./dist/lightsailctl_linux_amd64_v1/lightsailctl
```

`--agent-path` must point at a **linux/amd64** `lightsailctl` binary. Until
we publish releases, build one locally:

```sh
GOOS=linux GOARCH=amd64 go build -o /tmp/lightsailctl-linux .
lightsailctl app create --agent-path /tmp/lightsailctl-linux ...
```

On success the wizard writes `./lightsail.conf` so subsequent
`lightsailctl deploy` calls pick up the app/env/region without flags.

## `lightsail.conf`

Minimal YAML, one per project:

```yaml
app: my-web-app
env: dev
region: us-east-2
ignore:          # paths excluded from the deploy tarball (additive to
  - .venv        # built-ins: .git, .lightsail, node_modules, .DS_Store)
  - target
```

Discovery: `Find()` walks up from the current directory, just like git.

## Logging

`lightsailctl` writes structured, text-formatted logs at `INFO` by default.
Attach one to a bug report and a reviewer can reconstruct the run.

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `--debug` | `LIGHTSAILCTL_DEBUG` | off | flip the threshold from `INFO` to `TRACE` (includes SDK retries) |
| `--log-dest` | `LIGHTSAILCTL_LOG_DEST` | `file` | sink: `file`, `stderr`, or `none` |
| `--log-file` | `LIGHTSAILCTL_LOG_FILE` | _auto_ | override path when `--log-dest=file` (no retention on user-supplied paths) |

Default log location: `$HOME/.lightsailctl/logs/<UTC-ts>-<pid>.log`. The
file is created lazily on the first record, and the directory is pruned
at startup (files older than 14 days or beyond the 100-most-recent are
deleted). Every log line includes `ui=cli|interactive|tui|watch`, the
cobra command path, and the saga/step context emitted by the runtime.

The on-instance watcher (installed by `app create`) runs under systemd
with `--log-dest=stderr`; `journalctl -u lightsailctl-<app>-<env>` is the
support artifact.

## Installing

### Homebrew 🍻

```sh
brew install aws/tap/lightsailctl
```

### From Source

`lightsailctl` is written in Go, so please [install Go.][getgo]

If all you want is to install `lightsailctl` binary, then do the following:

```sh
go install github.com/aws/lightsailctl@latest
```

> **Note:** the executable is installed in `$HOME/go/bin` on macOS/Linux/Unix
> and in `%USERPROFILE%\go\bin` on Windows.

Keep reading if you want to work with `lightsailctl` source code locally.

After you clone this repo and open your terminal app in it, you'll be
able to test and build this code like so:

```sh
go test ./...
go install ./...
```

### Windows

For Windows installation instructions, please see [Install Docker, AWS CLI, and the Lightsail Control plugin for containers](https://docs.aws.amazon.com/lightsail/latest/userguide/amazon-lightsail-install-software.html#install-lightsailctl-on-windows).

## Development

The `Makefile` wraps the common tasks:

```sh
make tools     # install goreleaser and golangci-lint (one-time)
make lint      # run golangci-lint
make test      # run unit tests
make ci        # lint + test (same as CI)
make snapshot  # build local release artifacts via goreleaser (dist/)
make help      # list targets
```

### Integration test

An end-to-end integration test lives at `test/integ/`. It drives the real
CLI against real AWS resources (creates a bucket, uploads a deploy, hits the
deployed endpoint, deletes everything). It requires an existing Lightsail
instance with Docker installed.

```sh
GOOS=linux GOARCH=amd64 go build -o /tmp/lightsailctl-linux .

LS_INTEG_INSTANCE=my-inst \
LS_INTEG_REGION=us-east-2 \
LS_INTEG_AGENT_PATH=/tmp/lightsailctl-linux \
  go test -tags=integ -v -timeout=20m ./test/integ/...
```

The test is gated behind `-tags=integ` so `make ci` ignores it. Each phase
(`Create`, `Deploy`, `Status`, `Delete`) is a `t.Run` subtest so you can
target one with e.g. `-run TestEndToEnd/Deploy` after a successful earlier
run. Set `LS_INTEG_KEEP=1` to skip teardown.

## Under The Hood

Let's consider this command and see what actually happens:

```sh
aws lightsail push-container-image \
 --service-name hello \
 --image hello-world:latest \
 --label www
```

The above command pushes a local container image with tag
`hello-world:latest` to make it available in Lightsail container
service deployments for service `hello`.

This container image pushing logic requires a number of steps that are
outsourced from AWS CLI to `lightsailctl`.

Here's a shell invocation of `lightsailctl` that approximates what AWS
CLI does when the command above is invoked:

```sh
$ echo '{
  "inputVersion": "1",
  "operation": "PushContainerImage",
  "payload": {
    "service": "hello",
    "label":   "www",
    "image":   "hello-world:latest"
  }
}' | lightsailctl --plugin --input-stdin

85fcec7ef3ef: Layer already exists 
3e5288f7a70f: Layer already exists 
56bc37de0858: Layer already exists 
1c91bf69a08b: Layer already exists 
cb42413394c4: Layer already exists 
Digest: sha256:0b159cd1ee1203dad901967ac55eee18c24da84ba3be384690304be93538bea8
Image "hello-world:latest" registered.
Refer to this image as ":hello.www.73" in deployments.
```

## Security Disclosures

See [CONTRIBUTING.md](CONTRIBUTING.md#security-issue-notifications) for
more information.

## Giving Feedback and Contributing

Aside from the security feedback covered above, do you have any
feedback, bug reports, questions or feature ideas?

You are welcome to write up an [issue][issue] for us.

Please read about [Contributing Guidelines.](CONTRIBUTING.md)

## Releases

Releases are automated: pushing a `v*.*.*` tag triggers
[`.github/workflows/release.yml`][release], which uses
[GoReleaser][goreleaser] to cross-compile binaries and attach them to a
new GitHub release.

## License

This project is licensed under the Apache-2.0 License.

[lscli]: https://docs.aws.amazon.com/cli/latest/reference/lightsail/index.html
[getgo]: https://go.dev/doc/install
[issue]: https://github.com/aws/lightsailctl/issues/new
[release]: .github/workflows/release.yml
[goreleaser]: https://goreleaser.com/
