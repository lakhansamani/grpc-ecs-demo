# Two roles, and the distinction matters on stage:
#
#   EXECUTION role - used by the ECS agent BEFORE your code runs: pull the
#                    image, resolve `secrets` from Secrets Manager, create log
#                    streams. This is what makes JWT_SECRET injection work.
#   TASK role      - the credentials YOUR process gets. Empty here, because
#                    the deployment calls no AWS APIs at runtime (the
#                    explanation provider is the template, not Bedrock).
#
# Keeping the task role empty is the point: no API keys anywhere, and no
# permissions granted "just in case".

variable "name_prefix" { type = string }

# When set, no roles are created and this ARN is used as-is. For accounts where
# iam:CreateRole is not granted but a role already exists (most accounts that
# have ever used ECS have `ecsTaskExecutionRole`). iam:PassRole is still
# required - Fargate cannot start a task without passing an execution role.
variable "existing_execution_role_arn" {
  type    = string
  default = ""
}
variable "secret_arns" {
  type    = list(string)
  default = []
}
variable "tags" {
  type    = map(string)
  default = {}
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

locals {
  create_roles = var.existing_execution_role_arn == ""
}

resource "aws_iam_role" "execution" {
  count              = local.create_roles ? 1 : 0
  name               = "${var.name_prefix}-execution"
  assume_role_policy = data.aws_iam_policy_document.assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "execution_managed" {
  count      = local.create_roles ? 1 : 0
  role       = aws_iam_role.execution[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# Scoped to the exact secrets this deployment injects, not secretsmanager:*.
data "aws_iam_policy_document" "read_secrets" {
  count = length(var.secret_arns) == 0 ? 0 : 1
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = var.secret_arns
  }
}

resource "aws_iam_role_policy" "execution_secrets" {
  count  = local.create_roles && length(var.secret_arns) > 0 ? 1 : 0
  name   = "read-injected-secrets"
  role   = aws_iam_role.execution[0].id
  policy = data.aws_iam_policy_document.read_secrets[0].json
}

resource "aws_iam_role" "task" {
  count              = local.create_roles ? 1 : 0
  name               = "${var.name_prefix}-task"
  assume_role_policy = data.aws_iam_policy_document.assume.json
  tags               = var.tags
}

output "execution_role_arn" {
  value = local.create_roles ? aws_iam_role.execution[0].arn : var.existing_execution_role_arn
}

# Falls back to the execution role when roles are not being created. The task
# role is empty anyway: the services call no AWS API at runtime.
output "task_role_arn" {
  value = local.create_roles ? aws_iam_role.task[0].arn : var.existing_execution_role_arn
}
