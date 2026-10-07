# The only interface you type on stage.
SHELL := /bin/bash
TF    := terraform -chdir=terraform/envs/local

.PHONY: local-up local-down tf-local-apply tf-local-destroy dns demo demo-load test

local-up:          ## emulator + observability
	docker compose up -d ministack redis jaeger
	@until [ "$$(curl -s -o /dev/null -w '%{http_code}' http://localhost:4566/_ministack/health)" = 200 ]; do sleep 1; done
	@echo "ministack ready on :4566"

local-down:
	docker compose down

tf-local-apply:
	$(TF) init -upgrade
	$(TF) apply -auto-approve

tf-local-destroy:
	$(TF) destroy -auto-approve

# Ministack stores Cloud Map registrations but serves no DNS (PLAN.md §8 finding 3).
# Bridge it with docker network aliases: service discovery IS just DNS.
dns:
	@docker network create ecom-dns >/dev/null 2>&1 || true
	@for svc in identityd paymentd; do \
		cid=$$(docker ps --filter "name=ministack-ecs-" --filter "name=$$svc" -q | head -1); \
		if [ -n "$$cid" ]; then \
			docker network connect --alias $$svc.ecom.local ecom-dns $$cid 2>/dev/null \
				&& echo "aliased $$svc.ecom.local" || echo "$$svc already aliased"; \
		else echo "no running task for $$svc"; fi; \
	done

test:
	go test ./...

# awsvpc tasks have no host port, so the demo client runs ON the task network.
demo:
	go run ./cmd/demo-client -mode=once

demo-load:
	go run ./cmd/demo-client -mode=load

# Optional: real LLM text locally. Without this, Bedrock Converse returns
# Ministack's canned mock reply -- correct API shape, placeholder text.
llm-up:
	docker compose --profile llm up -d ollama
	docker compose exec ollama ollama pull llama3.2:1b
	docker compose restart ministack
