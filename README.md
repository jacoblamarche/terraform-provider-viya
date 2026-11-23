# Terraform Provider for SAS Viya

Manage the configuration of a [SAS Viya](https://www.sas.com/en_us/software/viya.html)
deployment as code, with [Terraform](https://developer.hashicorp.com/terraform).

> **Status: early development.** The provider is not published to the Terraform
> Registry yet, and its resources may change without notice.

> **Licence:** this project is distributed under the National Bank of Canada
> License, version 1.0. See [`LICENSE`](LICENSE).

This project is not affiliated with, endorsed by, or sponsored by SAS Institute
Inc. SAS and SAS Viya are trademarks of SAS Institute Inc. Terraform is a
trademark of HashiCorp, Inc.

## Resources

| Resource | Manages |
| --- | --- |
| [`viya_configuration`](docs/resources/configuration.md) | An instance of a configuration definition, such as the LDAP connection or the Compute autoexec |
| [`viya_custom_group`](docs/resources/custom_group.md) | A custom group of the Identities service |

## Compatibility

The provider is tested in continuous integration with:

- [Terraform](https://developer.hashicorp.com/terraform) 1.5 and the latest release;
- the latest release of [OpenTofu](https://opentofu.org/).

## Usage

```terraform
terraform {
  required_providers {
    viya = {
      source = "jacoblamarche/viya"
    }
  }
}

provider "viya" {
  endpoint = "https://viya.example.com"
}

resource "viya_custom_group" "data_scientists" {
  id          = "DataScientists"
  name        = "Data Scientists"
  description = "Builds and validates models."
}
```

### Authentication

The provider authenticates against SAS Logon Manager. Supply credentials
through the environment rather than in configuration files.

| Method | Arguments | Environment variables |
| --- | --- | --- |
| Client credentials (recommended for automation) | `client_id`, `client_secret` | `VIYA_CLIENT_ID`, `VIYA_CLIENT_SECRET` |
| Access token obtained out of band (not renewed) | `access_token` | `VIYA_ACCESS_TOKEN` |

The endpoint can be set with `VIYA_ENDPOINT`, and must use HTTPS. Supplying
both an access token and a client is an error. See the
[provider documentation](docs/index.md) for details.

## Development

Requirements: [Go](https://go.dev/doc/install) 1.27 or later,
[Terraform](https://developer.hashicorp.com/terraform/install) 1.5 or later (or
[OpenTofu](https://opentofu.org/docs/intro/install/)), and
[golangci-lint](https://golangci-lint.run/) 2.

| Command | Does |
| --- | --- |
| `make build` | Compiles every package |
| `make test` | Runs the unit tests |
| `make testacc` | Runs every test, including the Terraform acceptance tests |
| `make lint` | Runs the linters |
| `make vulncheck` | Reports known vulnerabilities in the code and its dependencies |
| `make docs` | Regenerates `docs/` from the schemas and `examples/` |
| `make mock` | Serves the fake Viya deployment on `http://127.0.0.1:8080` |

### Releasing

1. In `CHANGELOG.md`, rename the `[Unreleased]` section to the new version and
   date, for example `## [0.1.0] - 2026-11-12`, and add an empty
   `[Unreleased]` section above it. Commit this on `main`.
2. Tag that commit and push the tag:

   ```shell
   git tag v0.1.0
   git push origin v0.1.0
   ```

The release workflow then checks that the tag is on `main` and that
`CHANGELOG.md` has a section for the version, runs the tests, and publishes a
GitHub release with signed binaries for Linux, Windows and macOS on amd64 and
arm64. It needs two repository secrets: `GPG_PRIVATE_KEY`, the armored private
key that signs the checksums, and `PASSPHRASE`, its passphrase.

### Testing without a Viya deployment

The acceptance tests run real Terraform plans and applies against
[`viyamock`](internal/viyamock/README.md), an in-memory fake of the Viya REST
APIs that lives in this repository. No Viya deployment and no credentials are
needed, so the full suite runs on every pull request.

To run the suite with OpenTofu instead of Terraform:

```shell
export TF_ACC_TERRAFORM_PATH="$(command -v tofu)"
export TF_ACC_PROVIDER_NAMESPACE=hashicorp
export TF_ACC_PROVIDER_HOST=registry.opentofu.org
make testacc
```

### Testing against a real Viya deployment

The same suite runs against a real deployment when `VIYA_ENDPOINT` is set. It
creates and deletes groups whose ID starts with `tfacc-`, and `logging.level`
configuration instances for loggers whose name starts with `tfacc.`. The tests
that rewrite existing settings, such as the LDAP connection, only run against
the fake.

```shell
export VIYA_ENDPOINT=https://viya.example.com
export VIYA_CLIENT_ID=...
export VIYA_CLIENT_SECRET=...
make testacc
```

The fake only stays faithful if it is checked against the real service: run the
suite against a real deployment before each release.

### Trying the provider by hand

1. Start the fake deployment: `make mock`.
2. Install the provider binary: `make install`.
3. Tell Terraform to use that binary, in `~/.terraformrc`:

   ```hcl
   provider_installation {
     dev_overrides {
       "jacoblamarche/viya" = "/path/to/your/go/bin"
     }
     direct {}
   }
   ```

4. In a directory with a configuration, point the provider at the fake and
   apply. `terraform init` is not needed with `dev_overrides`.

   ```shell
   export VIYA_ENDPOINT=http://127.0.0.1:8080
   export VIYA_CLIENT_ID=viya-mock-client
   export VIYA_CLIENT_SECRET=viya-mock-secret
   terraform apply
   ```
