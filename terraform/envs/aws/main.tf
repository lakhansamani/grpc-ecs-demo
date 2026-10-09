# AWS environment: the same stack, against the real thing.

variable "user_image" {
  type        = string
  description = "ECR image URI, e.g. 123456789012.dkr.ecr.ap-south-1.amazonaws.com/userd:0.1.0"
}
variable "product_image" { type = string }
variable "order_image" { type = string }
variable "gateway_image" { type = string }
variable "operator_ingress_cidrs" {
  type        = list(string)
  default     = []
  description = "Your public IP as a /32 so the demo client can reach paymentd. Keep this tight."
}
# Region lives here so you can switch without editing the stack. Fargate ARM64
# is available in all commercial regions, so any of them works.
variable "aws_region" {
  type    = string
  default = "ap-south-1"
}

variable "user_desired_count" {
  type    = number
  default = 1
}
variable "product_desired_count" {
  type    = number
  default = 1
}
variable "gateway_desired_count" {
  type    = number
  default = 1
}

# ---- permission fallbacks, see docs/AWS_PERMISSIONS.md and minimal.tfvars ----
variable "existing_execution_role_arn" {
  type    = string
  default = ""
}
variable "use_secrets_manager" {
  type    = bool
  default = true
}
variable "enable_service_discovery" {
  type    = bool
  default = true
}
variable "create_log_group" {
  type    = bool
  default = true
}
variable "enable_execute_command" {
  type    = bool
  default = true
}

module "stack" {
  source = "../../stack"

  environment        = "aws"
  aws_region         = var.aws_region
  availability_zones = ["${var.aws_region}a", "${var.aws_region}b"]

  user_image    = var.user_image
  product_image = var.product_image
  order_image   = var.order_image
  gateway_image = var.gateway_image

  user_desired_count    = var.user_desired_count
  product_desired_count = var.product_desired_count
  gateway_desired_count = var.gateway_desired_count
  order_desired_count   = 1

  operator_ingress_cidrs = var.operator_ingress_cidrs

  # No ADOT collector deployed, so tracing is disabled rather than failing to
  # export on every span.
  otlp_endpoint = ""

  # No Bedrock access on this deployment. See SPEC.md 6.8.
  llm_provider = "template"

  existing_execution_role_arn = var.existing_execution_role_arn
  use_secrets_manager         = var.use_secrets_manager
  enable_service_discovery    = var.enable_service_discovery
  create_log_group            = var.create_log_group
  enable_execute_command      = var.enable_execute_command
}

output "cluster_name" { value = module.stack.cluster_name }
output "namespace" { value = module.stack.namespace }
output "service_addresses" { value = module.stack.service_addresses }
output "ecr_repository_urls" { value = module.stack.ecr_repository_urls }
output "jwt_secret_name" { value = module.stack.jwt_secret_name }
