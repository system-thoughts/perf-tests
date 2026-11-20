FROM grafana/k6:2.3.0

ARG TARGETARCH

ENV K6_NO_USAGE_REPORT=true

COPY tools/proxy-bench/proxy-bench.js tools/proxy-bench/run-proxy-bench.sh /home/k6/
COPY tools/proxy-bench/lib /home/k6/lib

COPY tools/proxy-bench/fixed-pool/benchmark.js tools/proxy-bench/fixed-pool/run-benchmark.sh /home/k6/fixed-pool/
COPY tools/proxy-bench/fixed-pool/lib /home/k6/fixed-pool/lib
COPY bin/proxy-bench-tools/${TARGETARCH}/proxy-bench-worker-support /usr/local/bin/

ENTRYPOINT ["/bin/sh", "/home/k6/run-proxy-bench.sh"]
