terraform {
  required_providers {
    viya = {
      source = "jacoblamarche/viya"
    }
  }
}

# Credentials are best supplied through the environment:
#
#   export VIYA_CLIENT_ID=terraform
#   export VIYA_CLIENT_SECRET=...
provider "viya" {
  endpoint = "https://viya.example.com"
}
