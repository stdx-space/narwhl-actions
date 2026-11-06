# narwhl/actions

A collection of GitHub Actions for keyless authentication, HashiCorp tooling, and publishing packages to narwhl's registry (Go modules at `pkg.narwhl.dev`).

Every push force-updates the `latest` tag (see [`.github/workflows/tag.yml`](.github/workflows/tag.yml)), so any action below can be referenced as `@latest`. Note this tag is mutable — pin to a commit SHA if you need stable behavior.

## Actions

### [setup-consul](setup-consul)

Installs the [Consul](https://developer.hashicorp.com/consul) CLI in your workflow, resolving the requested version from [releases.hashicorp.com](https://releases.hashicorp.com/consul) and adding the binary to `PATH`.

```yml
steps:
- uses: narwhl/actions/setup-consul@latest
  with:
    consul_version: "1.18.2"
```

### [consul-action](consul-action)

Authenticates to Consul with an existing ACL token or a caller-supplied JWT, then reads Consul KV values into masked action outputs and environment variables. The `secrets` input follows the Vault Action mapping style, adapted to Consul's single value per KV key:

```text
{{ KV key }} | {{ Env/output variable name }}
```

Requests are separated by semicolons. When the mapped name is omitted, the final key segment is normalized automatically.

```yml
steps:
- uses: narwhl/actions/consul-action@latest
  id: consul
  with:
    url: https://consul.example.com
    namespace: payments
    method: jwt
    authMethod: github-actions
    jwt: ${{ steps.github-oidc.outputs.token }}
    secrets: |
      apps/api/database-password | DB_PASSWORD ;
      apps/api/npm-token | NPM_TOKEN
```

Supported authentication strategies:

- `token` (default): provide `token` with an existing Consul ACL token.
- `jwt`: provide `authMethod` and `jwt`; the caller-supplied JWT is exchanged through `/v1/acl/login`.

The action does not mint GitHub OIDC tokens. A GitHub-issued token can be supplied through the `jwt` input. Consul's browser-interactive `oidc` auth method is not supported. ACL tokens created by `jwt` authentication are destroyed by the action's post step.

Set the optional `namespace` input for Consul Enterprise. The action sends it as `X-Consul-Namespace` during ACL login, KV retrieval, and token cleanup. When `exportToken` is enabled, the same value is also exported as `CONSUL_NAMESPACE`.

### [setup-terraform](setup-terraform)

Wraps [`hashicorp/setup-terraform`](https://github.com/hashicorp/setup-terraform) and authenticates automatically against a custom HTTP state backend (defaults to `terraform.narwhl.workers.dev`, overridable via `endpoint`), so no `cli_config_credentials_token` is required. Pass `use_federated_token: true` to authenticate with a token issued by [`imprint`](imprint), valid 1 hour instead of GitHub's ~5 minutes.

```yml
steps:
- uses: narwhl/actions/setup-terraform@latest
  with:
    state: my-state
    terraform_version: "1.13.0"
```

### [imprint](imprint)

Authenticates the runner to a Security Token Service via its OIDC ID token and exchanges it for short-lived resource credentials, exposed as environment variables for subsequent steps.

```yml
steps:
- uses: narwhl/actions/imprint@latest
  with:
    scope: tailscale cloudflare
```

### [connect-tailscale](connect-tailscale)

Connects the runner to a Tailscale tailnet, forked from [`tailscale/github-action`](https://github.com/tailscale/github-action) (BSD-3-Clause). Adds an `id-token` input so the node can authenticate with a federated OIDC token minted by any provider the tailnet trusts — for example one from [`imprint`](imprint), valid 1 hour instead of GitHub's ~5-minute job token — instead of being constrained to the runner-issued one:

```yml
steps:
- uses: narwhl/actions/imprint@latest
  with:
    scope: tailscale

- uses: narwhl/actions/connect-tailscale@latest
  with:
    oauth-client-id: ${{ vars.TAILSCALE_CLIENT_ID }}
    tags: tag:ci
    id-token: ${{ env.FEDERATED_TOKEN }}
```

Without `id-token`, behavior is identical to upstream: runner-requested workload identity (`audience` + `oauth-client-id`), OAuth client (`oauth-secret`), or a classic auth key.

### [publish-go-module](publish-go-module)

Publishes the Go module named by a pushed release tag (`go/<name>/vX.Y.Z`) to `pkg.narwhl.dev`, so it installs as `go get pkg.narwhl.dev/<name>`. The upload is authorized by the job's GitHub OIDC token, so no secrets are needed. The action's README covers the naming and tag rules and has a full release workflow.

```yml
on:
  push:
    tags: ["go/*/v*"]

permissions:
  contents: read
  id-token: write

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
    - uses: actions/checkout@v7
    - uses: narwhl/actions/publish-go-module@latest
```

The same program runs outside Actions: `go install github.com/narwhl/actions/publish-go-module@latest`.

## License

[MPL-2.0](https://www.mozilla.org/en-US/MPL/2.0/)

`connect-tailscale` retains its upstream [BSD-3-Clause license](connect-tailscale/LICENSE); everything else in this repository is MPL-2.0.
