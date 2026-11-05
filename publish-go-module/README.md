# publish-go-module

This GitHub Action publishes a Go module to `packages.narwhl.dev`, narwhl's Go module proxy, when a release tag is pushed. It packs the module from the tagged commit with [`golang.org/x/mod/zip`](https://pkg.go.dev/golang.org/x/mod/zip) and uploads it with the job's GitHub OIDC token, so the repository needs no secrets. Afterwards anyone can install it:

```sh
go get packages.narwhl.dev/go/authz@latest
```

### Usage

```yml
on:
  push:
    tags: ["go/*/v*"]

permissions:
  contents: read
  id-token: write # the OIDC token is the only credential packages.narwhl.dev accepts

concurrency:
  group: publish-${{ github.ref }}
  cancel-in-progress: false

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: narwhl/actions/publish-go-module@latest
```

A fuller workflow, which also builds and tests the module on its own before publishing, is [below](#example-workflow).

### Naming and tags

| | Example |
|---|---|
| Module path | `packages.narwhl.dev/go/authz` |
| go.mod | `go/authz/go.mod`, declaring exactly that path |
| Release tag | `go/authz/v0.1.0`: the go.mod directory, then the version |
| v2 and later | module `packages.narwhl.dev/go/authz/v2`, go.mod still in `go/authz/`, tag `go/authz/v2.0.0` |

Change a module's `go.mod` to its `packages.narwhl.dev` path before its first tag. Once a version is published, changing the path creates a new module.

### Rules

- **A published version never changes.** sum.golang.org records each version's hash the first time it is fetched, so different contents later would break every user. To fix a bad release, publish a new version and add a [`retract`](https://go.dev/ref/mod#go-mod-file-retract) directive. Never move or re-push a tag. packages.narwhl.dev refuses to replace a version (409). Re-running the same release is accepted.
- Only release tags can publish, and only the version the tag names. packages.narwhl.dev accepts tokens only from the repositories allowed to publish there, which today is [narwhl/library](https://github.com/narwhl/library).
- Release versions only (`v1.2.3`, `v1.2.3-rc.1`). No pseudo-versions, no `+build` metadata.
- The module zip may be at most 32 MiB.
- Protect the release tags (a ruleset that blocks updating and deleting `go/*/v*`): pushing a tag is what grants permission to publish.

### Action Inputs

- `tag` — release tag to publish, `<module directory>/<version>`. Defaults to the pushed tag.
- `path` — checkout holding the tag, relative to the workspace. Defaults to the workspace.
- `host` — host part of the module paths. Defaults to `packages.narwhl.dev`.
- `proxy` — module proxy to publish to. Defaults to `https://<host>`.
- `mirror` — public mirror to warm after publishing, so pkg.go.dev and sum.golang.org see the version straight away. Defaults to `https://proxy.golang.org`; empty to skip.
- `out` — write the version into this directory as a GOPROXY tree instead of publishing it, for a dry run. Relative to the workspace.

### What it does

1. Packs the module from the tagged commit. Nested modules and untracked files are left out, as the go command does.
2. Checks the zip with `zip.CheckZip` and uploads it with the job's OIDC token (audience `https://packages.narwhl.dev`).
3. Downloads the version back and checks its `h1:` hash.
4. Asks proxy.golang.org for the version, so pkg.go.dev and sum.golang.org pick it up.

The job summary shows the version's `go.sum` line.

### Outside GitHub Actions

The action is a Go program you can run directly:

```sh
go install github.com/narwhl/actions/publish-go-module@latest
publish-go-module -repo path/to/library -tag go/authz/v0.1.0 -out /tmp/goproxy  # dry run
GOPROXY=file:///tmp/goproxy GONOSUMDB=packages.narwhl.dev go get packages.narwhl.dev/go/authz@v0.1.0
```

To publish rather than write a dry-run tree, set `REGISTRY_TOKEN` to a token packages.narwhl.dev accepts.

### Example workflow

Save as `.github/workflows/release-go.yml`:

```yml
name: Release Go module

on:
  push:
    tags:
      - "go/*/v*"

permissions:
  contents: read
  id-token: write

# One publish per tag at a time. If a tag is moved while its first publish is
# still running, the second run waits and then gets a clean 409, rather than
# racing the first and mixing files from two commits.
concurrency:
  group: publish-${{ github.ref }}
  cancel-in-progress: false

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7

      - name: Locate module
        id: module
        run: echo "dir=${GITHUB_REF_NAME%/*}" >> "$GITHUB_OUTPUT"

      - uses: actions/setup-go@v7
        with:
          go-version-file: ${{ steps.module.outputs.dir }}/go.mod
          cache-dependency-path: ${{ steps.module.outputs.dir }}/go.sum

      - name: Build and test the module on its own
        working-directory: ${{ steps.module.outputs.dir }}
        env:
          GOWORK: "off"
        run: |
          go build ./...
          go vet ./...
          go test ./...

      - uses: narwhl/actions/publish-go-module@latest # or pin to a commit SHA
```

### License

MPL-2.0, like the rest of this repository.
