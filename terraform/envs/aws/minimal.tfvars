# MINIMUM-PERMISSION profile.
#
# For an account with ECS + ECR + EC2 + iam:PassRole + logs only. Everything
# that needs a permission you may not have is switched off, and the demo still
# deploys and runs on real Fargate.
#
#   terraform -chdir=terraform/envs/aws apply -var-file=minimal.tfvars \
#     -var identity_image=... -var payment_image=...

# Reuse a role that already exists instead of creating one.
# Find yours:  aws iam list-roles --query 'Roles[?contains(RoleName,`ecsTaskExecution`)].Arn'
existing_execution_role_arn = "arn:aws:iam::ACCOUNT_ID:role/ecsTaskExecutionRole"

# JWT_SECRET as a plain env value. Still ONE shared value across all userd
# tasks, so scaling and the load-balancing demo still work.
use_secrets_manager = false

# Cloud Map also requires Route 53 permissions. With it off, orderd needs
# userd's address another way - see docs/AWS_PERMISSIONS.md.
# Leave TRUE if you can get servicediscovery + route53: it is the best segment.
enable_service_discovery = true

# Leave true unless logs:CreateLogGroup is refused. If false, the EXECUTION
# ROLE needs logs:CreateLogGroup, which the AWS managed policy does NOT include.
create_log_group = true
