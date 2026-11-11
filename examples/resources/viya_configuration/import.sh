# A configuration instance is imported with the ID that SAS Viya assigned
# to it. To find it:
#
#   sas-viya configuration configurations list --definition-name sas.compute.server
#
# An imported instance is treated as adopted: destroying the resource leaves
# it in place.
terraform import viya_configuration.compute_autoexec 0f4b2c1e-8d6a-4e57-9c3b-2a1d5e7f9b60
