terraform {
  required_providers {
    azsubcache = {
      source = "instruqt/azsubcache"
    }
  }
}

# Every setting also reads from AZSUBCACHE_*, so this block can be empty in CI.
provider "azsubcache" {
  pool_management_group_id    = "mg-subscription-pool"
  claimed_management_group_id = "mg-sandboxes"
  claim_container_url         = "https://sandboxlocks.blob.core.windows.net/subscription-claims"

  # Only needed to create a subscription when the pool is empty.
  billing_scope = "/billingAccounts/1234567/enrollmentAccounts/890123"
}
