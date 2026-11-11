// Command terraform-provider-viya is the Terraform provider for SAS Viya.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/jacoblamarche/terraform-provider-viya/internal/provider"
)

// version is set at build time with -ldflags.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers such as Delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/jacoblamarche/viya",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
