# terraform-provider-azsubcache

Hands out Azure subscriptions. Claims a pre-provisioned one from a pool
management group; creates a new one only when the pool is empty. Claiming moves
the subscription to another management group, so it can't be handed out twice.
Destroy cancels the subscription. Safe under parallel applies: a claim is a blob
created with `If-None-Match: *`, so exactly one apply can win a given
subscription.

```hcl
provider "azsubcache" {
  pool_management_group_id    = "mg-subscription-pool"
  claimed_management_group_id = "mg-sandboxes"
  claim_container_url         = "https://sandboxlocks.blob.core.windows.net/subscription-claims" # created on first claim
  billing_scope               = "/billingAccounts/1234567/enrollmentAccounts/890123" # optional; without it an empty pool is an error
  workload                    = "Production"                                         # optional, this is the default
}
```

Every setting falls back to an environment variable, so in CI the provider block
can be empty:

| setting | env var |
|---|---|
| `pool_management_group_id` | `AZSUBCACHE_POOL_MANAGEMENT_GROUP_ID` |
| `claimed_management_group_id` | `AZSUBCACHE_CLAIMED_MANAGEMENT_GROUP_ID` |
| `claim_container_url` | `AZSUBCACHE_CLAIM_CONTAINER_URL` |
| `billing_scope` | `AZSUBCACHE_BILLING_SCOPE` |
| `workload` | `AZSUBCACHE_WORKLOAD` |

```hcl
resource "azsubcache_subscription" "sandbox" {
  display_name = "sandbox-${var.track_slug}"
}

output "subscription_id" {
  value = azsubcache_subscription.sandbox.subscription_id
}
```

One blob per claimed subscription is left behind permanently — that is the
record of the claim, and claimed subscriptions never return to the pool. Nothing
to clean up.

Falling back to *creating* a subscription is not serialized: if the pool runs dry
under a burst, every apply creates its own subscription and you can hit Azure's
creation rate limit. Keep the pool stocked.

Auth is `DefaultAzureCredential` (env vars, workload identity, `az login`). The
identity needs Owner or Management Group Contributor on both management groups,
and the billing role to create subscriptions, plus Storage Blob Data Contributor
on the claim container (the container is created on first use if missing, so
double-check the URL: a typo gives you a private, empty lock namespace).

Refill the pool out of band with `examples/refill-pool.sh` (plain `az` CLI); the
provider never creates subscriptions while the pool has stock.

Docs under `docs/` are generated from the schema, `templates/` and `examples/` — run
`go generate ./...` after changing either. Releases are cut with
`goreleaser release --clean` (needs `GPG_FINGERPRINT` for the key registered
with the Terraform Registry).

Build and use locally:

```
go build -o ~/.terraform.d/plugins/registry.terraform.io/amochtar/azsubcache/0.1.0/darwin_arm64/terraform-provider-azsubcache
```

MIT licensed, see `LICENSE`.
