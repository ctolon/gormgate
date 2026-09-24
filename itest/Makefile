# gormgate integration tests. Usable from anywhere: make -C itest <target>
ITEST_DIR := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
COMPOSE   := docker compose -f $(ITEST_DIR)/compose.yaml
GO_TEST   := go test -tags integration -count=1

# Vendors that run from the host. Oracle needs Oracle Instant Client and
# therefore runs in the oracle-runner container (see oracle-smoke).
SERVER_VENDORS := pg14 pg18 mysql80 mysql84 mariadb106 mariadb118 tidb cockroach mssql gauss clickhouse
HOST_VENDORS   := $(SERVER_VENDORS) sqlite sqlite-purego
ALL_VENDORS    := $(HOST_VENDORS) oracle

empty :=
space := $(empty) $(empty)
bar   := |
HOST_RUN := TestSmoke/^($(subst $(space),$(bar),$(HOST_VENDORS)))$$

# Extra flags for go test, e.g. make smoke TESTFLAGS=-v
TESTFLAGS ?=
# Test selection inside the Oracle runner container.
ORACLE_RUN ?= TestSmoke/^oracle$$
# The full Oracle suite, the counterpart of the `integration` CI matrix job.
ORACLE_TEST_RUN ?= TestSmoke|TestConformance|TestGormParity|TestPropertyEvolution|TestOracleSequenceReset

.PHONY: help up down ps smoke oracle-runner oracle-smoke oracle-test smoke-all \
	$(addprefix up-,$(SERVER_VENDORS) oracle) $(addprefix smoke-,$(ALL_VENDORS))

help:
	@echo "targets: up down ps smoke oracle-smoke oracle-test smoke-all oracle-runner"
	@echo "         up-<svc>    start one service   ($(SERVER_VENDORS) oracle)"
	@echo "         smoke-<key> smoke one vendor    ($(ALL_VENDORS))"

## up: start every database service and wait until all are healthy
up:
	$(COMPOSE) up -d --wait

## down: stop and remove all services (tmpfs data is discarded)
down:
	$(COMPOSE) --profile runner down --remove-orphans

ps:
	$(COMPOSE) ps

## smoke: smoke tests for all host-runnable vendors; unreachable = failure
smoke:
	cd $(ITEST_DIR) && GORMGATE_ITEST_REQUIRE=1 $(GO_TEST) $(TESTFLAGS) -run '$(HOST_RUN)' ./...

## oracle-runner: build the Go + Oracle Instant Client test-runner image
oracle-runner:
	$(COMPOSE) --profile runner build oracle-runner

## oracle-smoke: Oracle smoke test inside the runner container
oracle-smoke: oracle-runner
	$(COMPOSE) up -d --wait oracle
	$(COMPOSE) --profile runner run --rm oracle-runner \
		go test -tags integration -count=1 -v $(TESTFLAGS) -run '$(ORACLE_RUN)' ./...

## oracle-test: the full Oracle suite inside the runner container
oracle-test: oracle-runner
	$(COMPOSE) up -d --wait oracle
	$(COMPOSE) --profile runner run --rm \
		-e GORMGATE_ITEST_REQUIRE=1 -e GORMGATE_ITEST_VENDORS=oracle oracle-runner \
		go test -tags integration -count=1 -v $(TESTFLAGS) -run '$(ORACLE_TEST_RUN)' .

## smoke-all: every vendor, including Oracle
smoke-all: up smoke oracle-smoke

$(addprefix up-,$(SERVER_VENDORS) oracle): up-%:
	$(COMPOSE) up -d --wait $*

$(addprefix smoke-,$(SERVER_VENDORS)): smoke-%: up-%
	cd $(ITEST_DIR) && GORMGATE_ITEST_REQUIRE=1 $(GO_TEST) -v $(TESTFLAGS) -run 'TestSmoke/^$*$$' ./...

smoke-sqlite smoke-sqlite-purego: smoke-%:
	cd $(ITEST_DIR) && GORMGATE_ITEST_REQUIRE=1 $(GO_TEST) -v $(TESTFLAGS) -run 'TestSmoke/^$*$$' ./...

smoke-oracle: oracle-smoke
