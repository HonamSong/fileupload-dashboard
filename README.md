# File Upload Dashboard

파일 업로드/다운로드 대시보드. Go(순수 Go, CGO 없음) + SQLite 단일 컨테이너, 포트 8180.
로그인·역할 기반 권한(RBAC)·폴더별 접근 제어·API 키(서명 검증)·접근 로그·IP 차단까지 포함합니다.

---

## 주요 기능

### 파일
- **업로드**: 드래그&드롭 또는 클릭, 다중 파일. 모달에서 첨부 개수·총 용량·파일당 한도 표시.
- **덮어쓰기**: 같은 폴더에 동일 이름 업로드 시 링크(id) 유지한 채 내용만 갱신.
- **체크섬**: 업로드 시 SHA-256 자동 계산·표시.
- **미리보기**: 텍스트/이미지 (view 역할은 미리보기 불가).
- **폴더**: 중첩 폴더 생성/삭제/이동, 접기, 폴더별 파일 개수 표시.
- **페이지네이션**: 파일 목록(기본 10, 20/30/50/100), 접근 로그(기본 20, 50/100).
- **리사이즈 레이아웃**: 파일/명령어·미리보기 영역을 드래그로 좌우 조절, 창 높이에 맞춤.

### 다운로드
- **브라우저 다운로드**: 파일 선택 후 다운로드(쿠키 인증). 여러 개 선택 시 **zip으로 묶어** 다운로드.
- **curl 다운로드**: `X-API-Key` 헤더 필수. `GET /d/{id}` 또는 `GET /f/<폴더>/<파일명>`. URL에 키를 넣지 않음.
- **curl 업로드**: `POST /u` (`-F "file=@..." -F "folder=/..."`).
- 파일별 curl 명령어를 대시보드에서 자동 생성(사용 가능한 키를 드롭다운에서 선택, 화면에는 마스킹).

### 삭제 / 휴지통
- 일반 삭제 → 휴지통(약 10일 후 백그라운드 완전삭제), 강제삭제 → 즉시 완전삭제, 휴지통에서 복구.

### API 키
- **개인 키**(사용자당 최대 3개) + **서비스 키**(owner/admin 전용, 개수 제한 없음).
- 스코프: `download` / `upload` / `all(both)`.
- **HMAC 서명**: 발급되는 키에 서버 비밀키로 서명이 포함되어, 위조/변조 키를 암호학적으로 판별(기존 키는 DB 인증으로 하위 호환).
- 발급/비활성화/폐기, 라벨·생성·마지막 사용·사용 횟수 표시. owner/admin은 모든 사용자 키 조회.

### 접근 로그 (owner/admin)
- **업로드·다운로드 통합** 기록: 시각·구분·결과·파일·사용자·API 키·IP·User-Agent.
- **거부된 시도도 기록**: 알 수 없는 키/위조 키/폐기·비활성 키/권한 없음 등 사유와 함께.
- 컬럼 정렬·검색·필터(전체/업로드/다운로드/거부됨), 페이지네이션.
- 로그의 IP에 마우스를 올리면 **차단/허용** 버튼(owner).

### 서버 설정 (owner, 설정 > Server)
- **Base URL**: curl 명령어에 표시되는 주소.
- **IP 접근 제어**: Allow/Block 목록(단일 IP 또는 CIDR). 입력 즉시 형식 검증.
- **자동 차단**: 같은 IP가 지정 시간 안에 잘못된 키로 임계값 이상 시도하면 자동 차단. 차단 목록 관리(해제).

---

## 역할 (RBAC)

| 기능 | owner | admin | user | view |
|------|:-----:|:-----:|:----:|:----:|
| 서버 설정(Base URL·IP·자동차단) | O | X | X | X |
| 사용자 관리 | 전체 | user/view만 | X | X |
| 폴더 권한 관리 | O | O | X | X |
| 업로드·이동·삭제 | 전체 | 전체 | 쓰기 부여 폴더 | X |
| 읽기·다운로드 | 전체 | 전체 | 기본 전체(읽기) | 부여된 폴더만 |
| 미리보기 | O | O | O | X |

- 최초 기동 시 생성되는 admin 계정은 **owner**가 됩니다.
- **폴더별 권한**(화이트리스트, 하위 상속): `user`는 모든 폴더 기본 읽기(폴더별로 쓰기 상향 또는 차단), `view`는 기본 차단(폴더별 읽기 부여). owner/admin은 전체 접근.

---

## 실행

compose 파일은 `docker-compose.sample.yml`(커밋됨)을 복사해서 환경에 맞게 편집합니다. 실제 `docker-compose.yml`은 git에 올라가지 않습니다.

```bash
# 1) 샘플을 복사해서 도메인·비밀번호 등 편집
cp docker-compose.sample.yml docker-compose.yml
#    PUBLIC_BASE_URL, ADMIN_PASSWORD 등을 실제 값으로 수정

# 2) 이미지 빌드 (태그 지정)
docker build -t filemanage-dashboard:latest .

# 3) 이미지 교체 방식으로 기동 (compose는 빌드된 이미지를 그대로 사용)
docker-compose down
docker-compose up -d
```

- 접속: http://localhost:8180 (또는 설정한 도메인)
- 최초 로그인: `admin` / `ADMIN_PASSWORD`(기본 `admin`) → 로그인 후 프로필에서 비밀번호 변경
- 데이터: `./data` (SQLite `app.db` + 업로드 파일 `files/`)

> 샘플에는 `IP_GUARD_DISABLE: "1"`이 포함되어 있어 **최초 기동 시 모든 IP 제한이 해제(복구 모드)**됩니다. 설정 > Server에서 IP 규칙을 구성한 뒤에는 이 값을 `"0"`으로 바꾸거나 제거하세요(그래야 재시작 후에도 제한이 유지됩니다). 현재 세션에만 적용하려면 복구 배너의 "지금 IP 제한 다시 적용" 버튼을 사용합니다.

---

## 환경변수 (docker-compose.yml)

| 변수 | 기본값 | 설명 |
|------|--------|------|
| LISTEN_ADDR | :8180 | 수신 주소 |
| DATA_DIR | /data | 데이터 디렉터리 |
| PUBLIC_BASE_URL | http://localhost:8180 | curl 명령어 기본 주소(설정>Server에서 재정의 가능) |
| ADMIN_USER | admin | 최초 owner 계정 아이디 |
| ADMIN_PASSWORD | admin | 최초 owner 비밀번호(최초 1회만 반영) |
| TRASH_TTL_DAYS | 10 | 휴지통 보관 일수 |
| MAX_UPLOAD_MB | 1024 | 파일당 최대 업로드 크기 |
| PREVIEW_LIMIT_KB | 1024 | 텍스트 미리보기 최대 바이트 |
| IP_GUARD_DISABLE | (없음) | `1`이면 모든 IP 제한(UI·API·차단 목록) 해제 — 잠금 복구/최초 기동용 |

---

## 사용 예시

```bash
# 다운로드 (API 키 탭에서 발급한 키의 <YOUR_API_KEY> 부분만 교체)
curl -H "X-API-Key: fk_xxxxxxxx" -O -J http://localhost:8180/d/<FILE_ID>

# 업로드 (업로드 스코프 키 필요)
curl -H "X-API-Key: fk_xxxxxxxx" -F "file=@./local.txt" -F "folder=/docs" http://localhost:8180/u
```

---

## 프로덕션 배포

이 앱은 **자체 HTTPS가 없고**, IP 로깅·차단이 `X-Forwarded-For`(없으면 `X-Real-IP`) 헤더에 의존합니다.
따라서 **리버스 프록시 뒤에 두고 8180 포트는 외부에 직접 노출하지 마세요.**

1. compose에서 포트를 로컬로만 바인딩: `ports: ["127.0.0.1:8180:8180"]`
2. `PUBLIC_BASE_URL`을 실제 도메인(`https://...`)으로 설정, 강한 `ADMIN_PASSWORD` 지정
3. 앞단에 HTTPS 프록시 배치 — Caddy(자동 인증서) 예:

   ```
   files.example.com {
       reverse_proxy 127.0.0.1:8180
   }
   ```

   nginx라면 `proxy_set_header X-Forwarded-For $remote_addr;`, `X-Real-IP`, `Host`를 전달하고 `client_max_body_size`를 `MAX_UPLOAD_MB`에 맞춥니다.
4. 방화벽에서 80/443만 허용, 8180은 차단
5. 배포 후: 비밀번호 변경 → 설정>Server에서 Base URL·IP 규칙 확인 → 사용자·폴더 권한 부여

> ⚠️ 8180을 직접 노출하면 클라이언트가 `X-Forwarded-For`를 위조해 IP 차단/허용을 우회하거나 로그 IP를 속일 수 있습니다. 반드시 신뢰된 프록시 뒤에 두세요.

### IP 접근 제한 (설정 > Server)

두 가지 IP 접근 제어를 각각 Allow/Block 목록(단일 IP 또는 CIDR)으로 설정합니다.

- **① API 엔드포인트**: curl 다운로드/업로드(`/d`, `/f`, `/u`)에 적용.
- **② 대시보드 UI**: 로그인·관리 화면에만 적용. **API 키 접근은 제외**되므로 UI만 사내/VPN으로 잠그고 다운로드 API는 열어둘 수 있습니다.
- **자동 차단**: 같은 IP가 지정 시간 안에 잘못된 키로 임계값 이상 시도하면 자동 차단. 접근 로그의 IP에서 수동 차단/해제도 가능.

### 잠금 복구 (IP_GUARD_DISABLE)

UI 허용 목록에 본인 IP를 넣지 않으면 대시보드에서 잠깁니다(로그인조차 불가). 복구 절차:

1. 컨테이너에 환경변수 `IP_GUARD_DISABLE=1`을 주고 재시작 → **모든 IP 제한(UI·API·차단 목록)이 일시 해제**되어 대시보드에 진입 가능.
2. 로그인 후 설정 > Server에서 UI 허용 목록을 본인 IP로 수정하고 저장.
3. 상단 복구 배너의 **"지금 IP 제한 다시 적용"** 버튼 클릭 → **재시작 없이** 저장된 규칙으로 즉시 재적용.
4. 환경변수 `IP_GUARD_DISABLE`은 편할 때(다음 배포 시) 제거.

> 도커 포트매핑만 쓰면 클라이언트 IP가 게이트웨이로 보입니다. 실제 방문자 IP로 제한하려면 프록시에서 `X-Forwarded-For`를 전달하세요.

### 데이터 백업

모든 상태는 `./data`에 있습니다. WAL 정합성을 위해 컨테이너를 멈추고 복사하세요.

```bash
docker compose stop && tar czf backup-$(date +%F).tgz data && docker compose start
```

### 업데이트

```bash
git pull
docker build -t filemanage-dashboard:latest .   # 새 이미지 빌드
docker-compose down                              # 기존 컨테이너 정리
docker-compose up -d                             # 새 이미지로 교체 기동
```

UI(HTML/JS/CSS)는 `Cache-Control: no-cache`로 서빙되므로 배포 후 일반 새로고침으로 반영됩니다.

---

## 기술 스택

- Go 표준 net/http (Go 1.22+ ServeMux 패턴 라우팅)
- modernc.org/sqlite (순수 Go, WAL) · golang.org/x/crypto/bcrypt
- 바닐라 JS 단일 페이지 대시보드
