resource "azsubcache_subscription" "sandbox" {
  display_name = "sandbox-${var.track_slug}"
}

output "subscription_id" {
  value = azsubcache_subscription.sandbox.subscription_id
}

# Hand the claimed subscription to the azurerm provider.
provider "azurerm" {
  features {}
  subscription_id = azsubcache_subscription.sandbox.subscription_id
}
