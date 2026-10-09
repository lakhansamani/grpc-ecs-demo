# LOCAL environment: the same stack, pointed at the Ministack emulator.
#
# Compare with ../aws/main.tf. They differ ONLY in the provider block.
# Everything describing the deployment lives in ../stack, shared verbatim.

# Exposed so the guard is demonstrable:
#   terraform plan -var order_desired_count=3   -> refused, with the reason
variable "order_desired_count" {
  type    = number
  default = 1
}
variable "user_desired_count" {
  type    = number
  default = 1
}
variable "product_desired_count" {
  type    = number
  default = 1
}

module "stack" {
  source = "../../stack"

  environment        = "local"
  aws_region         = "us-east-1"
  availability_zones = ["us-east-1a", "us-east-1b"]

  # Images already in the local Docker daemon. Ministack's ECS launches
  # containers through that daemon, so there is nothing to pull.
  user_image    = "localhost:4566/userd:0.1.0"
  product_image = "localhost:4566/productsd:0.1.0"
  order_image   = "localhost:4566/orderd:0.1.0"
  gateway_image = "localhost:4566/gatewayd:0.1.0"

  user_desired_count    = var.user_desired_count
  product_desired_count = var.product_desired_count
  order_desired_count   = var.order_desired_count
  gateway_desired_count = 1

  # jaeger from compose.yaml, reachable because Ministack places task
  # containers on the ecom-local network (DOCKER_NETWORK).
  otlp_endpoint = "jaeger:4317"

  # Ministack cannot create Cloud Map services through Terraform: it wants a
  # top-level NamespaceId while the provider nests it in DnsConfig. `make dns`
  # supplies the same DNS names with Docker network aliases instead. On AWS
  # this stays true and Cloud Map does it properly.
  enable_service_discovery = false
}

output "cluster_name" { value = module.stack.cluster_name }
output "namespace" { value = module.stack.namespace }
output "service_addresses" { value = module.stack.service_addresses }
output "ecr_repository_urls" { value = module.stack.ecr_repository_urls }
