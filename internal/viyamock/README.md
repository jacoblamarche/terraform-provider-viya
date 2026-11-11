# viyamock

An in-memory fake of the SAS Viya REST APIs that the provider uses. It lets the
Terraform acceptance tests run without a Viya deployment.

It is a *fake*, not a stub: it keeps state, so a resource that Terraform
creates can be read back, updated, imported and deleted.

## What it implements

| Service | Endpoint | Notes |
| --- | --- | --- |
| SAS Logon | `POST /SASLogon/oauth/token` | `client_credentials` grant; client authenticated with HTTP Basic or form fields |
| Identities | `POST /identities/groups` | |
| Identities | `GET`, `PUT`, `DELETE /identities/groups/{id}` | No entity tag; `If-Match` is ignored |
| Configuration | `GET /configuration/definitions` | Every definition, with a link that names the media type of its instances; `filter` is ignored |
| Configuration | `GET`, `POST /configuration/configurations` | Filtered with `definitionName`; paginated with `start` and `limit` |
| Configuration | `GET`, `PUT`, `DELETE /configuration/configurations/{id}` | The `Content-Type` names the definition; weak entity tags |

The fake starts with three configuration definitions and the default instances
that services would have written: `sas.compute.server` (three named instances),
`sas.identities.providers.ldap.connection` (one instance, with a `password`)
and `logging.level` (no instance).

Conventions it reproduces:

- Bearer tokens that expire; a missing, unknown or expired token gets HTTP 401.
- SAS media types (`application/vnd.sas.identity.group+json`,
  `application/vnd.sas.collection+json`, `application/vnd.sas.error+json`).
- Collections with `start`, `limit`, `count`, `items` and `links`.

Anything else gets HTTP 404, and query parameters that are not implemented get
HTTP 400. The fake fails loudly rather than ignore a request it does not
understand, so that a test cannot pass by accident. The one exception is
`filter` on the definition list, which SAS Viya ignores too.

## How its behaviour was established

A fake is only useful if it behaves like the real service. Each behaviour
below is marked as verified against SAS Viya, documented by SAS, or still to
verify.

| Behaviour | Status |
| --- | --- |
| Token endpoint: grants, parameters, response fields ([SAS Logon specification](https://developer.sas.com/rest-apis/SASLogon)) | Documented |
| Groups: `POST` → 201 with `Location`; `GET` → 200 with `application/vnd.sas.identity.group+json`; `PUT` → 200; `POST` of an existing ID → 409; `DELETE` → 204, then `GET` → 404 | Verified |
| Groups: no `ETag` and no `Last-Modified`, on creation or on read | Verified |
| Groups: the `id`/`name`/`description`/`state` body | Verified |
| Definitions: `GET /configuration/definitions/{name}` → 404; the list ignores `filter`; a link of each definition names its instance media type, with its version | Verified |
| Configurations: `POST` of a single instance → 201 with a weak `ETag` and `Location` | Verified |
| Configurations: `PUT` with the weak `ETag` in `If-Match` → 200 with the instance and a new `ETag`; a stale one → 412; no `If-Match` → 204 without body | Verified |
| Configurations: an instance read and sent back as is, with `id`, `metadata` and `links`, is accepted | Verified |
| Configurations: the password of the LDAP connection is returned in clear text | Verified |
| Configurations: `DELETE` → 204, then `GET` → 404 | Verified |
| Configurations: properties as top-level fields, `metadata.mediaType`/`services`/`isDefault`, the `definitionName` filter | Verified |
| Meaning of `isDefault`: a service overwrites a default instance at startup ([SAS blog](https://blogs.sas.com/content/sgf/2018/06/08/saving-and-reloading-sas-viya-configuration)) | Documented |
| Error body fields, retries on 429 and 503 | **To verify** |

Neither the custom group endpoints (`/identities/groups`) nor the Configuration
service are described by an OpenAPI specification that SAS currently publishes.

Only the LDAP connection is verified to return its password in clear text. If
another definition returned a secret masked, the provider would write the mask
back when it updates that instance, and report a difference on every plan for a
secret in `sensitive_properties`.

SAS Viya accepts a configuration update without `If-Match`. The
`WithRequireIfMatch` option makes the fake refuse it (HTTP 428), to prove that
the provider always sends the precondition.

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
  `FindConfigurations` read them.
- `Requests` returns the requests received, to assert on what the client sent.

## Keeping it honest

When a difference is found between the fake and a real deployment:

1. Reproduce the real behaviour in the fake, with a test in `server_test.go`.
2. Then fix the client or the provider.
3. Update the table above.
