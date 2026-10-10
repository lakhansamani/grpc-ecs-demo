# LOCAL environment: the same stack, pointed at the Ministack emulator.
#
# Compare with ../aws/main.tf. They differ ONLY in the provider block.
# Everything describing the deployment lives in ../../deployment, unchanged.

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

# `-var lb_policy=pick_first` redeploys orderd with the bug, so the
# load-balancing segment has a "before" to show. See ../../deployment/main.tf.
# ON by default locally. The emulator runs a plain postgres container, so it
# costs nothing, and it means the local demo exercises the SAME shape as the
# AWS one: one instance, a separate database per service, DSNs injected from
# Secrets Manager.
#
# Set -var use_rds=false for the SQLite shape, which is faster to stand up and
# is what the "orderd cannot scale" guard is about.
variable "use_rds" {
  type    = bool
  default = true
}

variable "lb_policy" {
  type    = string
  default = "round_robin"
}

module "deployment" {
  source = "../../deployment"

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
  lb_policy             = var.lb_policy
  use_rds               = var.use_rds
  # The emulator's postgres container speaks no TLS.
  db_sslmode            = "disable"
  gateway_desired_count = 1

  # jaeger from compose.yaml, reachable because Ministack places task
  # containers on the ecom-infra network (DOCKER_NETWORK).
  otlp_endpoint = "jaeger:4317"

  # Ministack cannot create Cloud Map services through Terraform: it wants a
  # top-level NamespaceId while the provider nests it in DnsConfig. `make dns`
  # supplies the same DNS names with Docker network aliases instead. On AWS
  # this stays true and Cloud Map does it properly.
  enable_service_discovery = false
}

output "cluster_name" { value = module.deployment.cluster_name }
output "namespace" { value = module.deployment.namespace }
output "service_addresses" { value = module.deployment.service_addresses }
output "ecr_repository_urls" { value = module.deployment.ecr_repository_urls }
