# The only interface you type on stage.
SHELL := /bin/bash
TF    := terraform -chdir=terraform/envs/local

# The emulator accepts any credentials, but the AWS CLI refuses to send a
# request without some. Prefixed onto every local `aws` call below so these
# targets work in a shell that has never been configured for AWS.
LOCAL_AWS := AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1 \
             aws --endpoint-url http://localhost:4566

.PHONY: local-up local-down tf-local-apply tf-local-destroy dns demo demo-load wait-ready \
        scale test ps ps-aws aws-ip aws-env local-env tf-aws-apply tf-aws-destroy \
        forward forward-stop images images-push \
        api-coverage show-guard wire-size \
        demo-lb demo-lb-before demo-lb-after lb-report \
        proto proto-breaking ts-demo

local-up:                      ## emulator + observability
	docker compose up -d ministack redis jaeger prometheus
	@until [ "$$(curl -s -o /dev/null -w '%{http_code}' http://localhost:4566/_ministack/health)" = 200 ]; do sleep 1; done
	@echo "ministack ready on :4566"

local-down:
	docker compose down


# Always re-alias after an apply: new task containers, new container ids, so
# the previous aliases are gone with the old containers.
tf-local-apply:
	$(TF) init -upgrade
	$(TF) apply -auto-approve
	@sleep 12
	@$(MAKE) --no-print-directory dns
	@$(MAKE) --no-print-directory wait-ready

# Blocks until orderd can actually reach userd and productsd. See the script -
# the probe must exercise the real hop, not just check that DNS resolves.
wait-ready:
	@bash scripts/wait-ready.sh

tf-local-destroy:
	$(TF) destroy -auto-approve

# Ministack stores Cloud Map registrations but serves no DNS (SPEC.md 14.1).
# Bridge it with docker network aliases: service discovery IS just DNS.
# Aliases EVERY task, not just the first - docker's embedded DNS returns all A
# records for a shared alias, which is what makes the local load-balancing demo
# possible at all.
# NOTE: ONE regex filter, not two name filters. Docker ORs multiple
# --filter name= values, so "name=ministack-ecs-" plus "name=userd" matches
# every task and would alias orderd's container as userd.ecom.local -
# a silent, demo-breaking misroute.
dns:
	@docker network create ecom-dns >/dev/null 2>&1 || true
	@# Detach everything first. `docker network rm` cannot be used here: it
	@# fails while containers are attached, which would silently leave stale
	@# aliases pointing at replaced tasks.
	@for cid in $$(docker ps --filter "name=ministack-ecs-" -q); do \
		docker network disconnect -f ecom-dns $$cid >/dev/null 2>&1 || true; \
	done
	@for svc in userd productsd orderd gatewayd; do \
		cids=$$(docker ps --filter "name=ministack-ecs-.*-$$svc$$" -q); \
		if [ -z "$$cids" ]; then echo "no running task for $$svc"; continue; fi; \
		for cid in $$cids; do \
			docker network connect --alias $$svc.ecom.local ecom-dns $$cid \
				&& echo "aliased $$svc.ecom.local -> $$cid"; \
		done; \
	done

# Prove on screen that this is really ECS: control plane, Fargate-shaped task
# definition, the task self-describing via ECS_CONTAINER_METADATA_URI_V4, and
# credentials arriving the task-role way with no keys anywhere.
ps:
	@AWS_ENDPOINT_URL=http://localhost:4566 bash scripts/ps.sh

# Same, against real AWS (no endpoint override).
ps-aws:
	@CLUSTER=ecom-aws bash scripts/ps.sh

# ---- real AWS: two commands, and here is what they do ----
TF_AWS := terraform -chdir=terraform/envs/aws

# ONE command to stand the whole thing up on real AWS.
#
# Under the hood, in order:
#   1. terraform init          install the AWS provider and the modules
#   2. apply -target=...ecr    create the four ECR repositories FIRST, because
#                              you cannot push an image to a repo that does not
#                              exist, and you cannot start a service from an
#                              image that has not been pushed
#   3. make images-push        build four ARM64 distroless images and push them,
#                              using the account + region from your credentials
#   4. terraform apply         the rest: VPC, subnets, security group, cluster,
#                              Cloud Map, log group, IAM roles, the JWT secret,
#                              four task definitions, four ECS services
#   5. make aws-ip             print each task's public IP:port
#
# Needs terraform/envs/aws/terraform.tfvars (copy terraform.tfvars.example).
# It is ~2 minutes without RDS. Set `use_rds = true` there for a database,
# which adds 5-10 minutes.
tf-aws-apply:
	@echo "==> which account?"
	@aws sts get-caller-identity --query '{Account:Account,Arn:Arn}' --output table
	@test -f terraform/envs/aws/terraform.tfvars || { \
		echo "missing terraform/envs/aws/terraform.tfvars"; \
		echo "  cp terraform/envs/aws/terraform.tfvars.example terraform/envs/aws/terraform.tfvars"; \
		exit 1; }
	@echo "==> your egress IPv4 (must be in operator_ingress_cidrs, or nothing is reachable)"
	@curl -s -4 ifconfig.me; echo
	@echo "==> 1/5 terraform init"
	@$(TF_AWS) init -upgrade
	@echo "==> 2/5 ECR repositories first"
	@$(TF_AWS) apply -auto-approve -target=module.deployment.module.ecr
	@echo "==> 3/5 build + push four ARM64 images"
	@$(MAKE) --no-print-directory images-push
	@echo "==> 4/5 the rest of the stack"
	@$(TF_AWS) apply -auto-approve
	@echo "==> 5/5 waiting for four RUNNING tasks"
	@bash scripts/aws-wait.sh
	@$(MAKE) --no-print-directory aws-ip

# Tear it ALL down, and verify nothing is still billing.
#
# Under the hood:
#   1. scale every service to 0 and wait ~75s, because ECS has to deregister
#      the tasks from Cloud Map first - otherwise DeleteService returns
#      ResourceInUse and the destroy fails half-way
#   2. terraform destroy
#   3. list the things that cost money, so "Destroy complete" is checked rather
#      than trusted
tf-aws-destroy:
	@echo "==> which account?"
	@aws sts get-caller-identity --query 'Account' --output text
	@echo "==> 1/3 draining services to 0 (ECS must deregister from Cloud Map first)"
	-@for s in userd productsd orderd gatewayd; do \
		aws ecs update-service --cluster ecom-aws --service $$s --desired-count 0 \
			--query 'service.serviceName' --output text 2>/dev/null || true; \
	done
	@echo "    waiting 75s for deregistration..."
	@bash -c 'for i in $$(seq 1 75); do printf "."; sleep 1; done; echo'
	@echo "==> 2/3 terraform destroy"
	@$(TF_AWS) destroy -auto-approve
	@echo "==> 3/3 verifying nothing is left"
	@bash scripts/aws-verify-empty.sh

# Print every task's PUBLIC IP:port on AWS, ready to paste into grpcurl/curl.
# There is no load balancer and no domain here, so addresses change whenever a
# task is replaced - re-run this after a deploy or a scale event.
aws-ip:
	@bash scripts/aws-endpoints.sh

# Just the shell assignments, so the addresses go straight into your shell:
#
#   eval "$$(make -s aws-env)"
#
# Then every grpcurl/curl command in INSTRUCTIONS.md works as written. Re-run
# it after any deploy or scale event - awsvpc gives each task its own ENI, so
# the IPs move.
aws-env:
	@bash scripts/aws-endpoints.sh --env

# The same variables for the LOCAL stack, so every command in INSTRUCTIONS.md
# works in both places without editing anything:
#
#   make forward && eval "$$(make -s local-env)"
local-env:
	@echo "export U=localhost:50051"
	@echo "export O=localhost:50052"
	@echo "export P=localhost:50053"
	@echo "export BASE=http://localhost:8080"
	@echo "export REST_BASE=http://localhost:8080"

test:
	go test ./...

# awsvpc tasks have no host port, so the demo client runs ON the task network.
demo:
	bash scripts/smoke.sh

# ---- the load-balancing demo, in the terminal (no Prometheus needed) ----
#
# Two commands, run in this order:
#   make demo-lb-before   gRPC's DEFAULT (pick_first): one task takes everything
#   make demo-lb-after    the fix (round_robin + dns:///): evenly spread
#
# Each one redeploys orderd with that client behaviour, scales userd to 3,
# drives load, and prints the per-task delta. `make demo-lb` runs both.
demo-lb-before:
	@$(MAKE) --no-print-directory scale N=3
	@bash scripts/lb-demo.sh pick_first

demo-lb-after:
	@$(MAKE) --no-print-directory scale N=3
	@bash scripts/lb-demo.sh round_robin

demo-lb: demo-lb-before demo-lb-after

# Just the per-task counters, without redeploying anything.
lb-report:
	@bash scripts/lb-report.sh

# Sustained load through orderd, so every request makes orderd call
# userd.VerifyToken - the hop the load-balancing demo is about.
# Needs `make forward` first, and authenticates as a SEEDED user.
demo-load:
	@bash scripts/load.sh

# Scale a stateless service. SCALE_SVC defaults to userd, the busiest hop.
#
# To demonstrate the BUG before the fix, redeploy orderd with the broken
# client first:
#   terraform -chdir=terraform/envs/local apply -auto-approve -var lb_policy=pick_first
# then `make scale N=3 && make demo-load` and watch one task take everything.
# Re-apply without the variable to show the fix.
SCALE_SVC ?= userd
scale:
	@$(LOCAL_AWS) ecs update-service \
		--cluster ecom-local --service $(SCALE_SVC) --desired-count $(N) >/dev/null
	@echo "waiting for the new tasks to come up..."
	@sleep 20
	@$(MAKE) --no-print-directory dns
	@$(LOCAL_AWS) ecs describe-services \
		--cluster ecom-local --services $(SCALE_SVC) \
		--query 'services[0].{Service:serviceName,Desired:desiredCount,Running:runningCount}' --output table

# Terraform refuses to scale the one service that writes.
show-guard:
	-@terraform -chdir=terraform/envs/local plan -var order_desired_count=3 -no-color 2>&1 \
		| grep -A8 "Invalid value for variable"


# ---- codegen: ONE proto, Go + TypeScript ----
# Generated code is committed, so a clone builds without buf installed.
proto:
	buf lint
	buf generate
	buf generate --template buf.gen.ts.yaml --include-imports

# Refuses to generate if a change would break existing clients.
proto-breaking:
	buf breaking --against '.git#branch=main'

ts-demo:
	cd clients/node && npm install && npm run demo

# awsvpc tasks have no host port, so publish one via a relay on the task
# network. This is the local stand-in for SSM port forwarding on AWS - needed
# because Ministack does not emulate ssmmessages.
# Depends on dns: terraform replaces task containers on every deploy, and a
# replaced container has no alias, so forwarding would resolve nothing.
forward: dns
	@bash scripts/forward.sh

forward-stop:
	@docker rm -f forward-userd forward-productsd forward-orderd forward-gatewayd \
		>/dev/null 2>&1 || true
	@echo "forwarders stopped"

# ---- loop 1: no docker, no emulator, fastest possible ----
# Three terminals. SQLite files in ./data. This is where you write business
# logic: a change is one ^C and one `go run` away, about two seconds.
DEV_JWT_SECRET ?= local-dev-secret-not-for-anything-real
DEV_DATA       ?= ./data

dev-seed:
	@mkdir -p $(DEV_DATA)
	go run ./cmd/seed -user-db "file:$(DEV_DATA)/user.db" -product-db "file:$(DEV_DATA)/product.db"

dev-userd:
	@mkdir -p $(DEV_DATA)
	JWT_SECRET=$(DEV_JWT_SECRET) \
	DB_URL="file:$(DEV_DATA)/user.db" \
	GRPC_ADDR=":50051" METRICS_ADDR=":9091" \
	go run ./cmd/userd

dev-productsd:
	@mkdir -p $(DEV_DATA)
	DB_URL="file:$(DEV_DATA)/product.db" \
	GRPC_ADDR=":50053" METRICS_ADDR=":9093" \
	go run ./cmd/productsd

dev-orderd:
	@mkdir -p $(DEV_DATA)
	USER_ADDR="localhost:50051" PRODUCT_ADDR="localhost:50053" \
	DB_URL="file:$(DEV_DATA)/order.db" \
	GRPC_ADDR=":50052" METRICS_ADDR=":9092" \
	go run ./cmd/orderd

dev-smoke:
	USER_ADDR=localhost:50051 PRODUCT_ADDR=localhost:50053 ORDER_ADDR=localhost:50052 \
	bash scripts/smoke.sh

dev-clean:
	rm -rf $(DEV_DATA)

# ---- loop 2: images. One Dockerfile per STORAGE SHAPE, not per service. ----
# seeded   = database baked in at build time  (userd, productsd)
# stateful = empty writable database         (orderd)
# stateless= no database at all              (gatewayd)
images:
	docker build --platform linux/arm64 -f build/Dockerfile.seeded \
	  --build-arg SERVICE=userd --build-arg SEED_FLAG=-user-db --build-arg DB_FILE=user.db \
	  -t userd:0.1.0 -t localhost:4566/userd:0.1.0 .
	docker build --platform linux/arm64 -f build/Dockerfile.seeded \
	  --build-arg SERVICE=productsd --build-arg SEED_FLAG=-product-db --build-arg DB_FILE=product.db \
	  -t productsd:0.1.0 -t localhost:4566/productsd:0.1.0 .
	docker build --platform linux/arm64 -f build/Dockerfile.stateful \
	  --build-arg SERVICE=orderd -t orderd:0.1.0 -t localhost:4566/orderd:0.1.0 .
	docker build --platform linux/arm64 -f build/Dockerfile.stateless \
	  --build-arg SERVICE=gatewayd -t gatewayd:0.1.0 -t localhost:4566/gatewayd:0.1.0 .
	@docker images --format '{{.Repository}}:{{.Tag}}\t{{.Size}}' | grep -E '^(userd|productsd|orderd|gatewayd):0.1.0'

# Build the same four images and push them to ECR, tagged for your account.
#
# Reads the account and region from your CURRENT aws credentials, so the thing
# it pushes to is the thing `terraform apply` will deploy from. Run
# `aws sts get-caller-identity` first and read the account number.
#
#   make images-push                 # uses AWS_REGION or us-east-1
#   AWS_REGION=eu-central-1 make images-push
IMAGE_TAG ?= 0.1.0
images-push:
	@command -v aws >/dev/null || { echo "aws CLI not found"; exit 1; }
	@set -euo pipefail; \
	region="$${AWS_REGION:-us-east-1}"; \
	account=$$(aws sts get-caller-identity --query Account --output text); \
	ecr="$$account.dkr.ecr.$$region.amazonaws.com"; \
	echo "account $$account  region $$region"; \
	echo "pushing to $$ecr"; \
	aws ecr get-login-password --region "$$region" \
	  | docker login --username AWS --password-stdin "$$ecr" >/dev/null; \
	docker build --platform linux/arm64 -f build/Dockerfile.seeded \
	  --build-arg SERVICE=userd --build-arg SEED_FLAG=-user-db --build-arg DB_FILE=user.db \
	  -t "$$ecr/userd:$(IMAGE_TAG)" . ; \
	docker build --platform linux/arm64 -f build/Dockerfile.seeded \
	  --build-arg SERVICE=productsd --build-arg SEED_FLAG=-product-db --build-arg DB_FILE=product.db \
	  -t "$$ecr/productsd:$(IMAGE_TAG)" . ; \
	docker build --platform linux/arm64 -f build/Dockerfile.stateful \
	  --build-arg SERVICE=orderd -t "$$ecr/orderd:$(IMAGE_TAG)" . ; \
	docker build --platform linux/arm64 -f build/Dockerfile.stateless \
	  --build-arg SERVICE=gatewayd -t "$$ecr/gatewayd:$(IMAGE_TAG)" . ; \
	for svc in userd productsd orderd gatewayd; do \
	  docker push "$$ecr/$$svc:$(IMAGE_TAG)"; \
	done; \
	echo; \
	echo "Put these in terraform/envs/aws/terraform.tfvars:"; \
	echo "  user_image    = \"$$ecr/userd:$(IMAGE_TAG)\""; \
	echo "  product_image = \"$$ecr/productsd:$(IMAGE_TAG)\""; \
	echo "  order_image   = \"$$ecr/orderd:$(IMAGE_TAG)\""; \
	echo "  gateway_image = \"$$ecr/gatewayd:$(IMAGE_TAG)\""

dev-gatewayd:
	USER_ADDR="127.0.0.1:50051" PRODUCT_ADDR="127.0.0.1:50053" ORDER_ADDR="127.0.0.1:50052" \
	HTTP_ADDR=":8080" METRICS_ADDR=":9094" \
	go run ./cmd/gatewayd

# Prove REST works, and that the internal-only RPC is NOT exposed.
dev-rest:
	@bash scripts/rest-smoke.sh

# Measure the same message as protobuf and as JSON. Run it on stage instead of
# claiming a number on a slide.
wire-size:
	@go run ./cmd/wiresize

# Hit every rpc and every REST route, and report anything missed.
api-coverage:
	@bash scripts/api-coverage.sh
