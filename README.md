# Amazon Lightsail CLI

`lightsailctl` deploys your app — Go, Node, Python, Java, Ruby, .NET,
PHP, a static site, a `Dockerfile`, or a `docker-compose.yml` — to an
Amazon Lightsail instance with **one command**. Builds run on the
instance, so there's no Docker, buildx, or cross-compile setup on your
laptop.

## Install

```sh
brew install aws/tap/lightsailctl
```

## Deploy

```sh
lightsailctl deploy
```

Any command run without required flags drops into an interactive
wizard — pass `-y` in CI to disable it.

## What it builds

`lightsailctl deploy` looks at your project and picks a strategy:

| It finds | It runs |
|---|---|
| `docker-compose.yml` | `docker compose up --build -d` |
| `Dockerfile` | `docker build` (port from `EXPOSE`, default 8080) |
| `go.mod`, `package.json`, `requirements.txt`, `pom.xml`, `Gemfile`, `*.csproj`, `index.html`, … | Cloud Native Buildpacks (port 8080) |

You'll see the chosen strategy before each deploy:

```
Build strategy
  Strategy  Cloud Native Buildpacks (Go via go.mod)
  Builder   paketobuildpacks/builder-jammy-base
  Note      no Dockerfile needed — built on the instance
```

## `lightsail.conf`

One YAML file per project, written on first `deploy`:

```yaml
app: my-web-app
env: dev
region: us-east-2
ignore:          # extra paths excluded from the deploy tarball
  - .venv
  - target
```

## App Commands

```sh
lightsailctl app  manage Lightsail Applications

USAGE
  lightsailctl app [flags]

COMMANDS
  add-target         add an instance as a deployment target
  create             create a new Lightsail application
  delete             delete an app and all its buckets
  deploy             deploy current dir to an app/env
  disable-gh-action  remove the GitHub Actions deploy workflow and its IAM role
  enable-gh-action   set up a GitHub Actions deploy workflow for this app
  get                get one app
  list               list apps
  local              [instance] commands invoked over SSH by the client
  logs               tail docker compose logs on a target
  remove-target      remove an instance from deployment targets

GLOBAL FLAGS
        --debug             enable trace logging (flips the global level threshold to TRACE) [$LIGHTSAILCTL_DEBUG]
    -h, --help              help for this command
        --log-dest string   log sink: file (default) | stderr | none [$LIGHTSAILCTL_LOG_DEST] (default "file")
        --log-file string   override the default log path (only when --log-dest=file; retention not applied) [$LIGHTSAILCTL_LOG_FILE]
    -y, --no-interactive    disable interactive prompts and live progress (for CI / scripts) [$LIGHTSAILCTL_NO_INTERACTIVE]
    -o, --output string     output: short|wide|yaml|json [$LIGHTSAILCTL_OUTPUT] (default "short")
        --region string     AWS region (blank = query all regions) [$LIGHTSAILCTL_REGION]
        --verbose           verbose output [$LIGHTSAILCTL_VERBOSE]
```

## Instance Commands

```sh
lightsailctl instance  manage Lightsail instances

USAGE
  lightsailctl instance [flags]

COMMANDS
  create    create a new Lightsail instance
  delete    delete a Lightsail instance
  firewall  update instance firewall rules
  get       get one instance
  list      list instances
  ssh       SSH to an instance
  start     start a stopped instance
  stop      stop a running instance

GLOBAL FLAGS
        --debug             enable trace logging (flips the global level threshold to TRACE) [$LIGHTSAILCTL_DEBUG]
    -h, --help              help for this command
        --log-dest string   log sink: file (default) | stderr | none [$LIGHTSAILCTL_LOG_DEST] (default "file")
        --log-file string   override the default log path (only when --log-dest=file; retention not applied) [$LIGHTSAILCTL_LOG_FILE]
    -y, --no-interactive    disable interactive prompts and live progress (for CI / scripts) [$LIGHTSAILCTL_NO_INTERACTIVE]
    -o, --output string     output: short|wide|yaml|json [$LIGHTSAILCTL_OUTPUT] (default "short")
        --region string     AWS region (blank = query all regions) [$LIGHTSAILCTL_REGION]
        --verbose           verbose output [$LIGHTSAILCTL_VERBOSE]
```

---

## AWS CLI plugin (separate use case)

`lightsailctl` is also the binary that the AWS CLI shells out to for
`aws lightsail push-container-image`. You don't run this directly —
the AWS CLI does. This section documents that contract for
troubleshooting and Windows installs.

### Windows install

For Windows, follow [Install Docker, AWS CLI, and the Lightsail Control
plugin for containers][win].

### Plugin contract

```sh
$ lightsailctl --plugin -h
Usage of `lightsailctl --plugin`:
  --input payload
        plugin payload
  --input-stdin
        receive plugin payload on stdin
```

### Under the hood

The AWS CLI command:

```sh
aws lightsail push-container-image \
  --service-name hello \
  --image hello-world:latest \
  --label www
```

is approximately equivalent to invoking `lightsailctl` like this:

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
Digest: sha256:0b159cd1ee1203dad901967ac55eee18c24da84ba3be384690304be93538bea8
Image "hello-world:latest" registered.
Refer to this image as ":hello.www.73" in deployments.
```

## Contributing & feedback

Open an [issue][issue] for bugs or feature ideas. See
[CONTRIBUTING.md](CONTRIBUTING.md), including the security disclosure
process.

This project is licensed under the Apache-2.0 License.

[win]: https://docs.aws.amazon.com/lightsail/latest/userguide/amazon-lightsail-install-software.html#install-lightsailctl-on-windows
[issue]: https://github.com/aws/lightsailctl/issues/new
