# viyamock

An in-memory fake of the SAS Viya REST APIs that the provider uses. It lets the
Terraform acceptance tests run without a Viya deployment.

It is a *fake*, not a stub: it keeps state, so a resource that Terraform
creates can be read back, updated, imported and deleted.

## What it implements

| Service | Endpoint | Notes |
| --- | --- | --- |
| SAS Logon | `POST /SASLogon/oauth/token` | `client_credentials` and `password` grants; client authenticated with HTTP Basic or form fields |
| Identities | `POST /identities/groups` | |
| Identities | `GET`, `PUT`, `DELETE /identities/groups/{id}` | `ETag` on reads and writes; `If-Match` on `PUT` |
| Configuration | `GET /configuration/definitions/{name}` | Name, version and properties of a definition |
| Configuration | `GET`, `POST /configuration/configurations` | Filtered with `definitionName`; paginated with `start` and `limit` |
| Configuration | `GET`, `PUT`, `DELETE /configuration/configurations/{id}` | The `Content-Type` names the definition; secrets are masked on reads |

The fake starts with three configuration definitions and the default instances
that services would have written: `sas.compute.server` (three named instances),
`sas.identities.providers.ldap.connection` (one instance, with a secret
`password`) and `logging.level` (no instance).

Conventions it reproduces:

- Bearer tokens that expire; a missing, unknown or expired token gets HTTP 401.
- Entity tags: `PUT` with a stale `If-Match` gets HTTP 412.
- SAS media types (`application/vnd.sas.identity.group+json`,
  `application/vnd.sas.collection+json`, `application/vnd.sas.error+json`).
- Collections with `start`, `limit`, `count`, `items` and `links`.

Anything else gets HTTP 404, and query parameters that are not implemented
(`filter`, `sortBy`) get HTTP 400. The fake fails loudly rather than ignore a
request it does not understand, so that a test cannot pass by accident.

## How its behaviour was established

A fake is only useful if it behaves like the real service. Each behaviour
below is marked with where it comes from.

| Behaviour | Source | Confidence |
| --- | --- | --- |
| Token endpoint: grants, parameters, response fields | [SAS Logon OpenAPI specification](https://developer.sas.com/rest-apis/SASLogon) | Documented |
| Group paths and the `id`/`name`/`description`/`state` body | [pyviyatools](https://github.com/sassoftware/pyviyatools), SAS's own administration scripts | Observed in SAS code |
| Media types, `ETag`/`If-Match` on groups, status codes 409/412/428, error body fields | Conventions shared by Viya services; not in a published specification for this endpoint | **To verify against a real deployment** |
| Configuration paths, the `definitionName` filter, properties as top-level fields, `metadata.mediaType`/`services`/`isDefault`, the definition-specific `Content-Type` on `PUT` | [pyviyatools](https://github.com/sassoftware/pyviyatools) | Observed in SAS code |
| Meaning of `isDefault` (a service overwrites a default instance at startup) | [SAS blog on saving and reloading configuration](https://blogs.sas.com/content/sgf/2018/06/08/saving-and-reloading-sas-viya-configuration) | Documented |
| `POST` to create one instance, `GET /configuration/definitions/{name}` and its `version`, `ETag`/`If-Match` on configurations, a `PUT` that replaces every property | Recollection of the Configuration API; no published specification | **To verify against a real deployment** |
| Secrets returned masked, and a masked value written back leaving the secret unchanged; the mask itself (`********`) | Inferred from how SAS Environment Manager edits such instances | **To verify against a real deployment** |

Neither the custom group endpoints (`/identities/groups`) nor the Configuration
service are described by an OpenAPI specification that SAS currently publishes.
Until the acceptance tests have run against a real deployment, treat the rows
marked "to verify" as assumptions.

The secrets row matters most. The provider updates an instance by reading it,
changing the managed properties and writing all of it back. If a real
deployment did not treat a masked secret as unchanged, an update would replace
every secret that Terraform does not manage with its mask. Check this on a
non-production deployment before managing an instance that holds a secret.

By default the fake checks `If-Match` only when the header is present. The
`WithRequireIfMatch` option makes it mandatory (HTTP 428), which is the
stricter of the two plausible behaviours; the provider is tested against both.

## Using it in a test

```go
mock := viyamock.New()
server := mock.Start()
defer server.Close()

// server.URL is the endpoint; the credentials are viyamock.DefaultClientID
// and viyamock.DefaultClientSecret.
```

Tests can also reach behind the API:

- `InjectFault` makes the next matching requests fail with a given status, to
  exercise retries.
- `RevokeTokens` invalidates every token, to exercise re-authentication.
- `RemoveGroup` deletes a group as if someone had done it outside Terraform.
- `AddConfiguration`, `SetConfigurationProperty` and `RemoveConfiguration`
  change configuration instances behind the API; `Configuration` and
  `FindConfigurations` read them with their secrets unmasked.
- `Requests` returns the requests received, to assert on what the client sent.

## Keeping it honest

When a difference is found between the fake and a real deployment:

1. Reproduce the real behaviour in the fake, with a test in `server_test.go`.
2. Then fix the client or the provider.
3. Update the table above.
