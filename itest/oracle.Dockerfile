# Go test runner with Oracle Instant Client, for the godror-based
# gorm-oracle driver (godror dlopen()s libclntsh.so at runtime).
#
# Built and used by `make -C itest oracle-smoke` via the `oracle-runner`
# service in compose.yaml; the repository is bind-mounted at /src.
FROM golang:1.26-bookworm

ARG IC_VERSION=23.9.0.25.07
ARG IC_DIR=2390000
ARG IC_URL=https://download.oracle.com/otn_software/linux/instantclient/${IC_DIR}/instantclient-basiclite-linux.x64-${IC_VERSION}.zip

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends libaio1 unzip ca-certificates curl; \
    rm -rf /var/lib/apt/lists/*; \
    mkdir -p /opt/oracle; \
    curl -fsSL -o /tmp/ic.zip "${IC_URL}"; \
    unzip -q /tmp/ic.zip -d /opt/oracle; \
    rm /tmp/ic.zip; \
    ln -s /opt/oracle/instantclient_* /opt/oracle/instantclient; \
    echo /opt/oracle/instantclient > /etc/ld.so.conf.d/oracle-instantclient.conf; \
    ldconfig; \
    ldconfig -p | grep -q libclntsh.so

ENV LD_LIBRARY_PATH=/opt/oracle/instantclient \
    CGO_ENABLED=1

WORKDIR /src/itest
CMD ["go", "test", "-tags", "integration", "-count=1", "-v", "-run", "TestSmoke/^oracle$", "./..."]
