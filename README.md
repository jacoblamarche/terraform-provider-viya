# Terraform Provider for SAS Viya

Manage the configuration of a [SAS Viya](https://www.sas.com/en_us/software/viya.html)
deployment as code, with [Terraform](https://developer.hashicorp.com/terraform).

> **Status: early development.** The provider is not published to the Terraform
> Registry yet, and its resources may change without notice.

> **Licence: not granted yet.** The licence of this project is being finalised.
> Until a `LICENSE` file is published, no right to use, copy, modify or
> distribute this code is granted.

This project is not affiliated with, endorsed by, or sponsored by SAS Institute
Inc. SAS and SAS Viya are trademarks of SAS Institute Inc. Terraform is a
trademark of HashiCorp, Inc.

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
| `make docs` | Regenerates `docs/` from the schemas and `examples/` |
| `make mock` | Serves the fake Viya deployment on `http://127.0.0.1:8080` |
