# shellcheck shell=bash
# Single sign-on with a real Keycloak in the cluster: a user in a mapped group signs in as
# operator through the browser flow (played with curl), and one in no mapped group is
# refused. Keycloak runs on HTTPS with a self-signed certificate, reachable from the host
# under its service name through a port-forward.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the sign-in settings of the installed release"
  return 0
fi

KC_HOST=keycloak.smoke-kc.svc
KC_PORT=${KC_PORT:-18443}
KC_ISSUER="https://$KC_HOST:$KC_PORT/realms/costdeck"
KC_CURL=(curl -sS -k --resolve "$KC_HOST:$KC_PORT:127.0.0.1")
OIDC_ON='{"auth":{"oidc":{"enabled":true,"displayName":"Keycloak","issuerUrl":"'"$KC_ISSUER"'","clientId":"costdeck","clientSecret":"smoke-secret","redirectUrl":"http://localhost:'"$API_PORT"'/api/auth/oidc/callback","groupsClaim":"groups","rolesClaim":"realm_access.roles","skipSslVerify":true,"autoProvision":false,"groupRoleMapping":{"/costdeck-operators":"operator"}}}}'

log "Keycloak with a realm for Cost Deck"
new_namespace smoke-kc
certs=$(mktemp -d)
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=$KC_HOST" -addext "subjectAltName=DNS:$KC_HOST" \
  -keyout "$certs/tls.key" -out "$certs/tls.crt" >/dev/null 2>&1 || fail "creating Keycloak's certificate"
kubectl create secret tls keycloak-tls -n smoke-kc --cert "$certs/tls.crt" --key "$certs/tls.key" >/dev/null
rm -rf "$certs"
kubectl apply -f - >/dev/null <<YAML || fail "applying Keycloak"
apiVersion: v1
kind: ConfigMap
metadata: {name: realm, namespace: smoke-kc}
data:
  costdeck.json: |
    {
      "realm": "costdeck", "enabled": true,
      "groups": [{"name": "costdeck-operators"}],
      "users": [
        {"username": "alice", "enabled": true, "email": "alice@example.com", "emailVerified": true, "firstName": "Alice", "lastName": "Ops",
         "credentials": [{"type": "password", "value": "alice-password", "temporary": false}], "groups": ["/costdeck-operators"]},
        {"username": "bob", "enabled": true, "email": "bob@example.com", "emailVerified": true, "firstName": "Bob", "lastName": "Else",
         "credentials": [{"type": "password", "value": "bob-password", "temporary": false}]}
      ],
      "clients": [{
        "clientId": "costdeck", "enabled": true, "protocol": "openid-connect", "publicClient": false, "secret": "smoke-secret",
        "standardFlowEnabled": true, "directAccessGrantsEnabled": false,
        "redirectUris": ["http://localhost:$API_PORT/api/auth/oidc/callback"],
        "protocolMappers": [{"name": "groups", "protocol": "openid-connect", "protocolMapper": "oidc-group-membership-mapper",
          "config": {"claim.name": "groups", "full.path": "true", "id.token.claim": "true", "access.token.claim": "true", "userinfo.token.claim": "true"}}]
      }]
    }
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: keycloak, namespace: smoke-kc}
spec:
  replicas: 1
  selector: {matchLabels: {app: keycloak}}
  template:
    metadata: {labels: {app: keycloak}}
    spec:
      containers:
        - name: keycloak
          image: quay.io/keycloak/keycloak:26.4.2
          args: [start-dev, --import-realm, --https-port=8443, --http-enabled=false,
            --https-certificate-file=/certs/tls.crt, --https-certificate-key-file=/certs/tls.key,
            "--hostname=https://$KC_HOST:$KC_PORT"]
          env: [{name: KC_BOOTSTRAP_ADMIN_USERNAME, value: admin}, {name: KC_BOOTSTRAP_ADMIN_PASSWORD, value: admin}]
          volumeMounts:
            - {name: realm, mountPath: /opt/keycloak/data/import}
            - {name: certs, mountPath: /certs}
          readinessProbe: {tcpSocket: {port: 8443}, periodSeconds: 3}
          resources: {requests: {cpu: 100m, memory: 512Mi}}
      volumes:
        - {name: realm, configMap: {name: realm}}
        - {name: certs, secret: {secretName: keycloak-tls}}
---
apiVersion: v1
kind: Service
metadata: {name: keycloak, namespace: smoke-kc}
spec: {selector: {app: keycloak}, ports: [{port: $KC_PORT, targetPort: 8443}]}
YAML
kubectl rollout status deploy/keycloak -n smoke-kc --timeout=300s >/dev/null || fail "Keycloak did not start"
kubectl port-forward -n smoke-kc svc/keycloak "$KC_PORT:$KC_PORT" >/dev/null 2>&1 &
PF_PIDS="$PF_PIDS $!"
TIMEOUT=180 wait_for "the realm is served" "${KC_CURL[@]}" -f -o /dev/null "$KC_ISSUER/.well-known/openid-configuration"

log "Cost Deck signs Keycloak users in, with the role their group maps to"
track_call PUT /api/settings '{"auth":{"oidc":{"enabled":false}}}'
api PUT /api/settings "$OIDC_ON" >/dev/null || fail "enabling OIDC sign-in"
api POST /api/settings/providers/oidc/test '{}' | jq -e '.connected' >/dev/null || fail "the connection test fails: $(api POST /api/settings/providers/oidc/test '{}')"
ok "the connection test finds the issuer"
curl -sS "localhost:$API_PORT/api/auth/config" | jq -e '.oidc.enabled and .oidc.displayName == "Keycloak"' >/dev/null || fail "the login page does not offer Keycloak"
ok "the login page offers Keycloak"

# kc_sign_in <user> <password>: plays the browser with the cookie jar $KC_JAR and prints
# where the callback sends it. It runs in a subshell: the caller creates the jar.
kc_sign_in() {
  local login form action callback
  login=$(curl -sS -c "$KC_JAR" -b "$KC_JAR" -o /dev/null -w '%{redirect_url}' "localhost:$API_PORT/api/auth/oidc/login?return=/budgets")
  form=$("${KC_CURL[@]}" -c "$KC_JAR" -b "$KC_JAR" "$login")
  action=$(echo "$form" | tr '\n' ' ' | sed -n 's/.*<form[^>]*id="kc-form-login"[^>]*action="\([^"]*\)".*/\1/p' | sed 's/&amp;/\&/g')
  [[ -n "$action" ]] || action=$(echo "$form" | tr '\n' ' ' | sed -n 's/.*<form[^>]*action="\([^"]*\)"[^>]*id="kc-form-login".*/\1/p' | sed 's/&amp;/\&/g')
  [[ -n "$action" ]] || { echo "no login form"; return; }
  callback=$("${KC_CURL[@]}" -c "$KC_JAR" -b "$KC_JAR" -o /dev/null -w '%{redirect_url}' --data-urlencode "username=$1" --data-urlencode "password=$2" "$action")
  curl -sS -c "$KC_JAR" -b "$KC_JAR" -o /dev/null -w '%{redirect_url}' "$callback"
}

KC_JAR=$(mktemp)
landed=$(kc_sign_in alice alice-password)
[[ "$landed" == "http://localhost:$API_PORT/budgets" ]] || fail "alice's sign-in ended at $landed"
me=$(curl -sS -b "$KC_JAR" "localhost:$API_PORT/api/auth/me")
echo "$me" | jq -e '.role == "operator" and .provider == "oidc" and .email == "alice@example.com"' >/dev/null || fail "alice's session is wrong: $me"
rm -f "$KC_JAR"
ok "alice signs in as operator through Keycloak"
KC_JAR=$(mktemp)
landed=$(kc_sign_in bob bob-password)
[[ "$landed" == *"sso_error=user_not_provisioned"* ]] || fail "bob, in no mapped group, ended at $landed"
[[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$KC_JAR" "localhost:$API_PORT/api/auth/me")" == 401 ]] || fail "bob got a session"
rm -f "$KC_JAR"
ok "bob, in no mapped group, is refused"
