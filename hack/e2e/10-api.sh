# shellcheck shell=bash
# Dashboard and API basics, sign-in, API tokens, local users and RBAC.

log "The dashboard and the API documentation are served"
expect_code 200 "dashboard at /" GET / "" -b "$JAR"
curl -sf -b "$JAR" "localhost:$API_PORT/" | grep -q 'id="root"' || fail "/ does not serve the dashboard"
expect_code 200 "OpenAPI document" GET /api/openapi.yaml
expect_code 404 "an unknown API path is a 404, not the dashboard" GET /api/no-such-thing "" -b "$JAR"

log "Sign-in is required, and wrong passwords are refused"
expect_code 401 "unauthenticated API call" GET /api/scaling/groups
expect_code 401 "wrong password" POST /api/login "{\"username\":\"$ADMIN_USER\",\"password\":\"not-the-password\"}"

log "API tokens carry their role and stop working when deleted"
TOKEN=$(api POST /api/tokens '{"name":"smoke-viewer","role":"viewer","expiresInDays":1}' | jq -r .token)
track_call DELETE /api/tokens/smoke-viewer
[[ "$TOKEN" == cdk_* ]] || fail "token creation returned no cdk_ token"
expect_code 200 "a viewer token reads" GET /api/namespaces "" -H "Authorization: Bearer $TOKEN"
expect_code 403 "a viewer token cannot change settings" PUT /api/settings '{}' -H "Authorization: Bearer $TOKEN"
expect_code 204 "deleting the token" DELETE /api/tokens/smoke-viewer "" -b "$JAR"
expect_code 401 "a deleted token is refused" GET /api/namespaces "" -H "Authorization: Bearer $TOKEN"

log "A local user must change the password an administrator set"
USER_PASSWORD="Smoke-$(date +%s)-initial"
api POST /api/users "{\"username\":\"smoke-user\",\"role\":\"operator\",\"password\":\"$USER_PASSWORD\",\"mustChangePassword\":true}" | jq -e '.user.username == "smoke-user"' >/dev/null \
  || fail "creating a local user"
track_call DELETE /api/users/smoke-user
USER_JAR=$(mktemp)
curl -sf -c "$USER_JAR" -H 'Content-Type: application/json' -d "{\"username\":\"smoke-user\",\"password\":\"$USER_PASSWORD\"}" \
  "localhost:$API_PORT/api/login" >/dev/null || fail "local user login"
expect_code 403 "the API is closed until the password is changed" GET /api/scaling/groups "" -b "$USER_JAR"
expect_code 200 "changing the password" POST /api/auth/password \
  "{\"currentPassword\":\"$USER_PASSWORD\",\"newPassword\":\"$USER_PASSWORD-changed\"}" -b "$USER_JAR" -c "$USER_JAR"
expect_code 200 "the API opens after the change" GET /api/scaling/groups "" -b "$USER_JAR"
expect_code 403 "an operator cannot manage users" GET /api/users "" -b "$USER_JAR"
expect_code 204 "deleting the user" DELETE /api/users/smoke-user "" -b "$JAR"
wait_for "the deleted user's session ends" bash -c "[[ \$(curl -s -o /dev/null -w '%{http_code}' -b '$USER_JAR' localhost:$API_PORT/api/scaling/groups) == 401 ]]"
rm -f "$USER_JAR"

log "RBAC: Secrets only in the operator namespace"
SA="system:serviceaccount:$NS:$RELEASE"
[[ "$(kubectl auth can-i list secrets --as="$SA" -A)" == no ]] || fail "operator can list Secrets cluster-wide"
[[ "$(kubectl auth can-i get secrets --as="$SA" -n "$NS")" == yes ]] || fail "operator cannot read its own Secrets"
ok "Secret access is namespaced"
