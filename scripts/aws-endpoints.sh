#!/usr/bin/env bash
# Print each service's PUBLIC IP:port on real AWS, ready to paste into grpcurl,
# curl or Postman.
#
# WHY THIS EXISTS: there is no load balancer and no domain in this deployment.
# Every task gets its own public IP on its own ENI (awsvpc networking), and
# that IP CHANGES whenever the task is replaced - a deploy, a scale event, or
# AWS retiring the Fargate platform version underneath it. So you re-run this
# after any of those rather than writing an address down.
#
# That is the honest trade-off of skipping an ALB: it costs nothing and needs no
# certificate, but you get N changing addresses instead of one stable name.
set -uo pipefail

CLUSTER="${CLUSTER:-ecom-aws}"
if [ "$#" -gt 0 ]; then
  SERVICES=("$@")
else
  SERVICES=(userd productsd orderd gatewayd)
fi

command -v aws >/dev/null || { echo "aws CLI not found" >&2; exit 1; }

printf '%-11s %-5s %-16s %s\n' SERVICE PORT "PUBLIC IP" "TRY THIS"
printf '%-11s %-5s %-16s %s\n' ------- ----- --------- --------

port_for() {
  case "$1" in
    userd) echo 50051 ;; orderd) echo 50052 ;;
    productsd) echo 50053 ;; gatewayd) echo 8080 ;;
    *) echo "" ;;
  esac
}

for svc in "${SERVICES[@]}"; do
  port=$(port_for "$svc")
  [ -z "$port" ] && { echo "unknown service $svc" >&2; continue; }

  # One service can have several tasks; print every one, because that is the
  # point of scaling out.
  arns=$(aws ecs list-tasks --cluster "$CLUSTER" --service-name "$svc" \
          --desired-status RUNNING --query 'taskArns' --output text 2>/dev/null)
  if [ -z "$arns" ] || [ "$arns" = "None" ]; then printf '%-11s %-5s %-16s %s\n' "$svc" "$port" "-" "no running task"; continue; fi

  for arn in $arns; do
    eni=$(aws ecs describe-tasks --cluster "$CLUSTER" --tasks "$arn" \
          --query 'tasks[0].attachments[0].details[?name==`networkInterfaceId`].value' \
          --output text 2>/dev/null)
    if [ -z "$eni" ] || [ "$eni" = "None" ]; then continue; fi
    ip=$(aws ec2 describe-network-interfaces --network-interface-ids "$eni" \
         --query 'NetworkInterfaces[0].Association.PublicIp' --output text 2>/dev/null)
    if [ "$ip" = "None" ] || [ -z "$ip" ]; then ip="(no public IP)"; fi

    if [ "$svc" = "gatewayd" ]; then
      hint="curl http://$ip:$port/healthz"
    else
      hint="grpcurl -plaintext $ip:$port list"
    fi
    printf '%-11s %-5s %-16s %s\n' "$svc" "$port" "$ip" "$hint"
  done
done

cat <<'NOTE'

Reminder: these only answer if your current egress IP is in
operator_ingress_cidrs. Check it with `curl ifconfig.me` FROM THE VENUE - the
conference NAT is probably not the IP you allow-listed from home. It must be
IPv4: an IPv6 address in a cidr_ipv4 rule fails the apply.
NOTE
