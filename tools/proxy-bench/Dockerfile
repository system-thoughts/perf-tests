FROM alpine:3.22

ARG TARGETARCH

COPY tools/proxy-bench/fixed-pool/run-benchmark.sh /home/k6/fixed-pool/
COPY bin/proxy-bench-tools/${TARGETARCH}/proxy-bench-worker-support /usr/local/bin/

USER 1000:1000
WORKDIR /home/k6

ENTRYPOINT ["/usr/local/bin/proxy-bench-worker-support", "original"]
