#!/usr/bin/env bash
# Top the pool back up to TARGET subscriptions. Run it from cron or a scheduled
# pipeline: creating subscriptions is rate limited, which is the whole reason
# Terraform claims from a pool instead of creating on demand.
set -euo pipefail

POOL_MG=${AZSUBCACHE_POOL_MANAGEMENT_GROUP_ID:?}
BILLING_SCOPE=${AZSUBCACHE_BILLING_SCOPE:?}
TARGET=${TARGET:-10}

have=$(az account management-group subscription show-sub-under-mg \
  --name "$POOL_MG" --query 'length(@)' -o tsv)
echo "pool $POOL_MG holds $have of $TARGET subscriptions"

for ((i = have; i < TARGET; i++)); do
  alias_name="pool-$(uuidgen | tr '[:upper:]' '[:lower:]')"

  # The name does not matter: the provider renames the subscription to the
  # resource's display_name when it claims it.
  subscription_id=$(az account alias create \
    --name "$alias_name" \
    --billing-scope "$BILLING_SCOPE" \
    --display-name "unclaimed" \
    --workload Production \
    --query 'properties.subscriptionId' -o tsv)

  # Putting it in the pool management group is what offers it to Terraform,
  # so do it last.
  az account management-group subscription add \
    --name "$POOL_MG" --subscription "$subscription_id"

  # The alias is only a creation receipt; deleting it leaves the subscription.
  az account alias delete --name "$alias_name"

  echo "added $subscription_id to $POOL_MG"
done
