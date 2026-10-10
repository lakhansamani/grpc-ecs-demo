#!/usr/bin/env bash
# After `terraform destroy`, list the things that cost money.
#
# "Destroy complete" is not the same as "nothing is billing": a leftover RDS
# instance, a snapshot or an ECR repository full of images all keep charging.
set -uo pipefail

check() {
  local label="$1"; shift
  local out
  out=$("$@" 2>/dev/null)
  if [ -z "$out" ] || [ "$out" = "None" ]; then
    printf '  clean    %s\n' "$label"
  else
    printf '  LEFT     %s -> %s\n' "$label" "$out"
  fi
}

echo "in $(aws configure get region 2>/dev/null || echo "${AWS_REGION:-us-east-1}"):"
check "ECS clusters"        aws ecs list-clusters --query 'clusterArns' --output text
check "ECR repositories"    aws ecr describe-repositories --query 'repositories[].repositoryName' --output text
check "Cloud Map namespaces" aws servicediscovery list-namespaces --query 'Namespaces[].Name' --output text
check "Secrets"             aws secretsmanager list-secrets --query 'SecretList[].Name' --output text
check "RDS instances"       aws rds describe-db-instances --query 'DBInstances[].DBInstanceIdentifier' --output text
check "RDS snapshots"       aws rds describe-db-snapshots --snapshot-type manual --query 'DBSnapshots[].DBSnapshotIdentifier' --output text
check "project VPCs"        aws ec2 describe-vpcs --filters Name=tag:Project,Values=ecom --query 'Vpcs[].VpcId' --output text
check "project IAM roles"   aws iam list-roles --query 'Roles[?starts_with(RoleName,`ecom-`)].RoleName' --output text
check "NAT gateways"        aws ec2 describe-nat-gateways --filter Name=state,Values=available --query 'NatGateways[].NatGatewayId' --output text
check "load balancers"      aws elbv2 describe-load-balancers --query 'LoadBalancers[].LoadBalancerName' --output text

cat <<'NOTE'

Anything marked LEFT is still billing. Note this only checks ONE region -
re-run with AWS_REGION set if you ever deployed elsewhere.
NOTE
