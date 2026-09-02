This is a real, runnable app and verify it compiles against 
the actual SDK.Compiles clean (`go vet` + `go build`) against oci-go-sdk v65.81.3.

```
go mod tidy && go build -o oci.get.ca .
./oci.get.ca                       # tenancy root compartment
./oci.get.ca -all -state ACTIVE -v # every accessible compartment, active only
./oci.get.ca -compartment ocid1.compartment.oc1..aaa -json
```

A few things worth flagging:

**`ListCertificateAuthorities` has no `compartmentIdInSubtree`.** It only filters by a single `compartmentId`, so "all available CAs" in a tenancy means enumerating compartments via `identity.ListCompartments` (subtree + `ACCESSIBLE`) and querying each — that's what `-all` does, with bounded fan-out and per-compartment 401/403/404 tolerance so one unreadable compartment doesn't abort the scan.

**Policies** the instance's dynamic group needs:

```
allow dynamic-group <dg> to read certificate-authorities in tenancy
allow dynamic-group <dg> to inspect compartments in tenancy   # only for -all
```

**Struct field gotcha:** the summary field is `CurrentVersionSummary`, not `CurrentVersion` (the *full* `CertificateAuthority` model from `GetCertificateAuthority` uses `CurrentVersion`). Expiry lives at `CurrentVersionSummary.Validity.TimeOfValidityNotAfter`.

**Auth notes:** `auth.InstancePrincipalConfigurationProvider()` hits IMDS at 169.254.169.254, pulls the instance leaf cert/key, and exchanges them for an RPST it refreshes internally — so the provider is long-lived and safe to share across clients. `InstancePrincipalConfigurationProviderForRegion` is used when `-region` is set, since the federation endpoint is region-scoped; that matters if you're calling a region other than the one the instance lives in. Region source of truth is `provider.Region()` (a `string`, not `common.Region` — easy to trip on).

`Certificates Management` here is the control plane. If you actually want to *retrieve* the CA bundle/PEM at runtime, that's the separate `certificates` data-plane client (`GetCertificateAuthorityBundle`) — say the word and I'll add that path.
