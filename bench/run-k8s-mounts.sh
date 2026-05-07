#!/usr/bin/env bash
#
# Sparkwing Cache Mount Benchmark — In-Cluster
#
# Tests BuildKit cache mounts persisting across builds on the same
# DinD volume (simulating the warm PVC pool).
#
# Scenarios:
#   A. Cold build (first ever — populates cache mounts)
#   B. Layer cache (unchanged rebuild)
#   C. --no-cache, mounts warm (layers busted, deps cached)
#   D. Dep change, mounts warm (add dayjs + httparty)
#   E. Cold comparison (prune everything, rebuild with new deps)
#
set -e
cd "$(dirname "$0")"

NAMESPACE="sparkwing"
JOB_NAME="bench-mounts-$(date +%s)"

# Pre-generate modified files for the dep change scenario
MODDIR=$(mktemp -d)
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
echo "==> Generating modified bench files..."
bash "$SCRIPT_DIR/.claude-scratch/gen-modified-bench.sh" "$(pwd)" "$MODDIR"

echo "==> Creating ConfigMaps"
kubectl delete configmap bench-files bench-files-modified -n "$NAMESPACE" 2>/dev/null || true

kubectl create configmap bench-files -n "$NAMESPACE" \
    --from-file=Dockerfile=Dockerfile \
    --from-file=package.json=package.json \
    --from-file=Gemfile=Gemfile \
    --from-file=Gemfile.lock=Gemfile.lock

kubectl create configmap bench-files-modified -n "$NAMESPACE" \
    --from-file=package.json="$MODDIR/package2.json" \
    --from-file=Gemfile="$MODDIR/Gemfile2" \
    --from-file=Gemfile.lock="$MODDIR/Gemfile.lock"

rm -rf "$MODDIR"

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
        app: bench-mounts
    spec:
      restartPolicy: Never
      containers:
        - name: bench
          image: docker:27
          env:
            - name: DOCKER_HOST
              value: tcp://localhost:2375
          command:
            - sh
            - -c
            - |
              set -e

              echo "==> Waiting for Docker daemon..."
              until docker info > /dev/null 2>&1; do sleep 1; done
              echo "==> Docker ready"

              # Set up original build dir
              mkdir -p /work
              cp /bench/* /work/
              cd /work

              time_build() {
                local label="\$1"
                shift
                echo ""
                echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                echo "  \$label"
                echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
                local start end elapsed
                start=\$(date +%s)
                docker build "\$@" . 2>&1 | tail -20
                end=\$(date +%s)
                elapsed=\$((end - start))
                local mins=\$((elapsed / 60))
                local secs=\$((elapsed % 60))
                echo ""
                echo "  TIME: \${mins}m \${secs}s (\${elapsed}s)"
                echo "\$label|\$elapsed" >> /tmp/results.txt
              }

              # ── A. Cold build ──
              docker builder prune -af > /dev/null 2>&1 || true
              time_build "A. Cold build" \
                --no-cache -t bench-a

              # ── B. Layer cache (unchanged) ──
              time_build "B. Layer cache" \
                -t bench-b

              # ── C. --no-cache, mounts warm ──
              time_build "C. --no-cache mounts warm" \
                --no-cache -t bench-c

              # ── D. Dep change + warm mounts ──
              # Swap in modified files (pre-generated with valid Gemfile.lock)
              cp /bench-modified/package.json /work/package.json
              cp /bench-modified/Gemfile /work/Gemfile
              cp /bench-modified/Gemfile.lock /work/Gemfile.lock

              time_build "D. Dep change + warm mounts" \
                --no-cache -t bench-d

              # ── E. Same new deps, prune all (cold comparison) ──
              docker builder prune -af > /dev/null 2>&1 || true
              time_build "E. Dep change cold" \
                --no-cache -t bench-e

              # ── Results ──
              echo ""
              echo "╔═════════════════════════════════════════════════════════╗"
              echo "║         CACHE MOUNT BENCHMARK (EKS)                    ║"
              echo "╠══════════════════════════════════╦══════════════════════╣"
              echo "║ Scenario                         ║  Time               ║"
              echo "╠══════════════════════════════════╬══════════════════════╣"
              while IFS='|' read -r label secs; do
                mins=\$((secs / 60))
                rem=\$((secs % 60))
                printf "║ %-32s ║  %3dm %02ds (%3ds)      ║\n" "\$label" "\$mins" "\$rem" "\$secs"
              done < /tmp/results.txt
              echo "╚══════════════════════════════════╩══════════════════════╝"

              echo ""
              a=\$(sed -n '1p' /tmp/results.txt | cut -d'|' -f2)
              b=\$(sed -n '2p' /tmp/results.txt | cut -d'|' -f2)
              c=\$(sed -n '3p' /tmp/results.txt | cut -d'|' -f2)
              d=\$(sed -n '4p' /tmp/results.txt | cut -d'|' -f2)
              e=\$(sed -n '5p' /tmp/results.txt | cut -d'|' -f2)

              echo "ANALYSIS:"
              echo "  Layer cache:           \${a}s -> \${b}s  (saved \$((a - b))s)"
              echo "  Cache mounts (no change): \${a}s -> \${c}s  (saved \$((a - c))s)"
              echo "  Cache mounts (dep change): \${e}s -> \${d}s  (saved \$((e - d))s)"

          volumeMounts:
            - name: bench-files
              mountPath: /bench
            - name: bench-files-modified
              mountPath: /bench-modified
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
        - name: bench-files-modified
          configMap:
            name: bench-files-modified
EOF

echo "==> Benchmark job created: $JOB_NAME"
echo "==> Streaming logs..."
echo ""

kubectl wait --for=condition=Ready "pods" -l "job-name=$JOB_NAME" -n "$NAMESPACE" --timeout=120s 2>/dev/null || true
kubectl logs -f "job/$JOB_NAME" -n "$NAMESPACE" -c bench
