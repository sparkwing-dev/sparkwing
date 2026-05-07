#!/usr/bin/env bash
#
# Sparkwing Proxy Benchmark — In-Cluster (EKS)
#
# Runs Docker builds inside a k8s pod with DinD to measure
# the impact of sparkwing-cache on build times.
#
set -euo pipefail
cd "$(dirname "$0")"

NAMESPACE="sparkwing"
PROXY_URL="http://sparkwing-cache.sparkwing.svc.cluster.local:80"
JOB_NAME="bench-proxy-$(date +%s)"

echo "==> Creating ConfigMap with bench files"
kubectl delete configmap bench-files -n "$NAMESPACE" 2>/dev/null || true
kubectl create configmap bench-files -n "$NAMESPACE" \
    --from-file=Dockerfile=Dockerfile \
    --from-file=package.json=package.json \
    --from-file=Gemfile=Gemfile \
    --from-file=Gemfile.lock=Gemfile.lock

echo "==> Creating benchmark job: $JOB_NAME"
kubectl apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB_NAME
  namespace: $NAMESPACE
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 1800
  template:
    metadata:
      labels:
        app: bench-proxy
    spec:
      restartPolicy: Never
      containers:
        - name: bench
          image: docker:27
          env:
            - name: DOCKER_HOST
              value: tcp://localhost:2375
            - name: PROXY_URL
              value: "$PROXY_URL"
          command:
            - sh
            - -c
            - |
              set -e

              # Wait for Docker daemon
              echo "==> Waiting for Docker daemon..."
              until docker info > /dev/null 2>&1; do sleep 1; done
              echo "==> Docker ready"

              # Copy ConfigMap files (symlinks) to a real directory
              mkdir -p /work
              cp /bench/* /work/
              cd /work

              time_build() {
                local label="\$1"
                shift
                echo ""
                echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                echo "  \$label"
                echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                local start end elapsed
                start=\$(date +%s)
                docker build "\$@" . 2>&1 | tail -15
                end=\$(date +%s)
                elapsed=\$((end - start))
                local mins=\$((elapsed / 60))
                local secs=\$((elapsed % 60))
                echo ""
                echo "  TIME: \${mins}m \${secs}s (\${elapsed}s)"
                echo "RESULT|\$label|\$elapsed" >> /tmp/results.txt
              }

              # ── Scenario 1: Cold build, no proxy ──
              docker builder prune -af > /dev/null 2>&1 || true
              time_build "1. Cold build (no proxy)" \
                --no-cache --build-arg PROXY_URL="" -t bench-cold

              # ── Scenario 2: Warm rebuild (Docker cache) ──
              time_build "2. Warm rebuild (Docker cache)" \
                --build-arg PROXY_URL="" -t bench-warm

              # ── Scenario 3: Cold build + proxy (warms cache) ──
              docker builder prune -af > /dev/null 2>&1 || true
              time_build "3. Cold build + proxy (proxy warms)" \
                --no-cache --build-arg PROXY_URL="\$PROXY_URL" -t bench-proxy-warm

              # ── Scenario 4: New base image + proxy (cached) ──
              docker builder prune -af > /dev/null 2>&1 || true
              time_build "4. New base image + proxy (cached)" \
                --no-cache \
                --build-arg NODE_IMAGE=node:20-alpine \
                --build-arg RUBY_IMAGE=ruby:3.2-alpine \
                --build-arg PROXY_URL="\$PROXY_URL" \
                -t bench-newbase-proxy

              # ── Scenario 5: New base image, no proxy ──
              docker builder prune -af > /dev/null 2>&1 || true
              time_build "5. New base image, no proxy" \
                --no-cache \
                --build-arg NODE_IMAGE=node:20-alpine \
                --build-arg RUBY_IMAGE=ruby:3.2-alpine \
                --build-arg PROXY_URL="" \
                -t bench-newbase-noproxy

              # ── Results ──
              echo ""
              echo "╔══════════════════════════════════════════════════════════════╗"
              echo "║                    BENCHMARK RESULTS                        ║"
              echo "╠══════════════════════════════════════════════════════════════╣"
              while IFS='|' read -r tag label secs; do
                mins=\$((secs / 60))
                rem=\$((secs % 60))
                printf "║  %-44s  %3dm %02ds  ║\n" "\$label" "\$mins" "\$rem"
              done < /tmp/results.txt
              echo "╚══════════════════════════════════════════════════════════════╝"

              echo ""
              echo "==> Proxy cache stats:"
              wget -qO- "\$PROXY_URL/stats" 2>/dev/null || echo "(could not reach proxy)"

          volumeMounts:
            - name: bench-files
              mountPath: /bench
          resources:
            requests:
              cpu: 500m
              memory: 512Mi
            limits:
              cpu: "2"
              memory: 2Gi

        - name: dind
          image: docker:27-dind
          securityContext:
            privileged: true
          env:
            - name: DOCKER_TLS_CERTDIR
              value: ""
          resources:
            requests:
              cpu: 500m
              memory: 1Gi
            limits:
              cpu: "2"
              memory: 4Gi

      volumes:
        - name: bench-files
          configMap:
            name: bench-files
EOF

echo "==> Benchmark job created: $JOB_NAME"
echo "==> Streaming logs (this will take 10-20 minutes)..."
echo ""

# Wait for pod to start, then stream logs
kubectl wait --for=condition=Ready "pods" -l "job-name=$JOB_NAME" -n "$NAMESPACE" --timeout=120s 2>/dev/null || true
kubectl logs -f "job/$JOB_NAME" -n "$NAMESPACE" -c bench
