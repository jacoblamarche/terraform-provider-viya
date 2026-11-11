# A definition with several named instances per service: the autoexec code
# that every SAS Compute session runs.
resource "viya_configuration" "compute_autoexec" {
  definition = "sas.compute.server"
  services   = ["compute"]
  name       = "autoexec_code"

  properties = {
    contents = <<-EOT
      options fullstimer;
      libname shared "/mnt/shared";
    EOT
  }
}

# A definition with a single instance and a secret: the LDAP connection of
# the Identities service. Only the listed properties are managed.
variable "ldap_bind_password" {
  type      = string
  sensitive = true
}

resource "viya_configuration" "ldap_connection" {
  definition = "sas.identities.providers.ldap.connection"
  services   = ["identities"]

  properties = {
    host = "ldap.example.com"
    port = 636
  }

  sensitive_properties = {
    password = var.ldap_bind_password
  }
}
