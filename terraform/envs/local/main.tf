# LOCAL environment: the same stack, pointed at the Ministack emulator.
#
# Compare this file with ../aws/main.tf. They differ ONLY in the provider block.
# Everything that describes the deployment lives in ../stack, shared verbatim.

module "stack" {
  source = "../../stack"

  environment        = "local"
  aws_region         = "us-east-1"
  availability_zones = ["us-east-1a", "us-east-1b"]

  # Images already in the local Docker daemon. Ministack's ECS launches
  # containers through that daemon, so there is nothing to pull.
  identity_image = "localhost:4566/identityd:0.1.0"
  payment_image  = "localhost:4566/paymentd:0.1.0"

  identity_desired_count = 1
  payment_desired_count  = 1

  # jaeger from compose.yaml, reachable because Ministack places task
  # containers on the ecom-local network (DOCKER_NETWORK).
  otlp_endpoint = "jaeger:4317"

  # No Bedrock access anywhere in this demo. See SPEC.md 6.8.
  llm_provider = "template"

  # Ministack cannot create Cloud Map services via Terraform: it wants a
  # top-level NamespaceId, the provider nests it in DnsConfig. `make dns`
  # supplies the same DNS names with Docker network aliases instead.
  # On AWS this stays true and Cloud Map does it properly.
  enable_service_discovery = false
}

output "cluster_name" { value = module.stack.cluster_name }
output "namespace" { value = module.stack.namespace }
output "identity_dns" { value = module.stack.identity_dns }
output "payment_dns" { value = module.stack.payment_dns }
output "ecr_repository_urls" { value = module.stack.ecr_repository_urls }
