FROM grafana/k6:2.3.0

ARG TARGETARCH

ENV K6_NO_USAGE_REPORT=true

COPY tools/proxy-bench/proxy-bench.js /home/k6/proxy-bench.js

COPY tools/proxy-bench/fixed-pool/run-benchmark.sh /home/k6/fixed-pool/
COPY bin/proxy-bench-tools/${TARGETARCH}/proxy-bench-worker-support /usr/local/bin/

ENTRYPOINT ["k6", "run", "/home/k6/proxy-bench.js"]
