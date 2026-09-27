// Terraform provider that hands out Azure subscriptions: claim a pre-provisioned
// one from a pool management group, or create one when the pool is empty.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/instruqt/terraform-provider-azsubcache/internal/provider"
)

//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name azsubcache

// version is set by the release build: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/instruqt/azsubcache",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
