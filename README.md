# CSMS Platform

[![CI](https://github.com/seanlee0923/csms-platform/actions/workflows/ci.yml/badge.svg)](https://github.com/seanlee0923/csms-platform/actions/workflows/ci.yml)

`github.com/seanlee0923/ocpp` 라이브러리로 만든 OCPP 런타임을 Kubernetes에서
선언적으로 배포·확장·헬스체크하기 위한 **Operator**와, 그 Operator를 실제로
검증하는 데 쓰는 **참조용 최소 Runtime**(`cmd/csms-server`)으로 구성된
프로젝트다.

- **Operator**: `CSMS` custom resource 하나로 임의의 OCPP 런타임 컨테이너의
  Deployment/Service/ConfigMap/Ingress/PodDisruptionBudget을 관리한다.
  Operator는 이미지 내부의 업무 로직(인증, 결제, transaction 등)을 전혀 알지
  못하며, 그 컨테이너가 `image`/`port`/`livenessPath`/`readinessPath` 계약만
  지키면 어떤 OCPP 런타임이든 배포 대상이 될 수 있다. MySQL/Redis/API key
  Secret은 직접 만들지 않고 참조만 한다.
- **참조 Runtime**(`cmd/csms-server`): OCPP-J WebSocket 연결을 수용하고,
  MySQL에 station/connector 상태를 영속화하며, Redis 기반 분산 session
  ownership과 cross-Pod command 전달로 수평 확장을 지원하는 최소 구현이다.
  BootNotification/Heartbeat/StatusNotification과 Reset command만 처리하며,
  실제 프로덕션 업무 로직(인증, 결제/과금, 전체 트랜잭션 흐름 등)은 의도적으로
  포함하지 않는다 — Operator가 어떤 이미지를 배포하든 동작한다는 것을 보여주는
  참조 구현이자 Operator 자체의 테스트 대상이다.

Runtime과 Operator는 서로 다른 컨테이너 이미지와 RBAC 권한을 갖는다. Runtime Pod에는
Kubernetes 리소스 변경 권한이 없고, Operator Pod에는 OCPP WebSocket 트래픽이
전달되지 않는다.

**직접 만든 OCPP 런타임을 배포하려면** 아래
[직접 만든 OCPP 런타임 배포하기](#직접-만든-ocpp-런타임-배포하기)를 참고한다.

## 아키텍처

Operator 입장에서 아래 박스는 `image`/`port`/`livenessPath`/`readinessPath`
계약만 지키는 교체 가능한 컨테이너다. 아래는 이 저장소가 포함하는 참조
Runtime(`cmd/csms-server`)이 그 계약 안에서 실제로 무엇을 구현했는지 보여준다.

```text
Charging Station
        │ OCPP-J WebSocket (ocpp1.6 | ocpp2.0.1 | ocpp2.1)
        ▼
Ingress / Load Balancer
        │
        ▼
참조 Runtime Pods (Deployment, replicas N)
        ├── ocpp.csms.Server (session, subprotocol 협상, typed routing)
        ├── BootNotification / Heartbeat / StatusNotification handler
        ├── Reset outbound command dispatcher
        ├── /livez, /readyz, /metrics
        ├── MySQL client (station/connector 상태 + event history)
        ├── Redis session ownership client (claim/renew/release, fencing)
        └── Redis Streams command bus consumer
                 │
    ┌────────────┼────────────┐
    ▼            ▼            ▼
 MySQL         Redis      (외부 API 호출자)
                 │              │
                 └── owner 조회 ─┘
                        │
              POST /api/v1/stations/{id}/commands/reset
              (owner가 다른 Pod여도 command bus로 전달)

Kubernetes Operator (별도 Deployment, 별도 RBAC)
        ├── CSMS CRD watch
        ├── Deployment/Service/ConfigMap/Ingress/PodDisruptionBudget reconcile
        ├── 위 Runtime 내부 로직(MySQL/Redis/command bus 등)은 알지 못함
        ├── Secret은 참조만 함 (생성/관리 안 함)
        └── CSMS.status(Available/Progressing) 갱신
```

station의 WebSocket 연결은 특정 Runtime Pod의 메모리에만 존재한다. 외부 API가
어느 Pod로 요청을 보내든, Redis session registry에서 현재 owner Pod를 조회한 뒤
Redis Streams command bus로 owner Pod에 명령을 전달한다 — Runtime Pod끼리 직접
HTTP를 호출하지 않는다.

## 저장소 구조

```text
cmd/csms-server/      Runtime 실행 진입점 (signal 처리, config 로드)
cmd/operator/          Operator 실행 진입점 (controller-runtime manager)
cmd/csms-probe/        OCPP 1.6/2.0.1/2.1 연결 점검용 CLI 클라이언트
internal/runtime/      HTTP 라우팅, config, health, metrics, command API, ownership
internal/handlers/     버전별 typed OCPP handler (Boot/Heartbeat/Status)
internal/sessionregistry/  session ownership interface + in-memory/Redis adapter
internal/commandbus/   cross-Pod command interface + Redis Streams adapter
internal/stationstore/ station/connector repository interface + in-memory/MySQL adapter
internal/controller/   CSMS reconciler (controller-runtime)
api/v1alpha1/          CSMS CRD Go 타입
config/crd/            controller-gen이 생성한 CRD manifest
config/rbac/            Operator ClusterRole/ServiceAccount
config/manager/         Operator Deployment
config/runtime/         Runtime 수동 배포용 kustomize (Operator 미사용 환경)
config/operator/        crd+rbac+manager 통합 kustomize 번들
config/samples/         CSMS 샘플 리소스
config/dependencies/redis/  단일 Redis 참고 manifest 
scripts/                원격 빌드/배포 스크립트
```

## 요구 사항

- Go 1.26.5 이상
- (선택) MySQL 8 — 비어 있으면 in-memory repository로 동작
- (선택) Redis — 비어 있으면 단일 replica 전용으로 동작 (분산 세션 없음)

## 로컬 실행

```sh
go run ./cmd/csms-server
```

기본 endpoint:

| Endpoint | 설명 |
| --- | --- |
| `ws://localhost:8080/{chargePointIdentity}` | OCPP WebSocket (subprotocol: `ocpp1.6`, `ocpp2.0.1`, `ocpp2.1`) |
| `http://localhost:8080/livez` | liveness |
| `http://localhost:8080/readyz` | readiness |
| `http://localhost:8080/metrics` | Prometheus metrics |
| `http://localhost:8080/api/v1/stations/{id}/commands/reset` | 충전기 명령 API (아래 [충전기 명령 API](#충전기-명령-api) 참고) |

## 설정 (환경 변수)

아래는 이 저장소의 참조 Runtime(`cmd/csms-server`)이 읽는 환경 변수다. Operator는
이 이름들을 알지 못한다 — 다른 OCPP 런타임을 배포한다면 그 런타임이 실제로 읽는
env var를 `CSMS.spec.config`에 자유롭게 넣으면 된다(아래
[Kubernetes — Operator](#kubernetes--operator-권장) 참고).

| 이름 | 기본값 | 설명 |
| --- | --- | --- |
| `CSMS_HTTP_ADDR` | `:8080` | HTTP/WebSocket listen 주소 |
| `CSMS_HEARTBEAT_INTERVAL` | `300` | BootNotification 응답 interval(초) |
| `CSMS_SHUTDOWN_TIMEOUT` | `30s` | graceful shutdown 최대 대기 시간 |
| `CSMS_LOG_LEVEL` | `info` | JSON 로그 레벨: `debug`, `info`, `warn`, `error` |
| `CSMS_MYSQL_DSN` | 비어 있음 | MySQL DSN. 비어 있으면 in-memory repository 사용 |
| `CSMS_REDIS_URL` | 비어 있음 | Redis URL. 설정하면 분산 session ownership과 command bus 활성화 |
| `CSMS_API_KEY` | 비어 있음 | 단일 command API Bearer 키. `CSMS_API_KEYS`가 없을 때 사용 |
| `CSMS_API_KEYS` | 비어 있음 | 쉼표로 구분한 command API Bearer 키 목록. 무중단 키 교체용 |
| `CSMS_COMMAND_RATE_LIMIT` | `60` | credential별 분당 command API 요청 한도 |
| `CSMS_INSTANCE_ID` | Pod hostname | session owner로 기록할 Runtime instance identity |
| `CSMS_SESSION_LEASE_TTL` | `30s` | session ownership TTL |
| `CSMS_SESSION_RENEW_INTERVAL` | `10s` | ownership 갱신 주기. TTL보다 짧아야 함(짧지 않으면 시작 시 오류) |
| `CSMS_TLS_CERT_FILE` | `/etc/csms/tls/server/tls.crt` | 서버 인증서 경로. 파일이 존재하면 TLS 자동 활성화 |
| `CSMS_TLS_KEY_FILE` | `/etc/csms/tls/server/tls.key` | 서버 개인키 경로 |
| `CSMS_TLS_CLIENT_CA_FILE` | `/etc/csms/tls/ca/ca.crt` | client 인증서 검증용 CA bundle 경로. 파일이 존재하면 mutual TLS 활성화 |

값 검증에 실패하면(`CSMS_SESSION_RENEW_INTERVAL >= CSMS_SESSION_LEASE_TTL` 등)
프로세스가 시작하지 않고 exit code 2로 종료한다.

## 관측(Observability)

`/metrics`는 `github.com/prometheus/client_golang`으로 다음을 제공한다.

| 지표 | 설명 |
| --- | --- |
| `csms_sessions_active` | 현재 활성 OCPP session 수 (gauge) |
| `csms_command_http_requests_total{class}` | command API HTTP 결과군(`2xx`/`4xx`/`5xx`/`other`)별 요청 수 |
| `csms_command_duration_seconds` | command API 요청 처리 시간 histogram |
| `csms_ocpp_events_total{type,action,version,error_code}` | OCPP 프로토콜 이벤트(session 연결/해제, inbound CALL 수신/완료/거부, outbound Call 전송/완료/실패/timeout/취소) 카운터 |
| `csms_ocpp_event_duration_seconds{type,action,version}` | 위 이벤트 중 duration이 있는 항목의 histogram |

`csms_ocpp_events_total`/`csms_ocpp_event_duration_seconds`는 `ocpp` 라이브러리의
`csms.Config.Metrics` hook(`internal/runtime/metrics.go`)으로 수집한다.

- **charger identity는 label에 넣지 않는다.** 대규모 배포에서 charger 수만큼
  시계열이 생기는 것을 막기 위해서다.
- **`action` label은 알려진 Action(`BootNotification`, `Heartbeat`,
  `StatusNotification`, `Reset`)으로만 채워지고, 그 외 값은 `"unknown"`으로
  정규화한다.** `action`은 원래 인증 없는 WebSocket CALL 프레임에서 그대로 오는
  값이라, 이 정규화가 없으면 누구든 임의의 Action 문자열을 반복 전송해
  `csms_ocpp_events_total`에 무한히 새 시계열을 만들어 프로세스 메모리를 고갈시킬
  수 있었다(Prometheus client는 한번 생긴 label 조합을 회수하지 않는다). 새
  handler를 추가할 때는 `internal/runtime/metrics.go`의 `knownOCPPActions`에도
  반드시 등록해야 한다 — 빠뜨려도 기능은 동작하지만 해당 Action의 metric이
  `"unknown"`으로 집계된다.

## 배포

### 컨테이너 이미지

```sh
docker build -t csms-runtime:0.1.0 .
docker run --rm -p 8080:8080 csms-runtime:0.1.0
```

Runtime은 Go 1.26.5로 빌드하고 `github.com/seanlee0923/ocpp v0.2.0`을 고정
사용한다(로컬 `replace` 없음). non-root distroless multi-stage 이미지다.

### Kubernetes — Operator (권장)

`csms-operator`는 `CSMS` custom resource 하나로 임의의 OCPP 런타임 컨테이너의
Deployment, Service, ConfigMap과 선택적 Ingress/PodDisruptionBudget을 관리한다.
Operator가 실제로 아는 건 `image`와, 그 컨테이너가 응답해야 하는
`port`/`livenessPath`/`readinessPath` 계약뿐이다 — 그 안에서 무엇을 하는지(어떤
OCPP Action을 처리하는지, 인증/결제 로직이 있는지)는 전혀 모른다. MySQL, Redis,
command API Secret도 직접 생성하지 않고 `CSMS.spec`에 지정한 이름의 기존 Secret만
`optional: true`로 참조한다.

```sh
docker build -f Dockerfile.operator -t csms-operator:0.1.0 .
kubectl apply -k config/operator      # CRD + RBAC + Operator Deployment
kubectl apply -f config/samples/csms_v1alpha1_csms.yaml
```

`CSMS.spec` 주요 필드:

| 필드 | 설명 |
| --- | --- |
| `image` | 배포할 OCPP 런타임 컨테이너 이미지. 이 저장소의 `csms-runtime`일 필요는 없다 |
| `replicas` | Runtime replica 수, 기본값 1 |
| `port` | 컨테이너가 HTTP를 서빙하는 포트, 기본값 `8080`. Service와 probe가 이 값을 그대로 사용한다 |
| `livenessPath` | liveness probe 경로, 기본값 `/livez` |
| `readinessPath` | readiness probe 경로, 기본값 `/readyz` |
| `terminationGracePeriodSeconds` | SIGTERM 후 강제 종료까지 대기 시간, 기본값 `30` |
| `databaseSecretName` | Runtime 컨테이너에 envFrom으로 주입할 기존 Secret 이름, 선택. Operator는 이 Secret의 키를 들여다보지 않는다 |
| `redisSecretName` | 위와 동일한 방식으로 주입되는 기존 Secret 이름, 선택 |
| `apiSecretName` | 위와 동일한 방식으로 주입되는 기존 Secret 이름, 선택 |
| `config` | ConfigMap에 그대로 채워지는 임의의 key-value. Operator는 이 값을 해석하거나 기본값을 채우지 않는다 — 배포하는 런타임이 실제로 읽는 env var를 그대로 넣는다 |
| `minAvailable` | 설정하면 PodDisruptionBudget 생성, 비우면 생성하지 않음 |
| `ingress` | 설정하면 Runtime Service를 가리키는 Ingress 생성, 비우면 생성하지 않음(기본값). 아래 [Ingress(선택)](#ingress선택) 참고 |
| `tls` | 설정하면 서버 인증서(+선택적으로 client CA)를 Runtime 컨테이너에 볼륨으로 마운트, 비우면 마운트하지 않음(기본값). Ingress TLS와는 별개다. 아래 [Runtime TLS/mTLS(선택)](#runtime-tlsmtls선택) 참고 |

`redisSecretName` 없이 `replicas`를 1보다 크게 설정하는 조합은 세션 상태가
프로세스 로컬인 Runtime에서는 안전하지 않다(분산 session ownership 없이는 어느
Pod가 어떤 station을 갖고 있는지 알 수 없다). CRD의 CEL validation이 apiserver
단에서 이 조합을 거부한다(`redisSecretName is required when replicas is greater
than 1: ...`). 이 검증은 순전히 replica 개수 관점의 안전장치이며, 배포하는
런타임이 실제로 Redis 기반 분산 세션을 구현했는지는 Operator가 확인할 수
없다 — 세션을 process-local로만 유지하는 런타임이라면 `replicas`는 1로 둔다.

`databaseSecretName`/`redisSecretName`/`apiSecretName`과
`ingress.host`/`ingress.ingressClassName`/`ingress.tlsSecretName`은 값이
있을 때 Kubernetes 리소스 이름/hostname 형식(DNS-1123)을 따르는지도 apiserver
단에서 검증한다 — 오타로 잘못된 이름을 넣었을 때 Pod 생성 실패 같은 우회적인
증상 대신 `kubectl apply` 시점에 바로 거부된다.

이 저장소의 참조 Runtime(`csms-runtime`)은 위 계약을 다음처럼 구현한다: 포트
`8080`, `/livez`·`/readyz`, `CSMS_MYSQL_DSN`/`CSMS_REDIS_URL`/`CSMS_API_KEY(S)`
env var(위 [설정 (환경 변수)](#설정-환경-변수) 참고), Pod 이름을 담은
`CSMS_INSTANCE_ID`. 다른 런타임을 배포한다면 이 이름들을 그대로 따를 필요는
없고, `config`/Secret에 그 런타임이 실제로 읽는 값을 넣으면 된다.

#### 직접 만든 OCPP 런타임 배포하기

이 저장소의 `csms-runtime`은 예시일 뿐, 실제로는 `github.com/seanlee0923/ocpp`
라이브러리로 직접 만든 어떤 런타임이든 이 Operator로 배포할 수 있다. 아래
계약만 지키면 된다.

1. **HTTP 포트 하나에서 응답한다** (`spec.port`, 기본 `8080`). Service와
   probe가 이 포트를 그대로 쓴다.
2. **liveness 경로**(`spec.livenessPath`, 기본 `/livez`)에서 프로세스가
   살아있으면 2xx를 반환한다.
3. **readiness 경로**(`spec.readinessPath`, 기본 `/readyz`)에서 트래픽을
   받을 준비가 됐으면 2xx, 아니면 5xx나 연결 거부를 반환한다. 이 값이 false가
   되면 Kubernetes가 Service Endpoint에서 해당 Pod를 자동으로 뺀다.
4. **SIGTERM을 받으면 `terminationGracePeriodSeconds`(기본 `30`초) 안에**
   진행 중인 OCPP 연결/명령을 정리하고 종료한다. 이 시간을 넘기면
   Kubernetes가 강제 종료(SIGKILL)한다.
5. **필요한 설정은 자기 마음대로 이름 지은 환경 변수로 읽는다.**
   `spec.config`(자유 key-value)와 `databaseSecretName`/`redisSecretName`/
   `apiSecretName`으로 참조한 Secret이 전부 `envFrom`으로 컨테이너에
   주입되므로, Operator가 아는 이름을 따를 필요가 없다.
6. **(선택) `CSMS_INSTANCE_ID`** — Operator가 Pod 이름을 자동으로 이 env var에
   주입해준다. 여러 replica 중 자기 자신이 누구인지 알아야 하는 런타임(예:
   분산 session ownership 구현)이라면 이 값을 쓰면 된다.
7. **`replicas`를 2 이상으로 늘리려면 Runtime이 스스로 분산 session
   ownership을 구현해야 한다.** station의 WebSocket 연결은 항상 하나의 Pod
   메모리에만 존재하므로, 여러 Pod가 있으면 "이 station은 지금 어느 Pod가
   갖고 있는가"를 Runtime이 직접 추적해야 한다(Redis 등으로). CRD의 CEL
   validation은 `redisSecretName`이 있는지만 확인할 뿐, 그 Secret을 실제로
   써서 분산 세션을 구현했는지는 검증할 수 없다 — 검증은 못 하지만 요구사항
   자체는 실재한다.

**절차:**

1. `github.com/seanlee0923/ocpp`로 필요한 OCPP Action의 handler를 등록한다
   (BootNotification, Authorize, StartTransaction, MeterValues 등 실제
   업무에 필요한 만큼).
2. 위 1~4번 HTTP 계약을 구현한다.
3. 인증/결제/과금/DB 등 실제 업무 로직을 붙인다 — 이 부분은 전적으로 직접
   구현해야 한다. Operator도, 이 저장소의 참조 Runtime도 이 로직을 대신 만들어
   주지 않는다.
4. 컨테이너 이미지를 빌드해 registry에 올린다.
5. `CSMS` 리소스에서 `image`/`port`/`livenessPath`/`readinessPath`와 필요한
   `config`/Secret 참조를 자신의 런타임에 맞게 지정한다.
6. `kubectl apply -f`로 배포한다.

**참고할 동작 예시**: 이 저장소의 참조 Runtime이 위 계약을 실제로 구현한
코드다.

| 계약 항목 | 참조 구현 위치 |
| --- | --- |
| OCPP handler 등록 | `internal/handlers/core.go`의 `Register()` |
| liveness/readiness | `internal/runtime/health.go` |
| graceful shutdown | `internal/runtime/server.go`의 `serve()` |
| 분산 session ownership(멀티 replica) | `internal/sessionregistry`, `internal/commandbus` |

같은 패턴을 따라 만들되, `internal/handlers`/`internal/stationstore` 자리에
실제 필요한 업무 로직(인증, 결제, transaction 등)을 채워 넣으면 된다 — 이
저장소는 그 부분을 대신 구현해주지 않는다.

`config/samples/csms_v1alpha1_csms_custom_runtime.yaml`은 이 저장소의
`csms-runtime`이 아닌 다른 이미지, 다른 포트(`9000`), 다른 probe 경로
(`/healthz`, `/ready`), 자체 env var 이름을 쓰는 `CSMS` 예시다. 이 조합이
실제로 배포·헬스체크되는지 원격 클러스터에서 실측 검증했다(다른 이름의
독립 CSMS 리소스를 같은 namespace에 동시에 띄워도 label/selector가 겹치지
않고 각자의 Service가 자기 Pod만 가리키는 것도 함께 확인).

#### Ingress(선택)

`ingress`를 비워 두면(기본값) Operator는 Ingress를 전혀 만들지 않는다. 대부분의
환경에서는 이게 맞는 기본값이다 — TLS 종료, Ingress class, WebSocket용
timeout/buffer annotation은 클러스터/조직마다 다르고 보통 플랫폼팀이 별도로
관리하는 영역이라, `csms-runtime` Service(Operator가 이미 생성)를 그 앞단
Ingress/Gateway가 가리키게 하면 된다. Ingress 없이도 지금 CRD/Runtime은 아무
제약이 없다.

한 `CSMS` 리소스로 전체 노출까지 끝내고 싶을 때만 다음처럼 설정한다.

```yaml
spec:
  ingress:
    host: csms.example.com
    ingressClassName: nginx
    tlsSecretName: csms-runtime-tls   # 기존 cert-manager Certificate/Secret 참조, Operator가 만들지 않음
    annotations:
      nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"   # OCPP heartbeat/장시간 연결 고려
```

| 필드 | 설명 |
| --- | --- |
| `host` | Runtime Service로 라우팅할 hostname (필수) |
| `ingressClassName` | Ingress controller 지정, 비우면 클러스터 기본 IngressClass 사용 |
| `tlsSecretName` | TLS 인증서가 담긴 기존 Secret 이름. Operator는 이 Secret을 만들거나 갱신하지 않는다. 비우면 평문 HTTP로 노출된다(공인망에 노출되는 OCPP endpoint에는 권장하지 않음) |
| `annotations` | 생성되는 Ingress에 그대로 적용. WebSocket 관련 timeout/buffer 등 Ingress controller별 설정에 사용 |

**운영 중 흔한 작업:**

```sh
# 이미지 갱신 (rolling update)
kubectl patch csms csms-runtime --type=merge -p '{"spec":{"image":"csms-runtime:0.1.1"}}'

# replica 조정
kubectl patch csms csms-runtime --type=merge -p '{"spec":{"replicas":3}}'

# 상태 확인
kubectl get csms csms-runtime -o wide
```

두 작업 모두 실제 클러스터에서 Pod restart 없는 rollout으로 검증했다(아래
[운영: 검증된 동작](#운영-검증된-동작) 참고).

#### Runtime TLS/mTLS(선택)

[Ingress(선택)](#ingress선택)와는 별개의 기능이다. Ingress TLS는 클러스터
경계에서 종료되고 그 뒤(Ingress→Service→Pod) 구간은 평문인 반면, 이건
**Runtime 컨테이너 자신이 직접 TLS를 종료**하도록 인증서를 마운트해주는
기능이다. Ingress가 아예 없는 TLS passthrough 환경이거나, client 인증서를
OCPP 레벨에서 직접 검증해야 하는 mTLS(Security Profile 3)가 필요할 때만
쓴다. 대부분의 환경은 Ingress TLS만으로 충분하다.

```yaml
spec:
  tls:
    secretName: csms-runtime-tls          # kubernetes.io/tls Secret (tls.crt/tls.key), Operator가 만들지 않음
    clientCASecretName: csms-runtime-ca   # 선택: 설정하면 mutual TLS(Security Profile 3)
```

| 필드 | 설명 |
| --- | --- |
| `secretName` | 서버 인증서/키가 담긴 기존 Secret. `/etc/csms/tls/server`에 읽기 전용으로 마운트된다(필수) |
| `clientCASecretName` | client 인증서 검증용 CA bundle이 담긴 기존 Secret. `/etc/csms/tls/ca`에 읽기 전용으로 마운트된다. 비우면 client 인증서 검증 없는 TLS(선택) |

`spec.tls`가 설정되면 liveness/readiness probe도 자동으로 HTTPS scheme을
쓴다. Operator는 Secret을 마운트만 할 뿐, 그 안의 인증서로 실제 TLS를
종료하는 건 전적으로 Runtime 컨테이너의 몫이다 — Operator는 Runtime이 이
마운트를 실제로 사용하는지조차 확인할 수 없다.

이 저장소의 참조 Runtime은 이 계약을 실제로 구현한다.

- 시작 시 `/etc/csms/tls/server/tls.crt`·`tls.key`(경로는
  `CSMS_TLS_CERT_FILE`/`CSMS_TLS_KEY_FILE`로 override 가능)가 존재하면
  자동으로 TLS를 활성화한다 — 별도의 on/off 플래그는 없다. 파일이 없으면
  그냥 평문 HTTP로 기존과 동일하게 동작한다(하위 호환).
- `/etc/csms/tls/ca/ca.crt`(`CSMS_TLS_CLIENT_CA_FILE`)가 추가로 존재하면
  mutual TLS 모드로 전환한다. TLS handshake 자체는 client 인증서 없이도
  성공한다(`tls.VerifyClientCertIfGiven`) — kubelet의 liveness/readiness
  probe는 client 인증서를 절대 보내지 않으므로, handshake 단계에서
  인증서를 강제하면(`RequireAndVerifyClientCert`) probe가 영원히 실패해
  Pod가 crash-loop에 빠진다(실제 원격 배포에서 이 문제를 겪고 나서
  고쳤다 — 아래 참고).
- 대신 `ocpp` 라이브러리의 `Security.Profile =
  SecurityProfileTLSClientCertificate`가 OCPP WebSocket upgrade
  시점에서 인증서 자체가 없으면 403으로 거부하고, `Authenticator`가
  제출된 인증서의 CN이 URL 경로의 station identity와 일치하는지 한 번 더
  확인해 불일치 시 마찬가지로 403을 반환한다. `/livez`·`/readyz`·
  `/metrics`는 OCPP upgrade 경로를 타지 않아 이 검사 자체가 적용되지
  않으므로, client 인증서 없이도(또는 CA로 서명됐다면 어떤 CN이든) 정상
  응답한다 — kubelet probe가 계속 통과하는 이유다.
- 인증서/키 중 하나만 있고 다른 하나가 없으면(예: 마운트 실수) 시작 시
  바로 에러를 내고 종료한다 — 조용히 평문으로 fallback하지 않는다.

### Kubernetes — 수동 manifest (Operator 미사용 환경)

Operator 없이 하나의 고정 Deployment만 운영하려면 `config/runtime`을 직접
적용한다.

```sh
kubectl apply -k config/runtime
```

**주의:** `config/runtime`과 `config/operator`(CSMS 기반)는 같은 이름
`csms-runtime`으로 Deployment/Service/ConfigMap을 만들기 때문에 동시에 사용하지
않는다. `Deployment.spec.selector`는 immutable이라 수동으로 만든 리소스를
Operator가 나중에 "입양"할 수 없다. 수동 배포에서 Operator로 전환하려면 활성
연결이 없는 시점에 기존 `config/runtime` 리소스를 삭제한 뒤 `CSMS` 리소스를
적용한다.

### 원격 배포 스크립트

SSH로 접근 가능한 RKE2 단일 노드에 소스를 동기화하고, 이미지를 빌드하여 배포한 뒤
health endpoint까지 확인한다.

```sh
CSMS_REMOTE_HOST=user@server CSMS_REMOTE_PORT=22 ./scripts/deploy-remote.sh
CSMS_REMOTE_HOST=user@server CSMS_REMOTE_PORT=22 ./scripts/deploy-operator-remote.sh
```

| 이름 | 기본값 | 설명 |
| --- | --- | --- |
| `CSMS_REMOTE_DIR` | 원격 사용자의 `~/csms-platform` | 원격 소스 경로 |
| `CSMS_IMAGE` | `csms-runtime:0.1.0` | 빌드하고 배포할 Runtime 이미지 |
| `CSMS_OPERATOR_IMAGE` | `csms-operator:0.1.0` | 빌드하고 배포할 Operator 이미지 |

`scripts/deploy-remote.sh`는 `kubectl apply -k config/runtime`으로 수동
manifest를 적용한다 — Operator가 이미 `csms-runtime` Deployment를 관리 중인
클러스터에서는 selector immutable 제약으로 실패한다. Operator로 전환된 환경에서
이미지만 갱신할 때는 스크립트 대신 위 [운영 중 흔한 작업](#kubernetes--operator-권장)의
`kubectl patch csms` 절차를 사용한다.

두 스크립트 모두 원격 `sudo`가 비밀번호를 요구하면 `ssh -t`로 터미널을 할당해야
비밀번호 프롬프트가 뜬다. 비대화형 환경(CI, 에이전트 세션 등)에서는 sudo
비밀번호를 입력할 방법이 없으므로 containerd image import 단계는 사용자가 직접
실행해야 한다. 인증 정보는 파일이나 명령 인자로 전달하지 않는다.

## 운영: 검증된 동작

아래 동작은 문서상 설계가 아니라 실제 원격 RKE2 클러스터에서 장애를 주입하거나
절차를 수행해 확인한 결과다.

- **Redis 장애 시 프로세스는 죽지 않는다.** 실행 중 Redis 연결이 끊기면 session
  lease 갱신 실패로 해당 WebSocket을 종료하고, command consumer는 중단되며,
  readiness가 false로 전환된다(Service Endpoint에서 자동 제외). Runtime
  프로세스는 종료되지 않고 2초 backoff로 Redis에 재연결을 시도하며, 복구되면
  readiness와 command consumer가 자동으로 돌아온다. Pod restart count는 장애
  전후로 증가하지 않는다.
- **이미지 태그 변경은 Pod restart 없이 rollout된다.** `kubectl patch csms
  ... image` 이후 새 ReplicaSet의 Pod가 정상 기동하고 기존 Pod의 재시작 없이
  전환된다.
- **CSMS spec의 replica 변경이 Deployment에 정확히 반영된다.** `kubectl patch
  csms ... replicas`로 증설/축소하면 `status.readyReplicas`가 그에 맞게
  갱신된다.
- **Operator는 자식 리소스의 수동 drift를 수초 내로 원복한다.** `kubectl scale`,
  `kubectl delete service`, `kubectl patch configmap`으로 자식 리소스를 직접
  건드려도 reconcile loop가 CSMS spec 기준으로 되돌린다.
- **CSMS 삭제 시 자식 리소스가 ownerReferences로 자동 GC된다.** Deployment,
  Service, ConfigMap, PodDisruptionBudget이 함께 정리된다.
- **Operator Pod 자체가 죽어도 leader election으로 복구된다.** 새 Pod가 리더를
  재획득한 뒤 reconcile을 재개한다.

## 세션 소유권과 명령 전달 모델

station WebSocket 연결은 하나의 Runtime Pod 메모리에만 존재한다.

1. Runtime은 연결 시 Redis에 `(station identity, owner Pod, fencing generation,
   TTL)`을 claim하고, 주기적으로 renew하며, 연결 종료 시 release한다.
2. Pod 장애 시에는 TTL 만료로 stale ownership이 자동 정리된다.
3. 외부 API는 station에 명령을 보내기 전에 Redis에서 현재 owner를 조회한다.
4. 명령은 owner Pod 전용 Redis Stream으로 publish되고, owner Pod의 consumer가
   실행한 뒤 결과를 correlation key로 돌려준다.
5. 실행 직전 owner ID와 fencing generation을 다시 검증해 stale owner의 명령
   실행을 막는다.
6. consumer는 5초 이상 pending 상태인 메시지를 `XAUTOCLAIM`으로 회수하고,
   완료된 command ID는 별도 completion record로 남겨 재실행(중복 실행)을 막는다.

Runtime Pod끼리는 서로 직접 HTTP를 호출하지 않는다.

## 보안

- **command API 인증**: `Authorization: Bearer` 헤더를 상수 시간 비교로
  검증한다. 유효한 키가 없으면 API 전체가 503으로 비활성화된다.
- **무중단 키 교체**: `CSMS_API_KEYS`에 기존 키와 새 키를 함께 배포한 뒤
  호출자를 전환하고 기존 키를 제거한다.
- **rate limit**: Redis Lua 원자 카운터로 credential별 분당 요청 수를 제한한다
  (기본 60, `CSMS_COMMAND_RATE_LIMIT`). Redis에 저장되므로 어느 Runtime
  replica로 요청해도 동일하게 적용된다. fixed-window 방식이라 창 경계에서
  이론상 최대 2배 버스트가 가능한 known limitation이 있다.
- **감사 로그**: credential 원문과 command payload는 로그에 남기지 않는다.
  SHA-256 8바이트 fingerprint(`credential_id`), command ID, station identity,
  owner generation, 결과만 기록한다.
- **redaction**: `CSMS_MYSQL_DSN`, `CSMS_REDIS_URL`, API key 원문이 로그에
  출력되는 경로가 없음을 grep으로 전수 확인했다.
- **metrics cardinality**: 위 [관측](#관측observability) 절 참고 — 인증 없는
  WebSocket 클라이언트가 임의 문자열로 `/metrics` 시계열을 무한 증식시키는
  경로를 막아 두었다.

**TLS**: 기본값은 **Security Profile 0**(TLS/mTLS 없음)이다. 두 가지 방식으로
TLS를 켤 수 있다.

- `CSMS.spec.ingress`(위 [Ingress(선택)](#ingress선택))로 앞단에 TLS 종료
  Ingress를 붙이는 방식 — 클러스터 경계에서만 TLS가 걸리고 Ingress 뒤
  Runtime Service까지 구간은 평문이다.
- `CSMS.spec.tls`(위 [Runtime TLS/mTLS(선택)](#runtime-tlsmtls선택))로
  Runtime 컨테이너 자신이 직접 TLS를 종료하는 방식 — `clientCASecretName`을
  함께 설정하면 client 인증서로 충전기를 검증하는 **Security Profile
  3(mutual TLS)**까지 지원한다. 이 경우 `ocpp` 라이브러리의
  `SecurityProfileTLSClientCertificate`가 TLS handshake에서 검증된 client
  인증서를 요구하고, Runtime의 `Authenticator`가 인증서 CN이 station
  identity와 일치하는지 OCPP WebSocket upgrade 시점에 한 번 더 확인한다.

## 충전기 명령 API

현재 연결된 충전기에 Reset 명령을 전달할 수 있다. 요청을 받은 Runtime이 세션을
직접 갖고 있지 않아도 Redis registry에서 owner를 조회해 해당 Runtime으로
전달한다.

```sh
curl -X POST \
  -H "Authorization: Bearer ${CSMS_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"type":"Immediate"}' \
  http://localhost:8080/api/v1/stations/{chargePointIdentity}/commands/reset
```

`type`은 `Immediate` 또는 `OnIdle`이다. OCPP 1.6에서는 각각 `Hard`, `Soft`로
변환하며, OCPP 2.0.1/2.1에서는 선택적으로 `evseId`를 함께 전달할 수 있다.
Kubernetes에서는 `csms-runtime-api` Secret의 `CSMS_API_KEY` key로 인증 정보를
주입한다.

## 데이터 저장소

### MySQL

설정하면 Runtime 시작 시 schema migration을 적용한다. DSN에는
`parseTime=true`와 UTF-8 설정을 포함해야 한다.

```sh
export CSMS_MYSQL_DSN='csms:password@tcp(mysql.example:3306)/csms?parseTime=true&charset=utf8mb4'
go run ./cmd/csms-server
```

- `stations` / `connector_status`: station·connector별 최신 상태
- `boot_events` / `status_events`: BootNotification/StatusNotification 이력
  append
- 최신 상태 갱신과 event append는 하나의 DB transaction으로 처리한다.

Kubernetes에서는 `csms-runtime-database` Secret의 `CSMS_MYSQL_DSN` key로
전달한다. DSN과 비밀번호는 ConfigMap이나 저장소에 기록하지 않는다.

### Redis

Redis URL은 Kubernetes `csms-runtime-redis` Secret의 `CSMS_REDIS_URL` key로
전달한다. Runtime Pod 이름이 `CSMS_INSTANCE_ID`로 자동 주입되며, session
ownership claim/renew/release와 command bus 둘 다 같은 Redis 인스턴스를
사용한다.

`config/dependencies/redis`는 단일 Redis를 구성하기 위한 참고 manifest다.
Operator는 이 리소스를 생성하지 않으며 Runtime은 `csms-runtime-redis` Secret만
참조한다. 가용성이 필요한 환경에서는 이 manifest 대신 외부 HA Redis 또는
managed Redis를 사용한다.

## OCPP 연결 점검

배포된 endpoint에 OCPP BootNotification/StatusNotification을 보내고 연결을
유지하며 outbound Reset command에 응답하는 테스트 클라이언트다.

```sh
go run ./cmd/csms-probe \
  -version ocpp2.0.1 \
  -url ws://localhost:8080/probe-station \
  -hold 30s
```

`-version`은 `ocpp1.6`, `ocpp2.0.1`, `ocpp2.1`을 지원하며 기본값은 `ocpp1.6`이다.

## 테스트 및 검증

```sh
go build ./...
go test ./... -count=1
go test -race ./...
go vet ./...
gofmt -l .
```

`internal/runtime` 패키지 테스트에 세 버전(1.6/2.0.1/2.1) 실제 WebSocket
upgrade 통합 테스트가 포함되어 있어 별도 절차가 필요 없다. `go test ./...`에는
Redis(`miniredis`)와 MySQL(`sqlmock`)을 사용하는 단위/통합 테스트가 포함되며
외부 인프라 없이 실행된다. `internal/commandbus/redisbus`의 pending message
recovery 테스트처럼 실제 Redis가 필요한 일부 테스트는
`CSMS_REDIS_INTEGRATION_URL` 환경 변수가 없으면 스킵된다.

`.github/workflows/ci.yml`이 `main` push와 PR마다 위 명령과 `go.mod`/`go.sum`
정합성, `config/` 아래 모든 kustomize 디렉토리 렌더링, Runtime/Operator 두
컨테이너 이미지 빌드와 Trivy 취약점 스캔(`CRITICAL`, unfixed 제외)을 실행한다.

## ocpp 라이브러리 업그레이드 정책

`ocpp`(`github.com/seanlee0923/ocpp`)는 Keep a Changelog + semver를 따르고,
`v0.x`에서는 breaking change가 `Changed` 섹션에 legitimate하게 들어갈 수 있다.
업그레이드 시 CHANGELOG의 `Added`/`Fixed`뿐 아니라 `Changed`까지 반드시 읽고,
`go.mod`의 버전 문자열만 올린다(로컬 `replace` 금지). 검증은 위
[테스트 및 검증](#테스트-및-검증) 절의 전체 명령을 통과해야 하며, 원격
재배포 후 세 버전 Boot/Status/Heartbeat와 Reset command 왕복을 최소 1회
재확인한다.
