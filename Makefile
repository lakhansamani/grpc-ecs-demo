# The only interface you type on stage.
SHELL := /bin/bash
TF    := terraform -chdir=terraform/envs/local

.PHONY: local-up local-down llm-up tf-local-apply tf-local-destroy dns demo demo-load scale test

local-up:                      ## emulator + observability
	docker compose up -d ministack redis jaeger
	@until [ "$$(curl -s -o /dev/null -w '%{http_code}' http://localhost:4566/_ministack/health)" = 200 ]; do sleep 1; done
	@echo "ministack ready on :4566"

local-down:
	docker compose down

# Optional: real LLM prose locally instead of Ministack's canned mock reply.
llm-up:
	docker compose --profile llm up -d ollama
	docker compose exec ollama ollama pull llama3.2:1b
	docker compose restart ministack

# Always re-alias after an apply: new task containers, new container ids, so
# the previous aliases are gone with the old containers.
tf-local-apply:
	$(TF) init -upgrade
	$(TF) apply -auto-approve
	@sleep 12
	@$(MAKE) --no-print-directory dns

tf-local-destroy:
	$(TF) destroy -auto-approve

# Ministack stores Cloud Map registrations but serves no DNS (SPEC.md 14.1).
# Bridge it with docker network aliases: service discovery IS just DNS.
# Aliases EVERY task, not just the first - docker's embedded DNS returns all A
# records for a shared alias, which is what makes the local load-balancing demo
# possible at all.
# NOTE: ONE regex filter, not two name filters. Docker ORs multiple
# --filter name= values, so "name=ministack-ecs-" plus "name=identityd" matches
# every task and would alias paymentd's container as identityd.ecom.local -
# a silent, demo-breaking misroute.
dns:
	@docker network create ecom-dns >/dev/null 2>&1 || true
	@# Detach everything first. `docker network rm` cannot be used here: it
	@# fails while containers are attached, which would silently leave stale
	@# aliases pointing at replaced tasks.
	@for cid in $$(docker ps --filter "name=ministack-ecs-" -q); do \
		docker network disconnect -f ecom-dns $$cid >/dev/null 2>&1 || true; \
	done
	@for svc in identityd paymentd; do \
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
	@CLUSTER=payments-aws bash scripts/ps.sh

test:
	go test ./...

# awsvpc tasks have no host port, so the demo client runs ON the task network.
demo:
	bash scripts/smoke.sh

# Authenticates as a SEEDED user, never a freshly registered one: a Register'd
# user exists on exactly one identityd task (SPEC.md 6.4).
demo-load:
	go run ./cmd/demo-client -mode=load

scale:
	aws --endpoint-url http://localhost:4566 ecs update-service \
		--cluster ecom-local --service identityd --desired-count $(N)
	$(MAKE) dns

# ---- codegen: ONE proto, Go + TypeScript ----
# Generated code is committed, so a clone builds without buf installed.
proto:
	buf lint
	buf generate

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
	@docker rm -f forward-identityd forward-paymentd >/dev/null 2>&1 || true
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
