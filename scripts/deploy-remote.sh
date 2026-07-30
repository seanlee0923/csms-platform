#!/bin/sh
set -eu

: "${CSMS_REMOTE_HOST:?set CSMS_REMOTE_HOST, for example user@server}"

CSMS_REMOTE_PORT="${CSMS_REMOTE_PORT:-22}"
CSMS_REMOTE_DIR="${CSMS_REMOTE_DIR:-/home/${CSMS_REMOTE_HOST%%@*}/csms-platform}"
CSMS_IMAGE="${CSMS_IMAGE:-csms-runtime:0.1.0}"
CSMS_ARCHIVE="/tmp/csms-runtime-image.tar"
SSH_COMMAND="ssh -p ${CSMS_REMOTE_PORT}"

rsync -az --delete \
	--exclude .git \
	--exclude .idea \
	-e "${SSH_COMMAND}" \
	./ "${CSMS_REMOTE_HOST}:${CSMS_REMOTE_DIR}/"

ssh -p "${CSMS_REMOTE_PORT}" "${CSMS_REMOTE_HOST}" \
	"cd '${CSMS_REMOTE_DIR}' && docker build -t '${CSMS_IMAGE}' . && docker save -o '${CSMS_ARCHIVE}' '${CSMS_IMAGE}'"

ssh -t -p "${CSMS_REMOTE_PORT}" "${CSMS_REMOTE_HOST}" \
	"sudo /var/lib/rancher/rke2/bin/ctr --address /run/k3s/containerd/containerd.sock --namespace k8s.io images import '${CSMS_ARCHIVE}'"

ssh -p "${CSMS_REMOTE_PORT}" "${CSMS_REMOTE_HOST}" \
	"cd '${CSMS_REMOTE_DIR}' &&
	kubectl apply -k config/runtime &&
	kubectl rollout restart deployment/csms-runtime &&
	kubectl rollout status deployment/csms-runtime --timeout=180s &&
	kubectl get deployment,pod,service -l app.kubernetes.io/name=csms-runtime -o wide &&
	service_ip=\$(kubectl get service csms-runtime -o jsonpath='{.spec.clusterIP}') &&
	curl -fsS \"http://\${service_ip}:8080/livez\" >/dev/null &&
	curl -fsS \"http://\${service_ip}:8080/readyz\" >/dev/null"

echo "CSMS Runtime deployment and health verification completed"
